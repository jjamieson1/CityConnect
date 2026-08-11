package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/jjamieson1/CityConnect/internal/config"
)

// Verification failures. Callers map these to 401s; none of them should ever
// leak a reason to the caller beyond "unauthorized".
var (
	ErrInvalidToken   = errors.New("oidc: invalid token")
	ErrIssuerMismatch = errors.New("oidc: issuer mismatch")
	ErrAudMismatch    = errors.New("oidc: audience mismatch")
	ErrExpired        = errors.New("oidc: token expired")
	ErrNonceMismatch  = errors.New("oidc: nonce mismatch")
)

// backchannelLogoutEvent is the member a logout token must carry.
const backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// Provider is the client half of the C2 relationship.
type Provider struct {
	cfg       config.C2Config
	client    *http.Client
	discovery *discoveryCache
	keys      *keySet
}

// New builds a Provider. It performs no network I/O; discovery happens lazily
// on first use and is exercised by Check at boot.
func New(cfg config.C2Config) *Provider {
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	disc := &discoveryCache{
		url:    cfg.DiscoveryURL(),
		issuer: cfg.Issuer,
		client: client,
		ttl:    cfg.DiscoveryCacheTTL,
	}
	return &Provider{
		cfg:       cfg,
		client:    client,
		discovery: disc,
		keys: &keySet{
			discovery:  disc,
			client:     client,
			minRefresh: cfg.JWKSMinRefreshEvery,
		},
	}
}

// Discovery returns the cached provider metadata.
func (p *Provider) Discovery(ctx context.Context) (*Discovery, error) {
	return p.discovery.get(ctx)
}

// Check resolves discovery and the JWKS so a misconfiguration surfaces at boot
// and on the readiness probe rather than on a user's first login.
func (p *Provider) Check(ctx context.Context) error {
	doc, err := p.discovery.get(ctx)
	if err != nil {
		return err
	}
	if err := p.keys.refresh(ctx); err != nil {
		return err
	}
	if len(doc.CodeChallengeMethods) > 0 && !contains(doc.CodeChallengeMethods, "S256") {
		return fmt.Errorf("oidc: provider does not advertise S256 PKCE support")
	}
	return nil
}

// ClientID is the OAuth client id C2 signs tokens for. It is the `aud` we
// expect on every inbound token.
func (p *Provider) ClientID() string { return p.cfg.ClientID }

// Issuer is the exact `iss` string every C2 token must carry.
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// ---------------------------------------------------------------------------
// Authorization request
// ---------------------------------------------------------------------------

// AuthRequest carries the per-attempt secrets for one login.
type AuthRequest struct {
	URL          string
	State        string
	Nonce        string
	CodeVerifier string
	// RedirectURL must be replayed verbatim at the token exchange.
	RedirectURL string
}

// Authorize builds an authorization-code request with PKCE.
//
// It deliberately sends neither `prompt=login` nor `max_age`. Either one makes
// C2 re-prompt for credentials on every visit even when the user already has
// an active session, which defeats SSO entirely — and some client libraries
// add them by default, serialising a "means unset" zero into `max_age=0`.
// Building the query explicitly here is what keeps that from happening;
// TestAuthorizeOmitsReauthParams asserts it.
//
// Pass silent=true to add `prompt=none` for a background "is the user still
// signed in?" check, which never shows UI and returns error=login_required
// instead.
func (p *Provider) Authorize(ctx context.Context, silent bool) (*AuthRequest, error) {
	return p.AuthorizeFor(ctx, p.cfg.RedirectURL, silent)
}

// AuthorizeFor builds the request against a specific redirect_uri.
//
// The console and the portal are served from different origins so they do not
// share a cookie jar, which means each has its own callback — and C2 matches
// redirect URIs exactly, so the value cannot be inferred.
func (p *Provider) AuthorizeFor(ctx context.Context, redirectURL string, silent bool) (*AuthRequest, error) {
	doc, err := p.discovery.get(ctx)
	if err != nil {
		return nil, err
	}

	state, err := randomString(32)
	if err != nil {
		return nil, err
	}
	nonce, err := randomString(32)
	if err != nil {
		return nil, err
	}
	verifier, err := randomString(48) // 64 base64url chars, inside the 43-128 range
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.cfg.ClientID)
	q.Set("redirect_uri", redirectURL)
	q.Set("scope", strings.Join(p.cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if silent {
		q.Set("prompt", "none")
	}

	sep := "?"
	if strings.Contains(doc.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return &AuthRequest{
		URL:          doc.AuthorizationEndpoint + sep + q.Encode(),
		State:        state,
		Nonce:        nonce,
		CodeVerifier: verifier,
		RedirectURL:  redirectURL,
	}, nil
}

// TokenResponse is the token endpoint's reply.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
}

// Exchange trades an authorization code for tokens. Confidential clients
// authenticate with the client secret in addition to PKCE.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (*TokenResponse, error) {
	return p.ExchangeFor(ctx, code, verifier, p.cfg.RedirectURL)
}

// ExchangeFor trades a code using the redirect_uri the authorize request used.
// A mismatch is rejected by C2, which is the point: it binds the code to the
// surface that started the flow.
func (p *Provider) ExchangeFor(ctx context.Context, code, verifier, redirectURL string) (*TokenResponse, error) {
	doc, err := p.discovery.get(ctx)
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURL) // must match the authorize request exactly
	form.Set("client_id", p.cfg.ClientID)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oidc: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if p.cfg.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var oe oauthError
		_ = json.NewDecoder(resp.Body).Decode(&oe)
		return nil, fmt.Errorf("oidc: token endpoint returned %d: %s %s", resp.StatusCode, oe.Error, oe.Description)
	}

	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("oidc: decode token response: %w", err)
	}
	if tr.IDToken == "" {
		return nil, fmt.Errorf("oidc: token response carried no id_token")
	}
	return &tr, nil
}

type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// EndSessionURL builds the RP-initiated logout redirect. Clearing only the
// local session leaves the C2 session alive, so the user stays SSO'd and walks
// straight back in.
func (p *Provider) EndSessionURL(ctx context.Context, idTokenHint string) (string, error) {
	return p.EndSessionURLFor(ctx, idTokenHint, p.cfg.PostLogoutRedirectURL)
}

// EndSessionURLFor builds the sign-out URL with a specific
// post_logout_redirect_uri.
//
// The console and the portal are different origins with different audiences,
// so they return to different places. C2 matches these exactly, the same as
// redirect URIs: sending the console's value from the portal does not merely
// land the citizen on a staff page, it is rejected by C2 — so each surface
// needs its own registration.
func (p *Provider) EndSessionURLFor(ctx context.Context, idTokenHint, postLogoutRedirectURL string) (string, error) {
	doc, err := p.discovery.get(ctx)
	if err != nil {
		return "", err
	}
	if doc.EndSessionEndpoint == "" {
		return "", fmt.Errorf("oidc: provider advertises no end_session_endpoint")
	}

	q := url.Values{}
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	if postLogoutRedirectURL != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirectURL)
	}
	q.Set("client_id", p.cfg.ClientID)

	sep := "?"
	if strings.Contains(doc.EndSessionEndpoint, "?") {
		sep = "&"
	}
	return doc.EndSessionEndpoint + sep + q.Encode(), nil
}

// UserInfo fetches fresh claims with the bearer access token. The access token
// is opaque — identity comes from the id_token or from here, never from
// parsing the access token.
func (p *Provider) UserInfo(ctx context.Context, accessToken string) (map[string]any, error) {
	doc, err := p.discovery.get(ctx)
	if err != nil {
		return nil, err
	}
	if doc.UserInfoEndpoint == "" {
		return nil, fmt.Errorf("oidc: provider advertises no userinfo_endpoint")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.UserInfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: userinfo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrExpired
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: userinfo returned %d", resp.StatusCode)
	}

	var claims map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&claims); err != nil {
		return nil, fmt.Errorf("oidc: decode userinfo: %w", err)
	}
	return claims, nil
}

// MergeUserInfo fetches the UserInfo claims with the access token and overlays
// the standard profile fields onto the already-verified id_token claims.
//
// In the authorization-code flow C2 releases profile/email from UserInfo, not
// in the id_token (OIDC Core §5.4 — and C2's per-client id_token assertion is
// off by default), so this is how CityConnect learns the user's email and,
// critically, whether C2 has verified it.
//
// The UserInfo `sub` MUST equal the id_token `sub` (OIDC Core §5.3.2): a
// mismatch means the access token was minted for a different subject, so the
// response is rejected rather than trusted. Only fields UserInfo actually
// returns are copied, so this never clobbers a value the id_token already
// carried.
func (p *Provider) MergeUserInfo(ctx context.Context, accessToken string, claims *Claims) error {
	info, err := p.UserInfo(ctx, accessToken)
	if err != nil {
		return err
	}

	sub, _ := info["sub"].(string)
	if sub == "" || sub != claims.Subject {
		return fmt.Errorf("%w: userinfo sub %q does not match id_token sub", ErrInvalidToken, sub)
	}

	if v, ok := info["email"].(string); ok && v != "" {
		claims.Email = v
	}
	if raw, ok := info["email_verified"]; ok {
		claims.EmailVerified = boolClaim(raw)
	}
	if v, ok := info["name"].(string); ok && v != "" {
		claims.Name = v
	}
	if v, ok := info["given_name"].(string); ok && v != "" {
		claims.GivenName = v
	}
	if v, ok := info["family_name"].(string); ok && v != "" {
		claims.FamilyName = v
	}
	if v, ok := info["phone_number"].(string); ok && v != "" {
		claims.Phone = v
	}
	return nil
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oidc: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// parseSigned parses a compact JWS, restricting the algorithm to RS256. C2
// signs everything with RS256; accepting anything else — `none` above all — is
// the classic JWT verification hole.
func parseSigned(token string) (*jose.JSONWebSignature, error) {
	jws, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if len(jws.Signatures) != 1 {
		return nil, fmt.Errorf("%w: expected exactly one signature", ErrInvalidToken)
	}
	return jws, nil
}

// verify checks a compact JWS against the JWKS and returns its payload.
func (p *Provider) verify(ctx context.Context, token string) ([]byte, error) {
	jws, err := parseSigned(token)
	if err != nil {
		return nil, err
	}
	kid := jws.Signatures[0].Header.KeyID

	key, err := p.keys.keyFor(ctx, kid)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	payload, err := jws.Verify(key)
	if err != nil {
		return nil, fmt.Errorf("%w: signature: %v", ErrInvalidToken, err)
	}
	return payload, nil
}

// now is overridable in tests.
var now = time.Now
