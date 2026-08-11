// Package portal is the public, citizen-facing surface: a resident signs in
// with C2, reports a problem from the service catalogue, and follows what
// happens to it.
//
// It is deliberately a separate package from the staff services, with its own
// session type and its own entry points, because the two surfaces have
// opposite trust models:
//
//   - Staff access is deny-by-default. C2 authenticates *citizens*, so an
//     unknown subject must never become an agent.
//   - Citizen access is open by design. Any resident may report a pothole, so
//     a first sign-in provisions a contact — which is safe precisely because
//     every read here is scoped to that one contact's own records.
//
// The scoping is the security boundary, and it is applied here rather than in
// the handlers: no method takes a caller-supplied contact or request owner.
package portal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/c2/oidc"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var (
	ErrNoSession    = errors.New("portal: no active session")
	ErrInvalidState = errors.New("portal: unknown or expired sign-in")
	ErrNotFound     = errors.New("portal: not found")
	ErrInvalidInput = errors.New("portal: invalid input")
	ErrNotPermitted = errors.New("portal: not permitted")
)

// Service implements the citizen portal.
type Service struct {
	db       *gorm.DB
	cfg      *config.Config
	oidc     *oidc.Provider
	contacts *contacts.Service
	catalog  *catalog.Service
	requests *requests.Service
	audit    *audit.Service
	log      *slog.Logger
}

// NewService builds the portal service.
func NewService(
	db *gorm.DB, cfg *config.Config, provider *oidc.Provider,
	cont *contacts.Service, cat *catalog.Service, reqs *requests.Service,
	aud *audit.Service, log *slog.Logger,
) *Service {
	return &Service{
		db: db, cfg: cfg, oidc: provider, contacts: cont, catalog: cat,
		requests: reqs, audit: aud, log: log.With("component", "portal"),
	}
}

// ---------------------------------------------------------------------------
// Sign-in
// ---------------------------------------------------------------------------

// StartLogin begins an authorization-code flow for the portal.
//
// Pass silent=true for a prompt=none probe: a resident who already holds a live
// C2 session is signed in without any UI, and C2 answers login_required instead
// of prompting when they do not.
func (s *Service) StartLogin(ctx context.Context, returnTo string, silent bool) (string, error) {
	// A single-origin deployment has no separate portal callback; the shared
	// one dispatches by audience, so fall back to it rather than sending an
	// empty redirect_uri that C2 would reject.
	redirect := s.cfg.C2.PortalRedirectURL
	if redirect == "" {
		redirect = s.cfg.C2.RedirectURL
	}

	req, err := s.oidc.AuthorizeFor(ctx, redirect, silent)
	if err != nil {
		return "", err
	}

	flow := domain.LoginFlow{
		State: req.State, Nonce: req.Nonce, CodeVerifier: req.CodeVerifier,
		ReturnTo:    sanitizeReturnTo(returnTo),
		Audience:    domain.AudienceCitizen,
		Silent:      silent,
		RedirectURL: req.RedirectURL,
		ExpiresAt:   time.Now().UTC().Add(10 * time.Minute),
	}
	if err := s.db.WithContext(ctx).Create(&flow).Error; err != nil {
		return "", fmt.Errorf("portal: persist sign-in: %w", err)
	}
	return req.URL, nil
}

// LoginResult carries a completed portal sign-in.
type LoginResult struct {
	SessionToken string
	ExpiresAt    time.Time
	Contact      *domain.Contact
	ReturnTo     string
}

// CompleteLogin finishes the redirect back from C2 and opens a portal session.
//
// Unlike staff sign-in, an unrecognised subject is provisioned rather than
// refused: this is the public front door, and the account it creates can only
// ever see its own records.
func (s *Service) CompleteLogin(ctx context.Context, code, state, userAgent, ip string) (*LoginResult, error) {
	var flow domain.LoginFlow
	if err := s.db.WithContext(ctx).Where("state = ?", state).First(&flow).Error; err != nil {
		return nil, ErrInvalidState
	}
	s.db.WithContext(ctx).Delete(&flow)

	// A flow started for the staff console must not be completed here, or the
	// audience recorded at the start would mean nothing.
	if flow.Audience != domain.AudienceCitizen {
		return nil, ErrInvalidState
	}
	if time.Now().UTC().After(flow.ExpiresAt) {
		return nil, ErrInvalidState
	}

	tokens, err := s.oidc.ExchangeFor(ctx, code, flow.CodeVerifier, flow.RedirectURL)
	if err != nil {
		return nil, err
	}
	claims, err := s.oidc.VerifyIDToken(ctx, tokens.IDToken, flow.Nonce)
	if err != nil {
		return nil, err
	}
	if tokens.AccessToken != "" {
		if err := s.oidc.MergeUserInfo(ctx, tokens.AccessToken, claims); err != nil {
			s.log.WarnContext(ctx, "portal userinfo fetch failed; continuing on id_token claims",
				"error", err, "c2_sub", claims.Subject)
		}
	}

	contact, err := s.contacts.EnsureByC2Sub(ctx,
		audit.C2Actor(claims.Subject), claims.Subject, claims.Name, claims.Email)
	if err != nil {
		return nil, err
	}

	token, session, err := s.openSession(ctx, contact, claims.Subject, tokens.IDToken, userAgent, ip)
	if err != nil {
		return nil, err
	}

	return &LoginResult{
		SessionToken: token, ExpiresAt: session.ExpiresAt,
		Contact: contact, ReturnTo: flow.ReturnTo,
	}, nil
}

func (s *Service) openSession(ctx context.Context, contact *domain.Contact, sub, idToken, userAgent, ip string) (string, *domain.CitizenSession, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()

	session := domain.CitizenSession{
		TokenHash: hashToken(raw), ContactID: contact.ID, C2Sub: sub,
		ExpiresAt: now.Add(s.cfg.Sec.SessionTTL), LastSeen: now,
		UserAgent: truncate(userAgent, 400), IP: ip, IDTokenHint: idToken,
	}
	if err := s.db.WithContext(ctx).Create(&session).Error; err != nil {
		return "", nil, fmt.Errorf("portal: create session: %w", err)
	}
	return raw, &session, nil
}

// Authenticate resolves a portal cookie to the contact it belongs to.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (*domain.Contact, *domain.CitizenSession, error) {
	if rawToken == "" {
		return nil, nil, ErrNoSession
	}

	var session domain.CitizenSession
	if err := s.db.WithContext(ctx).Where("token_hash = ?", hashToken(rawToken)).First(&session).Error; err != nil {
		return nil, nil, ErrNoSession
	}
	now := time.Now().UTC()
	if !session.Active(now, s.cfg.Sec.SessionIdleTTL) {
		return nil, nil, ErrNoSession
	}

	contact, err := s.contacts.Get(ctx, session.ContactID)
	if err != nil {
		return nil, nil, ErrNoSession
	}
	// A merged contact's records now live on the survivor; follow the merge so
	// a citizen does not lose sight of their own history.
	if contact.MergedIntoID != "" {
		if survivor, err := s.contacts.Get(ctx, contact.MergedIntoID); err == nil {
			contact = survivor
		}
	}

	if now.Sub(session.LastSeen) > time.Minute {
		s.db.WithContext(ctx).Model(&session).UpdateColumn("last_seen", now)
	}
	return contact, &session, nil
}

// Logout ends the portal session and returns C2's end-session URL.
func (s *Service) Logout(ctx context.Context, rawToken string) (string, error) {
	var session domain.CitizenSession
	if err := s.db.WithContext(ctx).Where("token_hash = ?", hashToken(rawToken)).First(&session).Error; err != nil {
		return "", ErrNoSession
	}
	s.db.WithContext(ctx).Model(&session).Updates(map[string]any{
		"revoked_at": time.Now().UTC(), "revoke_reason": "user_logout",
	})

	// The portal's own return address. The console's is a different origin and
	// a different registration; C2 rejects it from here.
	url, err := s.oidc.EndSessionURLFor(ctx, session.IDTokenHint, s.cfg.C2.PortalPostLogoutRedirectURL)
	if err != nil {
		return "", nil
	}
	return url, nil
}

// RevokeForSubject ends every portal session a C2 subject holds.
//
// Called from back-channel logout: C2's token identifies the user, not one
// session, so a citizen signing out of the portal on one device must be
// signed out everywhere.
func (s *Service) RevokeForSubject(ctx context.Context, sub string) (int64, error) {
	res := s.db.WithContext(ctx).Model(&domain.CitizenSession{}).
		Where("c2_sub = ? AND revoked_at IS NULL", sub).
		Updates(map[string]any{
			"revoked_at": time.Now().UTC(), "revoke_reason": "backchannel_logout",
		})
	return res.RowsAffected, store.Translate(res.Error)
}

// PurgeExpired clears spent portal sessions.
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).
		Where("expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)", now, now.Add(-7*24*time.Hour)).
		Delete(&domain.CitizenSession{})
	return res.RowsAffected, store.Translate(res.Error)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("portal: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func sanitizeReturnTo(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	return raw
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
