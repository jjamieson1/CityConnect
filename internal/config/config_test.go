package config

import (
	"strings"
	"testing"
)

// TestEmptyBasePathDeploysAtRoot covers a setting where "" is a real choice.
// Treating it as "unset" silently rewrote the redirect URI to include
// /cityconnect, and because C2 matches redirect URIs exactly, sign-in failed
// with an error that pointed at C2 rather than at the configuration.
func TestEmptyBasePathDeploysAtRoot(t *testing.T) {
	t.Setenv("CC_BASE_PATH", "")
	t.Setenv("CC_PUBLIC_URL", "http://localhost:4021")
	t.Setenv("CC_C2_PORTAL_ORIGIN", "http://localhost:5173")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.BasePath != "" {
		t.Errorf("BasePath = %q, want empty", cfg.BasePath)
	}
	if want := "http://localhost:4021/api/auth/callback"; cfg.C2.RedirectURL != want {
		t.Errorf("RedirectURL = %q, want %q", cfg.C2.RedirectURL, want)
	}
}

func TestUnsetBasePathUsesDefault(t *testing.T) {
	t.Setenv("CC_PUBLIC_URL", "http://localhost:4021")
	t.Setenv("CC_C2_PORTAL_ORIGIN", "http://localhost:5173")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BasePath != "/cityconnect" {
		t.Errorf("BasePath = %q, want /cityconnect", cfg.BasePath)
	}
}

// TestIssuerDefaultsToPortalOrigin pins the rule that causes the most
// expensive misconfiguration: the issuer is the portal origin, never C2's
// internal API host.
func TestIssuerDefaultsToPortalOrigin(t *testing.T) {
	t.Setenv("CC_C2_PORTAL_ORIGIN", "https://portal.example.gov/c2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if want := "https://portal.example.gov/c2/oidc"; cfg.C2.Issuer != want {
		t.Errorf("Issuer = %q, want %q", cfg.C2.Issuer, want)
	}
}

func TestRejectsInternalApiHostAsIssuer(t *testing.T) {
	t.Setenv("CC_C2_PORTAL_ORIGIN", "http://localhost:5173")
	t.Setenv("CC_C2_ISSUER", "http://localhost:8088/oidc")

	_, err := Load()
	if err == nil {
		t.Fatal("expected the internal API port to be rejected as an issuer")
	}
	if !strings.Contains(err.Error(), "internal API port") {
		t.Errorf("error did not explain the problem: %v", err)
	}
}

// TestProdRequiresSafeDefaults checks the guards that stop a development
// configuration reaching production.
func TestProdRequiresSafeDefaults(t *testing.T) {
	t.Setenv("CC_ENV", "prod")
	t.Setenv("CC_C2_PORTAL_ORIGIN", "https://portal.example.gov/c2")
	t.Setenv("CC_DB_AUTOMIGRATE", "true")
	t.Setenv("CC_COOKIE_SECURE", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("expected production validation to fail")
	}
	for _, want := range []string{"CC_C2_CLIENT_ID", "CC_COOKIE_SECURE", "CC_DB_AUTOMIGRATE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error did not mention %s: %v", want, err)
		}
	}
}
