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

// The two-origin split — a staff console on one host, a citizen portal on
// another, each with its own cookie jar — is enforced by Apache deciding what
// each origin may reach. That only holds if the API itself is not directly
// reachable, so production binds loopback unless told otherwise.
//
// Development binds every interface, because opening the console from a phone
// on the same network is how you check a form on a real device.
func TestProdBindsLoopbackByDefault(t *testing.T) {
	base := func(t *testing.T) {
		t.Setenv("CC_ENV", "prod")
		t.Setenv("CC_C2_PORTAL_ORIGIN", "https://portal.example.gov/c2")
		t.Setenv("CC_C2_CLIENT_ID", "id")
		t.Setenv("CC_C2_CLIENT_SECRET", "secret")
		t.Setenv("CC_PUBLIC_URL", "https://city.example.gov")
		t.Setenv("CC_PORTAL_PUBLIC_URL", "https://services.example.gov")
		t.Setenv("CC_DB_AUTOMIGRATE", "false")
		t.Setenv("CC_COOKIE_SECURE", "true")
	}

	t.Run("default", func(t *testing.T) {
		base(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Addr != "127.0.0.1:4021" {
			t.Errorf("Addr = %q, want 127.0.0.1:4021", cfg.Addr)
		}
		if cfg.PubliclyBound() {
			t.Error("PubliclyBound() = true for a loopback address")
		}
	})

	// A container or a deployment behind an external load balancer must bind
	// outward, so an explicit setting is honoured — and reported, not refused.
	t.Run("explicit override is honoured", func(t *testing.T) {
		base(t)
		t.Setenv("CC_ADDR", "0.0.0.0:4021")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Addr != "0.0.0.0:4021" {
			t.Errorf("Addr = %q, want the explicit value", cfg.Addr)
		}
		if !cfg.PubliclyBound() {
			t.Error("PubliclyBound() = false for 0.0.0.0")
		}
	})

	t.Run("dev binds all interfaces", func(t *testing.T) {
		t.Setenv("CC_C2_PORTAL_ORIGIN", "http://localhost:5173")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Addr != ":4021" {
			t.Errorf("Addr = %q, want :4021 in dev", cfg.Addr)
		}
	})
}

func TestPubliclyBound(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:4021": false,
		"[::1]:4021":     false,
		"localhost:4021": false,
		"0.0.0.0:4021":   true,
		"[::]:4021":      true,
		":4021":          true,
		"10.0.0.5:4021":  true,
		"garbage":        true, // unparseable: assume the worse case and say so
	} {
		c := &Config{Addr: addr}
		if got := c.PubliclyBound(); got != want {
			t.Errorf("PubliclyBound(%q) = %v, want %v", addr, got, want)
		}
	}
}
