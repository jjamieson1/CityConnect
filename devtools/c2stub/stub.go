// Package c2stub is a minimal stand-in for C2 (TrustIdentity): OIDC discovery,
// a JWKS, an authorization endpoint, a token endpoint, back-channel logout
// fan-out, and the partner notification sink.
//
// It exists because every interesting part of CityConnect sits behind a C2
// trust relationship. Without a stub, none of the login flow, the Service Card
// callout, or the notification outbox can be exercised in CI or developed
// offline — and with staff SSO as the only login path, nothing at all is
// demoable. It deliberately reproduces C2's documented quirks: the token
// endpoint is mounted at /oidc/oauth/token rather than /oidc/token, and the
// issuer is the portal origin.
package c2stub

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Options configures a stub instance.
type Options struct {
	// Issuer is the externally-visible issuer, e.g. "http://localhost:5173/oidc".
	Issuer string
	// ClientID is the one registered client. Tokens are signed with this as
	// `aud`; anything else is rejected, mirroring C2.
	ClientID     string
	ClientSecret string
	// KeyID names the signing key in the JWKS.
	KeyID string
	// DenyConsent makes the partner notification endpoint return 403, to
	// exercise the consent-denied path without hand-crafting a response.
	DenyConsent bool
}

// Notification is one message the stub accepted.
type Notification struct {
	Sub       string    `json:"sub"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	ShortBody string    `json:"shortBody"`
	Category  string    `json:"category"`
	ID        string    `json:"notificationId"`
	Channels  []string  `json:"channels"`
	At        time.Time `json:"at"`
	ClientID  string    `json:"clientId"`
}

// Server is a running stub.
type Server struct {
	opts    Options
	key     *rsa.PrivateKey
	signer  jose.Signer
	mux     *http.ServeMux
	counter int

	mu sync.Mutex
	// codes maps an issued authorization code to the pending login.
	codes map[string]*pendingAuth
	// sessions records which subjects currently hold a stub session, so
	// prompt=none can answer truthfully.
	sessions map[string]bool
	// profiles remembers the identity asserted for a subject, so UserInfo can
	// return it after the authorization code (which carried the profile) is
	// spent. This is what lets the stub mirror C2: profile/email claims live at
	// UserInfo, not in the id_token.
	profiles map[string]Profile
	// notifications is everything the partner endpoint accepted.
	notifications []Notification
	// consent gates the partner endpoint per subject; absent means consented.
	consent map[string]bool
	// backchannelURL is where explicit logouts are fanned out.
	backchannelURL string
}

type pendingAuth struct {
	sub           string
	nonce         string
	codeChallenge string
	redirectURI   string
	profile       Profile
	expires       time.Time
}

// Profile is the identity the stub asserts for a subject.
type Profile struct {
	Sub        string `json:"sub"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

// New builds a stub with a freshly generated signing key.
func New(opts Options) (*Server, error) {
	if opts.KeyID == "" {
		opts.KeyID = "c2stub-key-1"
	}
	if opts.ClientID == "" {
		opts.ClientID = "cityconnect-dev"
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("c2stub: generate key: %w", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", opts.KeyID),
	)
	if err != nil {
		return nil, fmt.Errorf("c2stub: build signer: %w", err)
	}

	s := &Server{
		opts:     opts,
		key:      key,
		signer:   signer,
		mux:      http.NewServeMux(),
		codes:    map[string]*pendingAuth{},
		sessions: map[string]bool{},
		profiles: map[string]Profile{},
		consent:  map[string]bool{},
	}
	s.routes()
	return s, nil
}

// Handler exposes the stub's routes.
func (s *Server) Handler() http.Handler { return s.mux }

// SetIssuer updates the issuer once the listening address is known, which is
// how httptest-based tests get a correct discovery document.
func (s *Server) SetIssuer(issuer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.Issuer = strings.TrimSuffix(issuer, "/")
}

// SetBackchannelURL registers the relying party's back-channel logout endpoint.
func (s *Server) SetBackchannelURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backchannelURL = u
}

// SetConsent sets whether a subject currently consents to our application.
// Revoking consent is what makes the partner endpoint answer 403.
func (s *Server) SetConsent(sub string, granted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consent[sub] = granted
}

// SignIn gives a subject an active stub session, so a subsequent authorize
// request completes silently the way real SSO does. A default profile is
// recorded so UserInfo has something to return for a subject signed in
// programmatically (rather than through the login form).
func (s *Server) SignIn(sub string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sub] = true
	if _, ok := s.profiles[sub]; !ok {
		s.profiles[sub] = defaultProfile(sub)
	}
}

// SignOutAll clears every stub session.
//
// The authorize endpoint completes silently for whichever subject holds a
// session, and with more than one it picks arbitrarily by map iteration. A
// test that signs in a citizen and then a staff member would otherwise
// authenticate as whichever Go happened to yield — an intermittent failure
// that looks like a bug in the application rather than in the stub.
func (s *Server) SignOutAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]bool{}
}

// defaultProfile is the identity the stub asserts for a subject that arrived
// without one (silent SSO or a programmatic SignIn), matching the shape C2
// would release under the profile/email scopes.
func defaultProfile(sub string) Profile {
	name := sub
	if len(sub) > 0 {
		name = strings.ToUpper(sub[:1]) + sub[1:]
	}
	return Profile{Sub: sub, Email: sub + "@example.gov", Name: name}
}

// profileFor returns the stored profile for a subject, or a synthesized default.
// Callers must hold s.mu.
func (s *Server) profileFor(sub string) Profile {
	if p, ok := s.profiles[sub]; ok {
		if p.Email == "" {
			p.Email = sub + "@example.gov"
		}
		return p
	}
	return defaultProfile(sub)
}

// Notifications returns everything the partner endpoint has accepted.
func (s *Server) Notifications() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Notification, len(s.notifications))
	copy(out, s.notifications)
	return out
}

// PublicJWKS returns the stub's verification key set.
func (s *Server) PublicJWKS() jose.JSONWebKeySet {
	return jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       s.key.Public(),
		KeyID:     s.opts.KeyID,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}}
}

func (s *Server) issuer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.Issuer
}

// sign mints a compact JWS over the given claims.
func (s *Server) sign(claims map[string]any) (string, error) {
	b, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	jws, err := s.signer.Sign(b)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func redirectErr(w http.ResponseWriter, r *http.Request, redirectURI, state, code, desc string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, code+": "+desc, http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", code)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
