// Package agents owns staff identity and access: the C2 login flow, sessions,
// users, departments, personal access tokens and connected systems.
//
// C2 SSO is the only staff login path. There are no local passwords, which
// makes three things load-bearing and all of them live here: the bootstrap
// that creates the first admin, the deny-by-default policy for a C2 subject
// with no CityConnect user, and the back-channel logout that ends every
// session a subject holds.
package agents

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
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service-level errors. Handlers map these to status codes.
var (
	ErrNoAccess      = errors.New("agents: no CityConnect access for this identity")
	ErrSuspended     = errors.New("agents: account suspended")
	ErrInvalidState  = errors.New("agents: unknown or expired login state")
	ErrNoSession     = errors.New("agents: no active session")
	ErrNotFound      = errors.New("agents: not found")
	ErrForbidden     = errors.New("agents: forbidden")
	ErrConflict      = errors.New("agents: conflict")
	ErrInvalidInput  = errors.New("agents: invalid input")
	ErrLastAdmin     = errors.New("agents: cannot remove the last active admin")
	ErrLoginRequired = errors.New("agents: interactive login required")
)

// Service implements staff identity and access control.
type Service struct {
	db    *gorm.DB
	cfg   *config.Config
	oidc  *oidc.Provider
	audit *audit.Service
	log   *slog.Logger
}

// NewService builds the agents service.
func NewService(db *gorm.DB, cfg *config.Config, provider *oidc.Provider, aud *audit.Service, log *slog.Logger) *Service {
	return &Service{db: db, cfg: cfg, oidc: provider, audit: aud, log: log.With("component", "agents")}
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

// StartLogin begins an authorization-code flow and returns the URL to redirect
// the browser to. The per-attempt verifier, state and nonce are persisted
// rather than kept in memory so a login survives a rolling restart.
func (s *Service) StartLogin(ctx context.Context, returnTo string, silent bool) (string, error) {
	req, err := s.oidc.Authorize(ctx, silent)
	if err != nil {
		return "", err
	}

	flow := domain.LoginFlow{
		State:        req.State,
		Nonce:        req.Nonce,
		CodeVerifier: req.CodeVerifier,
		ReturnTo:     sanitizeReturnTo(returnTo),
		Silent:       silent,
		ExpiresAt:    time.Now().UTC().Add(10 * time.Minute),
	}
	if err := s.db.WithContext(ctx).Create(&flow).Error; err != nil {
		return "", fmt.Errorf("agents: persist login flow: %w", err)
	}
	return req.URL, nil
}

// LoginResult carries the outcome of a completed login.
type LoginResult struct {
	SessionToken string
	ExpiresAt    time.Time
	User         *domain.User
	ReturnTo     string
}

// CompleteLogin handles the redirect back from C2: it verifies state, exchanges
// the code, validates the id_token, resolves the staff user, and opens a
// session.
func (s *Service) CompleteLogin(ctx context.Context, code, state, userAgent, ip string) (*LoginResult, error) {
	var flow domain.LoginFlow
	err := s.db.WithContext(ctx).Where("state = ?", state).First(&flow).Error
	if err != nil {
		return nil, ErrInvalidState
	}
	// Consume the flow immediately: state is single-use, and leaving it around
	// would allow a replay of the same authorization response.
	s.db.WithContext(ctx).Delete(&flow)

	if time.Now().UTC().After(flow.ExpiresAt) {
		return nil, ErrInvalidState
	}

	tokens, err := s.oidc.Exchange(ctx, code, flow.CodeVerifier)
	if err != nil {
		return nil, err
	}

	claims, err := s.oidc.VerifyIDToken(ctx, tokens.IDToken, flow.Nonce)
	if err != nil {
		return nil, err
	}

	// C2 releases profile/email from UserInfo rather than in the id_token in the
	// authorization-code flow (OIDC Core §5.4), so fetch them now — this is what
	// populates email and its verification state for invitation binding. A
	// failure here is not fatal: a user already bound to this subject signs in
	// on `sub` alone, and the one path that needs the email (first-time
	// invitation binding) fails closed when it is absent.
	if tokens.AccessToken != "" {
		if err := s.oidc.MergeUserInfo(ctx, tokens.AccessToken, claims); err != nil {
			s.log.WarnContext(ctx, "UserInfo fetch failed; continuing on id_token claims only",
				"error", err, "c2_sub", claims.Subject)
		}
	}

	user, err := s.resolveStaffUser(ctx, claims)
	if err != nil {
		return nil, err
	}

	token, session, err := s.openSession(ctx, user, tokens.IDToken, userAgent, ip)
	if err != nil {
		return nil, err
	}

	s.audit.Record(ctx, audit.UserActor(user.ID, user.Email, ip), audit.Entry{
		Action:     "auth.login",
		TargetType: "user",
		TargetID:   user.ID,
		Summary:    "signed in via C2 SSO",
	})

	return &LoginResult{
		SessionToken: token,
		ExpiresAt:    session.ExpiresAt,
		User:         user,
		ReturnTo:     flow.ReturnTo,
	}, nil
}

// resolveStaffUser maps a verified C2 subject onto a staff user.
//
// The policy is deny-by-default: a valid C2 identity with no CityConnect user
// gets no access. Auto-provisioning anyone who can authenticate to a *citizen*
// identity provider would hand the agent console to the entire public.
//
// An invited user is matched on email exactly once and pinned to the subject
// from then on, because `sub` is the stable identifier and email is not.
func (s *Service) resolveStaffUser(ctx context.Context, claims *oidc.Claims) (*domain.User, error) {
	var user domain.User

	err := s.db.WithContext(ctx).Where("c2_sub = ?", claims.Subject).First(&user).Error
	if err == nil {
		return s.activateUser(ctx, &user, claims)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.Translate(err)
	}

	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		s.logDenied(ctx, claims, "the token carried no email to match an invitation against")
		return nil, ErrNoAccess
	}

	// Binding an invitation is trust-on-first-use: whoever first presents a
	// matching email claims it, and from then on the account is pinned to their
	// subject. C2 asserts email_verified truthfully, so we require a verified
	// address before binding — otherwise anyone who sets an unverified copy of
	// an invited admin's email on their own C2 identity could seize the
	// invitation. High-privilege accounts should be provisioned by subject
	// (`ccadm grant-role --sub`), which skips this path entirely. The check
	// precedes the lookup so an unverified caller cannot even probe which
	// addresses have pending invitations.
	if !claims.EmailVerified {
		s.logDenied(ctx, claims, "email is present but not verified by C2; provision by subject with grant-role instead")
		return nil, ErrNoAccess
	}

	err = s.db.WithContext(ctx).
		Where("LOWER(email) = ? AND (c2_sub IS NULL OR c2_sub = '')", email).
		First(&user).Error
	if err != nil {
		s.logDenied(ctx, claims, "no invitation matches this email, and the subject is unknown")
		return nil, ErrNoAccess
	}

	s.log.InfoContext(ctx, "invitation bound to a C2 identity",
		"email", email, "c2_sub", claims.Subject, "role", user.Role)

	user.C2Sub = claims.Subject
	return s.activateUser(ctx, &user, claims)
}

// logDenied records who was turned away and how to admit them.
//
// Deny-by-default is correct — C2 authenticates citizens, so auto-provisioning
// would hand the console to the public — but it makes "I signed in and got
// nothing" the most common first-run experience. The subject identifier is
// opaque and the person being denied cannot see it, so without this line an
// administrator has no way to find the value they need to grant.
func (s *Service) logDenied(ctx context.Context, claims *oidc.Claims, why string) {
	s.log.WarnContext(ctx, "sign-in denied: no CityConnect account for this identity",
		"reason", why,
		"c2_sub", claims.Subject,
		"email", claims.Email,
		"name", claims.Name,
		"remedy", fmt.Sprintf("ccadm grant-role --sub %q --role admin", claims.Subject),
	)
}

func (s *Service) activateUser(ctx context.Context, user *domain.User, claims *oidc.Claims) (*domain.User, error) {
	if user.Status == domain.UserSuspended {
		return nil, ErrSuspended
	}

	now := time.Now().UTC()
	user.Status = domain.UserActive
	user.LastLoginAt = &now
	if user.Name == "" && claims.Name != "" {
		user.Name = claims.Name
	}
	if claims.Email != "" {
		user.Email = strings.ToLower(claims.Email)
	}

	err := s.db.WithContext(ctx).Model(user).
		Select("c2_sub", "status", "last_login_at", "name", "email").
		Updates(user).Error
	if err != nil {
		return nil, store.Translate(err)
	}
	return user, nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (s *Service) openSession(ctx context.Context, user *domain.User, idToken, userAgent, ip string) (string, *domain.Session, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()

	session := domain.Session{
		TokenHash:   hashToken(raw),
		UserID:      user.ID,
		C2Sub:       user.C2Sub,
		ExpiresAt:   now.Add(s.cfg.Sec.SessionTTL),
		LastSeen:    now,
		UserAgent:   truncate(userAgent, 400),
		IP:          ip,
		IDTokenHint: idToken,
	}
	if err := s.db.WithContext(ctx).Create(&session).Error; err != nil {
		return "", nil, fmt.Errorf("agents: create session: %w", err)
	}
	return raw, &session, nil
}

// Authenticate resolves a session cookie value to its user, sliding the idle
// window forward.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (*domain.User, *domain.Session, error) {
	if rawToken == "" {
		return nil, nil, ErrNoSession
	}

	var session domain.Session
	err := s.db.WithContext(ctx).Where("token_hash = ?", hashToken(rawToken)).First(&session).Error
	if err != nil {
		return nil, nil, ErrNoSession
	}

	now := time.Now().UTC()
	if !session.Active(now, s.cfg.Sec.SessionIdleTTL) {
		return nil, nil, ErrNoSession
	}

	var user domain.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", session.UserID).Error; err != nil {
		return nil, nil, ErrNoSession
	}
	if user.Status != domain.UserActive {
		return nil, nil, ErrSuspended
	}

	// Slide the idle window, but only once a minute — a write on every request
	// would make the session table the hottest thing in the database.
	if now.Sub(session.LastSeen) > time.Minute {
		s.db.WithContext(ctx).Model(&session).UpdateColumn("last_seen", now)
		session.LastSeen = now
	}
	return &user, &session, nil
}

// Logout ends the local session and returns the C2 end-session URL. Both
// halves matter: clearing only the local session leaves the C2 session alive,
// so the user stays SSO'd and walks straight back in.
func (s *Service) Logout(ctx context.Context, rawToken, ip string) (string, error) {
	var session domain.Session
	if err := s.db.WithContext(ctx).Where("token_hash = ?", hashToken(rawToken)).First(&session).Error; err != nil {
		return "", ErrNoSession
	}

	s.revokeSession(ctx, &session, "user_logout")
	s.audit.Record(ctx, audit.UserActor(session.UserID, "", ip), audit.Entry{
		Action: "auth.logout", TargetType: "user", TargetID: session.UserID,
		Summary: "signed out",
	})

	url, err := s.oidc.EndSessionURL(ctx, session.IDTokenHint)
	if err != nil {
		// A local logout that succeeded is still a logout; report the C2 leg
		// as unavailable rather than failing the whole operation.
		s.log.WarnContext(ctx, "could not build C2 end_session URL", "error", err)
		return "", nil
	}
	return url, nil
}

func (s *Service) revokeSession(ctx context.Context, session *domain.Session, reason string) {
	now := time.Now().UTC()
	s.db.WithContext(ctx).Model(session).Updates(map[string]any{
		"revoked_at":    now,
		"revoke_reason": reason,
	})
}

// BackchannelLogout handles C2's server-to-server logout notification.
//
// The logout token is sub-based and carries no `sid`, so a single session
// cannot be targeted: every session that subject holds must end. Replays are
// harmless but are deduplicated on `jti` anyway.
func (s *Service) BackchannelLogout(ctx context.Context, logoutToken string) error {
	claims, err := s.oidc.VerifyLogoutToken(ctx, logoutToken)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&domain.Session{}).
		Where("c2_sub = ? AND revoked_at IS NULL", claims.Subject).
		Updates(map[string]any{"revoked_at": now, "revoke_reason": "backchannel_logout"})
	if res.Error != nil {
		return store.Translate(res.Error)
	}

	s.audit.Record(ctx, audit.C2Actor(claims.Subject), audit.Entry{
		Action:     "auth.backchannel_logout",
		TargetType: "c2_subject",
		TargetID:   claims.Subject,
		Summary:    fmt.Sprintf("C2 ended %d session(s)", res.RowsAffected),
		Changes:    domain.JSONMap{"jti": claims.JTI, "sessions": res.RowsAffected},
	})

	s.log.InfoContext(ctx, "back-channel logout applied",
		"sub", claims.Subject, "sessions_revoked", res.RowsAffected)
	return nil
}

// ListSessions returns a user's live sessions, for the account screen and for
// supervisors investigating access.
func (s *Service) ListSessions(ctx context.Context, userID string) ([]domain.Session, error) {
	var out []domain.Session
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now().UTC()).
		Order("last_seen DESC").Find(&out).Error
	return out, store.Translate(err)
}

// RevokeUserSessions ends every session a user holds, used when an admin
// suspends an account.
func (s *Service) RevokeUserSessions(ctx context.Context, userID, reason string) (int64, error) {
	res := s.db.WithContext(ctx).Model(&domain.Session{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Updates(map[string]any{"revoked_at": time.Now().UTC(), "revoke_reason": reason})
	return res.RowsAffected, store.Translate(res.Error)
}

// PurgeExpired clears spent sessions and abandoned login flows.
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	s.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&domain.LoginFlow{})

	res := s.db.WithContext(ctx).
		Where("expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)", now, now.Add(-7*24*time.Hour)).
		Delete(&domain.Session{})
	return res.RowsAffected, store.Translate(res.Error)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("agents: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// sanitizeReturnTo keeps post-login redirects inside this application. An
// open redirect on the login callback is a phishing primitive.
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
