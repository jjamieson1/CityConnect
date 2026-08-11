package oidc

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jjamieson1/CityConnect/devtools/c2stub"
	"github.com/jjamieson1/CityConnect/internal/config"
)

// newTestProvider stands up a c2stub and points a Provider at it.
func newTestProvider(t *testing.T) (*Provider, *c2stub.Server, *httptest.Server) {
	t.Helper()

	stub, err := c2stub.New(c2stub.Options{ClientID: "cityconnect-test", ClientSecret: "shh"})
	if err != nil {
		t.Fatalf("new stub: %v", err)
	}
	srv := httptest.NewServer(stub.Handler())
	t.Cleanup(srv.Close)

	issuer := srv.URL + "/oidc"
	stub.SetIssuer(issuer)

	p := New(config.C2Config{
		PortalOrigin:          srv.URL,
		Issuer:                issuer,
		ClientID:              "cityconnect-test",
		ClientSecret:          "shh",
		RedirectURL:           "http://localhost:4021/cityconnect/api/auth/callback",
		PostLogoutRedirectURL: "http://localhost:4021/cityconnect/",
		Scopes:                []string{"openid", "profile", "email"},
		DiscoveryCacheTTL:     time.Minute,
		JWKSMinRefreshEvery:   0,
		HTTPTimeout:           5 * time.Second,
		ClockSkew:             time.Minute,
	})
	return p, stub, srv
}

func TestDiscoveryUsesAdvertisedEndpoints(t *testing.T) {
	p, _, srv := newTestProvider(t)

	doc, err := p.Discovery(context.Background())
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}

	// The token endpoint is /oidc/oauth/token, not the /oidc/token that every
	// hardcoding client assumes. Reading it from discovery is the whole point.
	want := srv.URL + "/oidc/oauth/token"
	if doc.TokenEndpoint != want {
		t.Errorf("token_endpoint = %q, want %q", doc.TokenEndpoint, want)
	}
	if doc.Issuer != srv.URL+"/oidc" {
		t.Errorf("issuer = %q, want %q", doc.Issuer, srv.URL+"/oidc")
	}
}

func TestDiscoveryRejectsIssuerMismatch(t *testing.T) {
	stub, err := c2stub.New(c2stub.Options{ClientID: "cityconnect-test"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(stub.Handler())
	defer srv.Close()
	stub.SetIssuer(srv.URL + "/oidc")

	// Configuring the issuer as C2's internal API host is the classic mistake:
	// discovery still resolves, and then every token fails to validate. Catch
	// it at discovery time with a message that says what to do.
	p := New(config.C2Config{
		Issuer:            "http://localhost:8088/oidc",
		ClientID:          "cityconnect-test",
		DiscoveryCacheTTL: time.Minute,
		HTTPTimeout:       5 * time.Second,
	})
	// Point the cache at the stub while keeping the wrong expected issuer.
	p.discovery.url = srv.URL + "/oidc/.well-known/openid-configuration"

	_, err = p.Discovery(context.Background())
	if err == nil {
		t.Fatal("expected an issuer mismatch error")
	}
	if !strings.Contains(err.Error(), "does not match configured issuer") {
		t.Errorf("error did not explain the mismatch: %v", err)
	}
}

// TestAuthorizeOmitsReauthParams guards the single most expensive mistake in
// this integration. `prompt=login` or `max_age=0` makes C2 re-prompt for
// credentials on every visit even with an active session, silently destroying
// SSO — and client libraries add them by default, serialising a "means unset"
// zero straight into max_age=0.
func TestAuthorizeOmitsReauthParams(t *testing.T) {
	p, _, _ := newTestProvider(t)

	req, err := p.Authorize(context.Background(), false)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	q := u.Query()

	if q.Has("prompt") {
		t.Errorf("authorize URL must not send prompt for a normal login, got %q", q.Get("prompt"))
	}
	if q.Has("max_age") {
		t.Errorf("authorize URL must not send max_age, got %q", q.Get("max_age"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Error("authorize URL carries no code_challenge")
	}
	if q.Get("state") == "" || q.Get("nonce") == "" {
		t.Error("authorize URL must carry both state and nonce")
	}
	if n := len(req.CodeVerifier); n < 43 || n > 128 {
		t.Errorf("code_verifier length %d outside the required 43-128 range", n)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("scope %q must include openid", q.Get("scope"))
	}
}

func TestAuthorizeSilentSendsPromptNone(t *testing.T) {
	p, _, _ := newTestProvider(t)

	req, err := p.Authorize(context.Background(), true)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(req.URL)
	if got := u.Query().Get("prompt"); got != "none" {
		t.Errorf("silent authorize prompt = %q, want none", got)
	}
}

func TestVerifyIDTokenHappyPath(t *testing.T) {
	p, stub, _ := newTestProvider(t)

	token, err := stub.IDToken("citizen-001", "the-nonce", map[string]any{"email": "a@b.gov"})
	if err != nil {
		t.Fatalf("mint id_token: %v", err)
	}

	claims, err := p.VerifyIDToken(context.Background(), token, "the-nonce")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "citizen-001" {
		t.Errorf("sub = %q, want citizen-001", claims.Subject)
	}
	if claims.Email != "a@b.gov" {
		t.Errorf("email = %q", claims.Email)
	}
}

func TestMergeUserInfoOverlaysProfile(t *testing.T) {
	p, stub, _ := newTestProvider(t)
	stub.SignIn("citizen-001") // the stub's userinfo answers for the active session

	claims := &Claims{Subject: "citizen-001"} // as if from a verified id_token
	if err := p.MergeUserInfo(context.Background(), "any-access-token", claims); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if claims.Email != "citizen-001@example.gov" {
		t.Errorf("email = %q, want it filled from userinfo", claims.Email)
	}
	if !claims.EmailVerified {
		t.Error("email_verified should be true from userinfo")
	}
}

// TestMergeUserInfoRejectsSubMismatch guards OIDC Core §5.3.2: a userinfo
// response for a different subject than the id_token must not be trusted, or an
// access token swapped in mid-flow could import another user's email.
func TestMergeUserInfoRejectsSubMismatch(t *testing.T) {
	p, stub, _ := newTestProvider(t)
	stub.SignIn("citizen-001") // userinfo will answer sub=citizen-001

	claims := &Claims{Subject: "someone-else"}
	err := p.MergeUserInfo(context.Background(), "any-access-token", claims)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
	if claims.Email != "" {
		t.Errorf("email = %q, want untouched on mismatch", claims.Email)
	}
}

func TestVerifyIDTokenRejectsBadNonce(t *testing.T) {
	p, stub, _ := newTestProvider(t)

	token, _ := stub.IDToken("citizen-001", "real-nonce", nil)
	_, err := p.VerifyIDToken(context.Background(), token, "different-nonce")
	if !errors.Is(err, ErrNonceMismatch) {
		t.Fatalf("err = %v, want ErrNonceMismatch", err)
	}
}

func TestVerifyIDTokenRejectsWrongAudience(t *testing.T) {
	p, stub, _ := newTestProvider(t)

	// A token signed for a different client is the silent 401 that happens
	// when a second OAuth client gets registered under the same application.
	token, _ := stub.IDToken("citizen-001", "n", map[string]any{"aud": "some-other-app"})
	_, err := p.VerifyIDToken(context.Background(), token, "n")
	if !errors.Is(err, ErrAudMismatch) {
		t.Fatalf("err = %v, want ErrAudMismatch", err)
	}
}

func TestVerifyIDTokenRejectsExpired(t *testing.T) {
	p, stub, _ := newTestProvider(t)

	past := time.Now().Add(-2 * time.Hour).Unix()
	token, _ := stub.IDToken("citizen-001", "n", map[string]any{"exp": past, "iat": past})
	_, err := p.VerifyIDToken(context.Background(), token, "n")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestVerifyIDTokenRejectsForeignSignature(t *testing.T) {
	p, _, _ := newTestProvider(t)

	// A token minted by a different C2 instance carries an unknown kid and an
	// unrelated key; it must not verify against our JWKS.
	other, err := c2stub.New(c2stub.Options{ClientID: "cityconnect-test", KeyID: "attacker-key"})
	if err != nil {
		t.Fatal(err)
	}
	other.SetIssuer(p.Issuer())

	token, _ := other.IDToken("citizen-001", "n", nil)
	if _, err := p.VerifyIDToken(context.Background(), token, "n"); err == nil {
		t.Fatal("a foreign-signed token must not verify")
	}
}

func TestVerifyCalloutAssertion(t *testing.T) {
	p, stub, _ := newTestProvider(t)

	token, err := stub.CalloutAssertion("citizen-007", "openid residency")
	if err != nil {
		t.Fatalf("mint assertion: %v", err)
	}

	claims, err := p.VerifyCalloutAssertion(context.Background(), token)
	if err != nil {
		t.Fatalf("verify assertion: %v", err)
	}
	if claims.Subject != "citizen-007" {
		t.Errorf("sub = %q", claims.Subject)
	}
	if !claims.HasScope("residency") {
		t.Errorf("scopes = %v, want residency", claims.Scopes())
	}
	// An absent Trust Adapter claim means unverified, never false.
	if v, ok := claims.StringClaim("residency_status"); ok {
		t.Errorf("residency_status unexpectedly present: %q", v)
	}
}

func TestVerifyLogoutToken(t *testing.T) {
	p, stub, _ := newTestProvider(t)

	token, err := stub.LogoutToken("citizen-001")
	if err != nil {
		t.Fatalf("mint logout token: %v", err)
	}

	claims, err := p.VerifyLogoutToken(context.Background(), token)
	if err != nil {
		t.Fatalf("verify logout token: %v", err)
	}
	if claims.Subject != "citizen-001" {
		t.Errorf("sub = %q", claims.Subject)
	}
	if claims.JTI == "" {
		t.Error("logout token should carry a jti for deduplication")
	}
}

// TestVerifyLogoutTokenRejectsNonce covers the spec rule that a logout_token
// must not contain a nonce. Without this check an ordinary id_token could be
// replayed to the logout endpoint as a denial-of-service against a user.
func TestVerifyLogoutTokenRejectsNonce(t *testing.T) {
	p, stub, _ := newTestProvider(t)

	nowUnix := time.Now().Unix()
	token, err := stub.IDToken("citizen-001", "a-nonce", map[string]any{
		"iat": nowUnix, "exp": nowUnix + 60, "jti": "abc",
		"events": map[string]any{
			"http://schemas.openid.net/event/backchannel-logout": map[string]any{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.VerifyLogoutToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "must not contain a nonce") {
		t.Fatalf("err = %v, want a nonce rejection", err)
	}
}

// TestVerifyLogoutTokenRequiresEvent rejects a token that is otherwise valid
// but does not actually assert a logout.
func TestVerifyLogoutTokenRequiresEvent(t *testing.T) {
	p, stub, _ := newTestProvider(t)

	token, _ := stub.IDToken("citizen-001", "", map[string]any{"jti": "abc"})
	_, err := p.VerifyLogoutToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "back-channel logout event") {
		t.Fatalf("err = %v, want a missing-event rejection", err)
	}
}

func TestEndSessionURLCarriesHintAndRedirect(t *testing.T) {
	p, _, _ := newTestProvider(t)

	got, err := p.EndSessionURL(context.Background(), "the-id-token")
	if err != nil {
		t.Fatalf("end session url: %v", err)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	if q.Get("id_token_hint") != "the-id-token" {
		t.Errorf("id_token_hint = %q", q.Get("id_token_hint"))
	}
	if want := "http://localhost:4021/cityconnect/"; q.Get("post_logout_redirect_uri") != want {
		t.Errorf("post_logout_redirect_uri = %q, want the console's %q",
			q.Get("post_logout_redirect_uri"), want)
	}
}

// A citizen signing out of the portal must return to the portal. C2 matches
// post_logout_redirect_uri exactly, so sending the console's value from the
// portal is rejected outright — the citizen sees a C2 error page rather than a
// signed-out portal, and their session on our side is already gone.
func TestEndSessionURLForUsesTheGivenReturnAddress(t *testing.T) {
	p, _, _ := newTestProvider(t)

	const portal = "https://services.example.gov/"
	got, err := p.EndSessionURLFor(context.Background(), "the-id-token", portal)
	if err != nil {
		t.Fatalf("end session url: %v", err)
	}
	q, _ := url.Parse(got)
	if q.Query().Get("post_logout_redirect_uri") != portal {
		t.Errorf("post_logout_redirect_uri = %q, want %q",
			q.Query().Get("post_logout_redirect_uri"), portal)
	}
	if q.Query().Get("id_token_hint") != "the-id-token" {
		t.Error("id_token_hint was dropped")
	}
}

func TestCheckSucceedsAgainstStub(t *testing.T) {
	p, _, _ := newTestProvider(t)
	if err := p.Check(context.Background()); err != nil {
		t.Fatalf("boot check failed: %v", err)
	}
}

func TestFullAuthorizationCodeFlow(t *testing.T) {
	p, stub, _ := newTestProvider(t)
	ctx := context.Background()

	// An active C2 session means the authorize request completes silently —
	// that is what SSO looks like when prompt is absent.
	stub.SignIn("staff-42")

	authReq, err := p.Authorize(ctx, false)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}

	// Stop at the redirect so the code and state can be read off Location.
	resp, err := noRedirectGet(authReq.URL)
	if err != nil {
		t.Fatalf("authorize request: %v", err)
	}
	loc, err := url.Parse(resp)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect %q (error=%q)", resp, loc.Query().Get("error"))
	}
	if loc.Query().Get("state") != authReq.State {
		t.Fatalf("state mismatch: got %q want %q", loc.Query().Get("state"), authReq.State)
	}

	tokens, err := p.Exchange(ctx, code, authReq.CodeVerifier)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	claims, err := p.VerifyIDToken(ctx, tokens.IDToken, authReq.Nonce)
	if err != nil {
		t.Fatalf("verify id_token: %v", err)
	}
	if claims.Subject != "staff-42" {
		t.Errorf("sub = %q, want staff-42", claims.Subject)
	}
}

// TestExchangeRejectsWrongVerifier proves PKCE is actually enforced end to
// end: a stolen authorization code is useless without the verifier.
func TestExchangeRejectsWrongVerifier(t *testing.T) {
	p, stub, _ := newTestProvider(t)
	ctx := context.Background()
	stub.SignIn("staff-42")

	authReq, _ := p.Authorize(ctx, false)
	loc, err := noRedirectGet(authReq.URL)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loc)

	_, err = p.Exchange(ctx, u.Query().Get("code"), "a-different-verifier-that-is-long-enough-x")
	if err == nil {
		t.Fatal("exchange must fail when the PKCE verifier does not match")
	}
}
