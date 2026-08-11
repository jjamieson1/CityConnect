// Package oidc implements CityConnect's side of the C2 (TrustIdentity) trust
// relationship: OIDC discovery, JWKS handling, the authorization-code login
// flow, and verification of every token C2 sends us — id_tokens, Service Card
// callout assertions, and back-channel logout tokens.
//
// Two rules run through the whole package, both learned expensively:
//
//   - Every endpoint comes from the discovery document. Nothing is hardcoded.
//     In this deployment the token endpoint is /oidc/oauth/token, not the
//     /oidc/token everyone assumes.
//   - All traffic goes through the portal origin. C2's internal API host is
//     never referenced, and the issuer is the portal origin even when
//     discovery is served from somewhere else.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Discovery is the subset of the OIDC provider metadata we rely on.
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserInfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	EndSessionEndpoint    string   `json:"end_session_endpoint"`
	IntrospectionEndpoint string   `json:"introspection_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	ResponseTypes         []string `json:"response_types_supported"`
}

// Validate checks that the discovery document is usable and that its issuer is
// the one we expect. A mismatch here is the failure that looks like discovery
// "worked" and then makes every token fail validation.
func (d *Discovery) Validate(expectedIssuer string) error {
	switch {
	case d.Issuer == "":
		return fmt.Errorf("oidc: discovery document has no issuer")
	case expectedIssuer != "" && d.Issuer != expectedIssuer:
		return fmt.Errorf("oidc: discovery issuer %q does not match configured issuer %q "+
			"(the issuer is the portal origin, not C2's internal API host)", d.Issuer, expectedIssuer)
	case d.AuthorizationEndpoint == "":
		return fmt.Errorf("oidc: discovery document has no authorization_endpoint")
	case d.TokenEndpoint == "":
		return fmt.Errorf("oidc: discovery document has no token_endpoint")
	case d.JWKSURI == "":
		return fmt.Errorf("oidc: discovery document has no jwks_uri")
	}
	return nil
}

// discoveryCache fetches and caches the provider metadata.
type discoveryCache struct {
	url      string
	issuer   string
	client   *http.Client
	ttl      time.Duration
	mu       sync.RWMutex
	doc      *Discovery
	fetched  time.Time
	inflight sync.Mutex
}

func (c *discoveryCache) get(ctx context.Context) (*Discovery, error) {
	c.mu.RLock()
	if c.doc != nil && time.Since(c.fetched) < c.ttl {
		doc := c.doc
		c.mu.RUnlock()
		return doc, nil
	}
	c.mu.RUnlock()

	// Serialise refreshes so a cold cache under load produces one fetch, not
	// one per in-flight request.
	c.inflight.Lock()
	defer c.inflight.Unlock()

	c.mu.RLock()
	if c.doc != nil && time.Since(c.fetched) < c.ttl {
		doc := c.doc
		c.mu.RUnlock()
		return doc, nil
	}
	stale := c.doc
	c.mu.RUnlock()

	doc, err := c.fetch(ctx)
	if err != nil {
		// Serving a stale document beats failing every login because C2's
		// metadata endpoint blipped. Endpoints change rarely; outages do not.
		if stale != nil {
			return stale, nil
		}
		return nil, err
	}

	c.mu.Lock()
	c.doc, c.fetched = doc, time.Now()
	c.mu.Unlock()
	return doc, nil
}

func (c *discoveryCache) fetch(ctx context.Context) (*Discovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("oidc: build discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: fetch discovery from %s: %w", c.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: discovery %s returned %d", c.url, resp.StatusCode)
	}

	var doc Discovery
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("oidc: decode discovery: %w", err)
	}
	if err := doc.Validate(c.issuer); err != nil {
		return nil, err
	}
	return &doc, nil
}
