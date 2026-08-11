package oidc

import (
	"fmt"
	"net/http"
)

// noRedirectGet issues a GET that stops at the first redirect and returns the
// Location header, so tests can inspect the authorization response rather than
// following it into the relying party.
func noRedirectGet(rawURL string) (string, error) {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("expected a redirect, got status %d", resp.StatusCode)
	}
	return loc, nil
}
