package agents

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// noRedirectGet issues a GET that stops at the first redirect.
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

// seedUser inserts an already-bound staff user.
func (f *fixture) seedUser(t *testing.T, sub, email string, role domain.Role) *domain.User {
	t.Helper()
	u := &domain.User{
		C2Sub: sub, Email: email, Name: email,
		Status: domain.UserActive, Role: role,
	}
	if err := f.db.Create(u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// loginAs completes a login for an arbitrary subject and email, asserting the
// email as C2-verified. It exists because the stub's silent-SSO path derives
// the email from the subject, which is not enough to exercise invite-by-email
// binding.
func (f *fixture) loginAs(t *testing.T, sub, email, name string) (*LoginResult, error) {
	t.Helper()
	return f.loginClaims(t, sub, map[string]any{
		"email": email, "name": name, "email_verified": true,
	})
}

// loginClaims completes a login for a subject with an explicit claim set,
// minting the id_token directly (bypassing the redirect dance) so a test can
// drive resolveStaffUser with, e.g., an unverified email.
func (f *fixture) loginClaims(t *testing.T, sub string, extra map[string]any) (*LoginResult, error) {
	t.Helper()
	ctx := context.Background()

	if _, err := f.svc.StartLogin(ctx, "", false); err != nil {
		t.Fatalf("start login: %v", err)
	}

	// Read back the flow the service just persisted so the minted token
	// carries the nonce it will verify against.
	var flow domain.LoginFlow
	if err := f.db.Order("created_at DESC").First(&flow).Error; err != nil {
		t.Fatalf("read login flow: %v", err)
	}

	idToken, err := f.stub.IDToken(sub, flow.Nonce, extra)
	if err != nil {
		t.Fatalf("mint id_token: %v", err)
	}

	claims, err := f.svc.oidc.VerifyIDToken(ctx, idToken, flow.Nonce)
	if err != nil {
		t.Fatalf("verify minted token: %v", err)
	}
	f.db.Delete(&flow)

	user, err := f.svc.resolveStaffUser(ctx, claims)
	if err != nil {
		return nil, err
	}
	token, session, err := f.svc.openSession(ctx, user, idToken, "test", "127.0.0.1")
	if err != nil {
		return nil, err
	}
	return &LoginResult{SessionToken: token, ExpiresAt: session.ExpiresAt, User: user}, nil
}
