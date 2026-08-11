package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// TokenPrefix marks CityConnect personal access tokens.
//
// It is cc_pat_, not c2_pat_: these are *our* credentials, and a c2_ prefix
// sitting next to real C2 client secrets in a config file is a debugging trap
// waiting to happen.
const TokenPrefix = "cc_pat_"

// Token scopes. A token is additionally constrained by its owner's role and
// department, so a scope can only ever narrow access, never widen it.
const (
	ScopeRequestsRead  = "requests:read"
	ScopeRequestsWrite = "requests:write"
	ScopeContactsRead  = "contacts:read"
	ScopeContactsWrite = "contacts:write"
	ScopeNotifySend    = "notifications:send"
	ScopeReportsRead   = "reports:read"
	ScopeAll           = "*"
)

// IssuedToken carries the one-time plaintext alongside the stored record.
type IssuedToken struct {
	Token  string           `json:"token"`
	Record *domain.ApiToken `json:"record"`
}

// IssueTokenInput describes a new personal access token.
type IssueTokenInput struct {
	Name      string
	OwnerID   string
	SystemID  string
	Scopes    []string
	ReadOnly  bool
	ExpiresIn time.Duration
}

// IssueToken mints a token and returns the plaintext exactly once. Only the
// SHA-256 hash is persisted, so a database disclosure does not hand over
// working credentials.
func (s *Service) IssueToken(ctx context.Context, actor audit.Actor, in IssueTokenInput) (*IssuedToken, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: token name is required", ErrInvalidInput)
	}
	if in.OwnerID == "" && in.SystemID == "" {
		return nil, fmt.Errorf("%w: a token needs either an owner or a connected system", ErrInvalidInput)
	}

	secret, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	plaintext := TokenPrefix + secret

	rec := &domain.ApiToken{
		Name:        strings.TrimSpace(in.Name),
		Prefix:      plaintext[:len(TokenPrefix)+6],
		TokenHash:   hashToken(plaintext),
		OwnerUserID: in.OwnerID,
		SystemID:    in.SystemID,
		Scopes:      domain.StringList(in.Scopes),
		ReadOnly:    in.ReadOnly,
	}
	if in.ExpiresIn > 0 {
		exp := time.Now().UTC().Add(in.ExpiresIn)
		rec.ExpiresAt = &exp
	}

	if err := s.db.WithContext(ctx).Create(rec).Error; err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "token.issued", TargetType: "api_token", TargetID: rec.ID,
		Summary: "issued API token " + rec.Name,
		Changes: domain.JSONMap{"scopes": in.Scopes, "readOnly": in.ReadOnly, "systemId": in.SystemID},
	})

	return &IssuedToken{Token: plaintext, Record: rec}, nil
}

// TokenPrincipal is the caller identified by a personal access token.
type TokenPrincipal struct {
	Token  *domain.ApiToken
	User   *domain.User
	System *domain.ConnectedSystem
}

// AuthenticateToken resolves a bearer token to its principal.
func (s *Service) AuthenticateToken(ctx context.Context, raw string) (*TokenPrincipal, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, TokenPrefix) {
		return nil, ErrNoSession
	}

	var tok domain.ApiToken
	if err := s.db.WithContext(ctx).Where("token_hash = ?", hashToken(raw)).First(&tok).Error; err != nil {
		return nil, ErrNoSession
	}
	now := time.Now().UTC()
	if !tok.Usable(now) {
		return nil, ErrNoSession
	}

	p := &TokenPrincipal{Token: &tok}

	if tok.OwnerUserID != "" {
		var u domain.User
		if err := s.db.WithContext(ctx).First(&u, "id = ?", tok.OwnerUserID).Error; err != nil {
			return nil, ErrNoSession
		}
		if u.Status != domain.UserActive {
			return nil, ErrSuspended
		}
		p.User = &u
	}

	if tok.SystemID != "" {
		var sys domain.ConnectedSystem
		if err := s.db.WithContext(ctx).First(&sys, "id = ?", tok.SystemID).Error; err != nil {
			return nil, ErrNoSession
		}
		if !sys.Active {
			return nil, ErrSuspended
		}
		p.System = &sys
	}

	// Record use at most once a minute; a write per API call would make this
	// table the bottleneck for integration traffic.
	if tok.LastUsedAt == nil || now.Sub(*tok.LastUsedAt) > time.Minute {
		s.db.WithContext(ctx).Model(&tok).UpdateColumn("last_used_at", now)
	}
	return p, nil
}

// HasScope reports whether a token grants the given scope.
func (p *TokenPrincipal) HasScope(scope string) bool {
	if p.Token == nil {
		return false
	}
	if len(p.Token.Scopes) == 0 {
		return false
	}
	return p.Token.Scopes.Contains(ScopeAll) || p.Token.Scopes.Contains(scope)
}

// ListTokens returns tokens, optionally narrowed to one owner or system.
func (s *Service) ListTokens(ctx context.Context, ownerID, systemID string) ([]domain.ApiToken, error) {
	q := s.db.WithContext(ctx).Model(&domain.ApiToken{}).Where("revoked_at IS NULL")
	if ownerID != "" {
		q = q.Where("owner_user_id = ?", ownerID)
	}
	if systemID != "" {
		q = q.Where("system_id = ?", systemID)
	}
	var out []domain.ApiToken
	err := q.Order("created_at DESC").Find(&out).Error
	return out, store.Translate(err)
}

// RevokeToken disables a token immediately.
func (s *Service) RevokeToken(ctx context.Context, actor audit.Actor, id string) error {
	res := s.db.WithContext(ctx).Model(&domain.ApiToken{}).
		Where("id = ? AND revoked_at IS NULL", id).
		UpdateColumn("revoked_at", time.Now().UTC())
	if res.Error != nil {
		return store.Translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "token.revoked", TargetType: "api_token", TargetID: id,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Connected systems
// ---------------------------------------------------------------------------

// ListSystems returns the connected systems.
func (s *Service) ListSystems(ctx context.Context, includeInactive bool) ([]domain.ConnectedSystem, error) {
	q := s.db.WithContext(ctx).Model(&domain.ConnectedSystem{})
	if !includeInactive {
		q = q.Where("active = ?", true)
	}
	var out []domain.ConnectedSystem
	err := q.Order("name ASC").Find(&out).Error
	return out, store.Translate(err)
}

// GetSystem loads one connected system.
func (s *Service) GetSystem(ctx context.Context, id string) (*domain.ConnectedSystem, error) {
	var sys domain.ConnectedSystem
	err := s.db.WithContext(ctx).Preload("Queues").First(&sys, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &sys, store.Translate(err)
}

// SaveSystem creates or updates a connected system, generating a webhook
// signing secret on creation.
func (s *Service) SaveSystem(ctx context.Context, actor audit.Actor, sys *domain.ConnectedSystem, queueIDs []string) (*domain.ConnectedSystem, error) {
	sys.Code = strings.ToUpper(strings.TrimSpace(sys.Code))
	if sys.Code == "" || strings.TrimSpace(sys.Name) == "" {
		return nil, fmt.Errorf("%w: system code and name are required", ErrInvalidInput)
	}

	action := "system.created"
	if sys.ID != "" {
		action = "system.updated"
	} else if sys.WebhookSecret == "" {
		secret, err := randomToken(32)
		if err != nil {
			return nil, err
		}
		sys.WebhookSecret = secret
	}

	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Save(sys).Error; err != nil {
			return err
		}
		if queueIDs == nil {
			return nil
		}
		if err := tx.Where("connected_system_id = ?", sys.ID).Delete(&domain.QueueSystem{}).Error; err != nil {
			return err
		}
		rows := make([]domain.QueueSystem, 0, len(queueIDs))
		now := time.Now().UTC()
		for _, qid := range queueIDs {
			if qid != "" {
				rows = append(rows, domain.QueueSystem{QueueID: qid, ConnectedSystemID: sys.ID, JoinedAt: now})
			}
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "connected_system", TargetID: sys.ID, Summary: sys.Name,
	})
	return sys, nil
}

// RotateWebhookSecret issues a new signing secret and returns it once.
func (s *Service) RotateWebhookSecret(ctx context.Context, actor audit.Actor, id string) (string, error) {
	secret, err := randomToken(32)
	if err != nil {
		return "", err
	}
	res := s.db.WithContext(ctx).Model(&domain.ConnectedSystem{}).
		Where("id = ?", id).UpdateColumn("webhook_secret", secret)
	if res.Error != nil {
		return "", store.Translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return "", ErrNotFound
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "system.secret_rotated", TargetType: "connected_system", TargetID: id,
	})
	return secret, nil
}
