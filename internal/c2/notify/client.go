// Package notify is the client for C2's partner notification API: the one way
// CityConnect reaches a citizen's inbox, email or SMS.
//
// Two properties of that API shape everything here. It is consent-gated by C2,
// so a refusal is a permanent state rather than a transient error. And it
// takes one recipient per call with a per-IP rate limit, so sending is paced
// through an outbox rather than fanned out.
package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/jjamieson1/CityConnect/internal/config"
)

// Outcome classifies a send attempt. The distinction between "retry later"
// and "never retry" is the whole point: retrying a consent refusal burns the
// rate limit forever and never succeeds.
type Outcome string

// Send outcomes.
const (
	OutcomeSent      Outcome = "sent"       // 202
	OutcomeNoConsent Outcome = "no_consent" // 403 — permanent until the citizen re-consents
	OutcomeUnknown   Outcome = "unknown_sub"// 404 — permanent; the identity link is stale
	OutcomeRejected  Outcome = "rejected"   // 400/401 — our fault, permanent until fixed
	OutcomeRetry     Outcome = "retry"      // 429/5xx/network
)

// Permanent reports whether an outcome should stop the outbox retrying.
func (o Outcome) Permanent() bool {
	return o == OutcomeNoConsent || o == OutcomeUnknown || o == OutcomeRejected
}

// Request is one citizen notification.
type Request struct {
	Sub       string `json:"sub"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	ShortBody string `json:"shortBody,omitempty"`
	Category  string `json:"category,omitempty"`
}

// Response is what C2 returns on acceptance.
type Response struct {
	NotificationID string   `json:"notificationId"`
	Channels       []string `json:"channels"`
}

// Result carries the full outcome of a send.
type Result struct {
	Outcome    Outcome
	StatusCode int
	Response   *Response
	RetryAfter time.Duration
	Err        error
}

// Client talks to C2's partner notification endpoint.
type Client struct {
	cfg    config.C2Config
	http   *http.Client
	signer jose.Signer
}

// New builds a notification client. When a client private key is configured it
// authenticates with a private-key JWT assertion, which keeps a shared secret
// off the wire; otherwise it falls back to HTTP Basic.
func New(cfg config.C2Config) (*Client, error) {
	c := &Client{cfg: cfg, http: &http.Client{Timeout: cfg.HTTPTimeout}}

	if cfg.ClientPrivateKeyPEM == "" {
		if cfg.ClientSecret == "" {
			return nil, errors.New("notify: neither a client private key nor a client secret is configured")
		}
		return c, nil
	}

	key, err := parsePrivateKey(cfg.ClientPrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", cfg.ClientKeyID),
	)
	if err != nil {
		return nil, fmt.Errorf("notify: build assertion signer: %w", err)
	}
	c.signer = signer
	return c, nil
}

// UsesPrivateKey reports which authentication mode is active, for the readiness
// report and the admin console.
func (c *Client) UsesPrivateKey() bool { return c.signer != nil }

// Send delivers one notification.
func (c *Client) Send(ctx context.Context, in Request) Result {
	body, err := json.Marshal(in)
	if err != nil {
		return Result{Outcome: OutcomeRejected, Err: fmt.Errorf("notify: encode request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.PartnerNotificationsURL(), bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: OutcomeRetry, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if err := c.authenticate(req); err != nil {
		return Result{Outcome: OutcomeRejected, Err: err}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{Outcome: OutcomeRetry, Err: fmt.Errorf("notify: send: %w", err)}
	}
	defer resp.Body.Close()

	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch resp.StatusCode {
	case http.StatusAccepted:
		var out Response
		if err := json.Unmarshal(payload, &out); err != nil {
			// C2 accepted it; a body we cannot parse does not undo that.
			return Result{Outcome: OutcomeSent, StatusCode: resp.StatusCode}
		}
		return Result{Outcome: OutcomeSent, StatusCode: resp.StatusCode, Response: &out}

	case http.StatusForbidden:
		// The citizen holds no active consent for our application. Expected,
		// not an error — and never worth retrying until they re-consent.
		return Result{Outcome: OutcomeNoConsent, StatusCode: resp.StatusCode}

	case http.StatusNotFound:
		// C2 does not know this subject: our identity link is stale.
		return Result{Outcome: OutcomeUnknown, StatusCode: resp.StatusCode}

	case http.StatusTooManyRequests:
		return Result{
			Outcome: OutcomeRetry, StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			Err:        errors.New("notify: rate limited by C2"),
		}

	case http.StatusBadRequest, http.StatusUnauthorized:
		// Our fault: a malformed payload or bad credentials. Retrying an
		// unchanged request cannot fix either.
		return Result{
			Outcome: OutcomeRejected, StatusCode: resp.StatusCode,
			Err: fmt.Errorf("notify: C2 rejected the request: %s", truncate(string(payload), 300)),
		}
	}

	return Result{
		Outcome: OutcomeRetry, StatusCode: resp.StatusCode,
		Err: fmt.Errorf("notify: C2 returned %d: %s", resp.StatusCode, truncate(string(payload), 300)),
	}
}

// authenticate attaches either a client assertion or HTTP Basic credentials.
func (c *Client) authenticate(req *http.Request) error {
	if c.signer == nil {
		req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)
		return nil
	}

	assertion, err := c.clientAssertion()
	if err != nil {
		return err
	}
	req.Header.Set("X-Client-Assertion", assertion)
	return nil
}

// clientAssertion mints the short-lived private-key JWT C2 verifies against
// our published JWKS. `iss` and `sub` are both our client id, and `aud` is the
// deployment issuer, which is what stops an assertion being replayed against
// another audience.
func (c *Client) clientAssertion() (string, error) {
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("notify: generate jti: %w", err)
	}

	now := time.Now()
	claims := map[string]any{
		"iss": c.cfg.ClientID,
		"sub": c.cfg.ClientID,
		"aud": c.cfg.NotifyAudience,
		"iat": now.Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
		"jti": base64.RawURLEncoding.EncodeToString(jti),
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("notify: encode assertion: %w", err)
	}
	jws, err := c.signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("notify: sign assertion: %w", err)
	}
	return jws.CompactSerialize()
}

// PublicJWKS renders our verification key set, which C2 fetches to verify the
// client assertions above. Serving it ourselves means there is no key material
// to exchange out of band beyond the URL.
func (c *Client) PublicJWKS() (*jose.JSONWebKeySet, error) {
	if c.cfg.ClientPrivateKeyPEM == "" {
		return &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{}}, nil
	}
	key, err := parsePrivateKey(c.cfg.ClientPrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       key.Public(),
		KeyID:     c.cfg.ClientKeyID,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}}, nil
}

func parsePrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("notify: client private key is not valid PEM")
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("notify: client private key is not RSA")
		}
		return rsaKey, nil
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("notify: parse client private key: %w", err)
	}
	return key, nil
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
