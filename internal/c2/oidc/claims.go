package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Claims is the decoded payload of a C2-issued token. Audience is a slice
// because the OIDC spec allows either a string or an array; JSON unmarshalling
// normalises both into this shape.
type Claims struct {
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"`
	Audience  Audience `json:"aud"`
	ExpiresAt int64    `json:"exp"`
	IssuedAt  int64    `json:"iat"`
	Nonce     string   `json:"nonce,omitempty"`
	JTI       string   `json:"jti,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	AuthTime  int64    `json:"auth_time,omitempty"`

	// Standard profile claims, released only with the matching scopes.
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	Name          string `json:"name,omitempty"`
	GivenName     string `json:"given_name,omitempty"`
	FamilyName    string `json:"family_name,omitempty"`
	Phone         string `json:"phone_number,omitempty"`

	Events map[string]any `json:"events,omitempty"`

	// Extra holds every claim not named above, including Trust Adapter claims
	// released by a consent scope (residency_status and the like). An absent
	// claim means "unverified", never "no" — C2 omits rather than fabricates.
	Extra map[string]any `json:"-"`
}

// Audience decodes the `aud` claim from either a string or an array.
type Audience []string

// UnmarshalJSON implements json.Unmarshaler.
func (a *Audience) UnmarshalJSON(b []byte) error {
	var single string
	if err := json.Unmarshal(b, &single); err == nil {
		*a = Audience{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("oidc: aud is neither string nor array: %w", err)
	}
	*a = many
	return nil
}

// Contains reports whether the audience includes the given client id.
func (a Audience) Contains(clientID string) bool {
	for _, v := range a {
		if v == clientID {
			return true
		}
	}
	return false
}

// Scopes splits the space-delimited scope claim.
func (c *Claims) Scopes() []string {
	if c.Scope == "" {
		return nil
	}
	return strings.Fields(c.Scope)
}

// HasScope reports whether the citizen consented to the given scope.
func (c *Claims) HasScope(want string) bool {
	return contains(c.Scopes(), want)
}

// StringClaim returns an extra claim as a string. It returns ("", false) when
// the claim is absent — which for a Trust Adapter claim means the citizen has
// not onboarded or has not consented, and must be treated as unverified rather
// than as a negative answer.
func (c *Claims) StringClaim(name string) (string, bool) {
	v, ok := c.Extra[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// boolClaim coerces a JSON claim to a bool, accepting both the native bool and
// the string forms ("true"/"false") some providers emit for email_verified.
func boolClaim(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

func decodeClaims(payload []byte) (*Claims, error) {
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("%w: decode claims: %v", ErrInvalidToken, err)
	}
	if err := json.Unmarshal(payload, &c.Extra); err != nil {
		return nil, fmt.Errorf("%w: decode extra claims: %v", ErrInvalidToken, err)
	}
	return &c, nil
}

// checkCore validates the claims every C2 token shares: issuer, audience and
// expiry. `aud` must equal our client_id — a mismatch is the silent 401 that
// happens when a second OAuth client gets registered under the same
// application and C2 starts signing for that one instead.
func (p *Provider) checkCore(c *Claims) error {
	if c.Issuer != p.cfg.Issuer {
		return fmt.Errorf("%w: got %q, want %q", ErrIssuerMismatch, c.Issuer, p.cfg.Issuer)
	}
	if !c.Audience.Contains(p.cfg.ClientID) {
		return fmt.Errorf("%w: got %v, want %q", ErrAudMismatch, []string(c.Audience), p.cfg.ClientID)
	}
	if c.Subject == "" {
		return fmt.Errorf("%w: no subject", ErrInvalidToken)
	}

	t := now()
	skew := p.cfg.ClockSkew
	if c.ExpiresAt == 0 {
		return fmt.Errorf("%w: no expiry", ErrInvalidToken)
	}
	if t.After(time.Unix(c.ExpiresAt, 0).Add(skew)) {
		return ErrExpired
	}
	if c.IssuedAt != 0 && time.Unix(c.IssuedAt, 0).After(t.Add(skew)) {
		return fmt.Errorf("%w: issued in the future", ErrInvalidToken)
	}
	return nil
}

// VerifyIDToken validates an id_token end to end: RS256 signature against the
// JWKS key named by `kid`, exact issuer, audience equal to our client id,
// unexpired, and the nonce we generated for this login attempt.
//
// Only after all of that is `sub` trustworthy. `sub` is opaque and stable —
// not an email, not a name — and it is the same value that arrives in Service
// Card callout assertions and back-channel logout tokens, which is why it is
// what we store as the identity link.
func (p *Provider) VerifyIDToken(ctx context.Context, token, expectedNonce string) (*Claims, error) {
	payload, err := p.verify(ctx, token)
	if err != nil {
		return nil, err
	}
	claims, err := decodeClaims(payload)
	if err != nil {
		return nil, err
	}
	if err := p.checkCore(claims); err != nil {
		return nil, err
	}
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return nil, ErrNonceMismatch
	}
	return claims, nil
}

// VerifyCalloutAssertion validates the short-lived RS256 bearer JWT C2 sends
// with a Service Card callout. It is verified against the same JWKS as
// id_tokens, so there is no extra secret to manage.
//
// The assertion lives about 60 seconds; every check must pass before `sub` is
// mapped to a contact, and any failure fails closed with a 4xx.
func (p *Provider) VerifyCalloutAssertion(ctx context.Context, token string) (*Claims, error) {
	payload, err := p.verify(ctx, token)
	if err != nil {
		return nil, err
	}
	claims, err := decodeClaims(payload)
	if err != nil {
		return nil, err
	}
	if err := p.checkCore(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// VerifyLogoutToken validates a back-channel logout token.
//
// Beyond the shared checks it enforces the two rules specific to this token
// type: the events claim must carry the back-channel logout member, and a
// `nonce` must be absent — the spec forbids one, and accepting a logout token
// with a nonce would accept a replayed id_token as a logout instruction.
//
// C2's logout token is sub-based and carries no `sid`, so the caller cannot
// target one session: every session held by that subject must end.
func (p *Provider) VerifyLogoutToken(ctx context.Context, token string) (*Claims, error) {
	payload, err := p.verify(ctx, token)
	if err != nil {
		return nil, err
	}
	claims, err := decodeClaims(payload)
	if err != nil {
		return nil, err
	}
	if err := p.checkCore(claims); err != nil {
		return nil, err
	}
	if claims.Nonce != "" {
		return nil, fmt.Errorf("%w: logout_token must not contain a nonce", ErrInvalidToken)
	}
	if _, ok := claims.Events[backchannelLogoutEvent]; !ok {
		return nil, fmt.Errorf("%w: logout_token missing the back-channel logout event", ErrInvalidToken)
	}
	if claims.JTI == "" {
		return nil, fmt.Errorf("%w: logout_token has no jti", ErrInvalidToken)
	}
	return claims, nil
}
