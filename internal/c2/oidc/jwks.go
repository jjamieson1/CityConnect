package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// keySet caches C2's signing keys. The same keys verify id_tokens, Service
// Card callout assertions and back-channel logout tokens — there is one JWKS,
// discovered from the metadata document, never a hardcoded path.
type keySet struct {
	discovery  *discoveryCache
	client     *http.Client
	minRefresh time.Duration

	mu          sync.RWMutex
	keys        *jose.JSONWebKeySet
	lastRefresh time.Time
	inflight    sync.Mutex
}

// keyFor returns the verification key matching kid, refreshing the cache once
// on an unknown kid. Keys rotate, and a rotation must not require a restart —
// but an attacker-supplied kid must not be able to drive unbounded refetches
// either, which is what minRefresh bounds.
func (k *keySet) keyFor(ctx context.Context, kid string) (*jose.JSONWebKey, error) {
	if key := k.lookup(kid); key != nil {
		return key, nil
	}

	k.inflight.Lock()
	defer k.inflight.Unlock()

	if key := k.lookup(kid); key != nil {
		return key, nil
	}

	k.mu.RLock()
	tooSoon := !k.lastRefresh.IsZero() && time.Since(k.lastRefresh) < k.minRefresh
	k.mu.RUnlock()
	if tooSoon {
		return nil, fmt.Errorf("oidc: no key for kid %q (refreshed %s ago)", kid, time.Since(k.lastRefresh).Round(time.Second))
	}

	if err := k.refresh(ctx); err != nil {
		return nil, err
	}
	if key := k.lookup(kid); key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("oidc: no key for kid %q in JWKS", kid)
}

func (k *keySet) lookup(kid string) *jose.JSONWebKey {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.keys == nil {
		return nil
	}
	// An empty kid is tolerated only when the set holds exactly one key.
	if kid == "" {
		if len(k.keys.Keys) == 1 {
			return &k.keys.Keys[0]
		}
		return nil
	}
	if matches := k.keys.Key(kid); len(matches) > 0 {
		return &matches[0]
	}
	return nil
}

func (k *keySet) refresh(ctx context.Context) error {
	doc, err := k.discovery.get(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.JWKSURI, nil)
	if err != nil {
		return fmt.Errorf("oidc: build JWKS request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("oidc: fetch JWKS from %s: %w", doc.JWKSURI, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc: JWKS %s returned %d", doc.JWKSURI, resp.StatusCode)
	}

	var set jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return fmt.Errorf("oidc: decode JWKS: %w", err)
	}
	if len(set.Keys) == 0 {
		return fmt.Errorf("oidc: JWKS at %s contains no keys", doc.JWKSURI)
	}

	k.mu.Lock()
	k.keys, k.lastRefresh = &set, time.Now()
	k.mu.Unlock()
	return nil
}
