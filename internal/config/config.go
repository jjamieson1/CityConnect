// Package config loads and validates all runtime configuration from the
// environment. Everything is resolved and validated once, at boot, so a
// misconfigured deployment fails immediately rather than on first request.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully-resolved application configuration.
type Config struct {
	Env             string // dev | staging | prod
	Addr            string // listen address, e.g. ":4021"
	BasePath        string // public base path, e.g. "/cityconnect"
	PublicURL       string // externally reachable base URL of the SPA
	ShutdownTimeout time.Duration

	DB  DBConfig
	C2  C2Config
	Sec SecurityConfig
	Job JobConfig

	AttachmentDir   string
	AttachmentMaxMB int64

	// BootstrapAdminSubs are C2 subject identifiers granted the admin role at
	// boot. With C2 SSO as the sole staff login there is no local account to
	// sign in with, and the UI cannot grant a role to a user that does not
	// exist yet, so a first admin has to come from configuration.
	BootstrapAdminSubs []string

	// BootstrapAdminEmails create admin *invitations* at boot, which bind to
	// a C2 subject on first sign-in.
	//
	// This exists because a subject identifier is opaque and nobody knows
	// theirs before they have signed in once — but sign-in is exactly what is
	// blocked. An email address is the one identifier an operator has in
	// advance. It is no weaker than the invite flow an administrator would use
	// from the console, and it still never auto-provisions an unknown identity.
	BootstrapAdminEmails []string
}

// DBConfig describes the MariaDB connection.
type DBConfig struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	AutoMigrate     bool
}

// C2Config holds everything needed to talk to, and be called by, C2
// (TrustIdentity).
type C2Config struct {
	// PortalOrigin is the single public host that serves the citizen portal and
	// fronts C2's API. Every C2 interaction goes through it. The internal API
	// host/port is never referenced.
	PortalOrigin string
	// Issuer is the exact `iss` string every C2 token carries. It is the portal
	// origin plus the OIDC mount path, and it is what discovery reports even
	// when discovery itself is fetched from elsewhere.
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// PostLogoutRedirectURL is where C2 sends the browser after RP-initiated
	// logout. Must be pre-registered; C2 matches redirect URIs exactly.
	PostLogoutRedirectURL string
	Scopes                []string

	// PartnerBaseURL is the base for the partner notification API. It is
	// mounted at <base>/partner, a sibling of the OIDC endpoints and NOT under
	// /api (that surface is for citizen sessions).
	PartnerBaseURL string
	// NotifyAudience is the `aud` of our client assertions: this deployment's
	// issuer as the C2 administrator states it, which is not necessarily the
	// same string as the OIDC Issuer above.
	NotifyAudience string
	// ClientPrivateKeyPEM / ClientKeyID sign the private-key JWT client
	// assertion. When empty we fall back to HTTP Basic with ClientSecret.
	ClientPrivateKeyPEM string
	ClientKeyID         string

	// CalloutAppKey / CalloutAppSecret enable the legacy app_key auth mode on
	// the inbound Service Card callout. Signed JWT is used whenever the
	// Authorization header is present; these are only consulted otherwise.
	CalloutAppKey       string
	CalloutAppSecret    string
	CalloutAllowAppKey  bool
	CalloutCacheTTL     time.Duration
	CalloutMaxTasks     int
	DiscoveryCacheTTL   time.Duration
	JWKSMinRefreshEvery time.Duration
	HTTPTimeout         time.Duration
	ClockSkew           time.Duration
}

// SecurityConfig covers session handling and request-level protections.
type SecurityConfig struct {
	SessionCookieName string
	SessionTTL        time.Duration
	SessionIdleTTL    time.Duration
	CookieSecure      bool
	CookieDomain      string
	CORSOrigins       []string
	RateLimitPerMin   int
	TrustProxyHeaders bool
}

// JobConfig tunes the background scheduler.
type JobConfig struct {
	Enabled           bool
	OutboxInterval    time.Duration
	WebhookInterval   time.Duration
	SLAInterval       time.Duration
	RollupInterval    time.Duration
	RetentionInterval time.Duration
	AutoCloseAfter    time.Duration
	OutboxMaxAttempts int
}

// Load reads configuration from the environment, applies defaults and
// validates the result.
func Load() (*Config, error) {
	c := &Config{
		Env:  env("CC_ENV", "dev"),
		Addr: env("CC_ADDR", ":4021"),
		// An explicitly empty base path is meaningful — it deploys at the
		// root — so it must not fall through to the default the way a blank
		// value does everywhere else.
		BasePath:        strings.TrimSuffix(envAllowEmpty("CC_BASE_PATH", "/cityconnect"), "/"),
		PublicURL:       strings.TrimSuffix(env("CC_PUBLIC_URL", "http://localhost:4021"), "/"),
		ShutdownTimeout: envDuration("CC_SHUTDOWN_TIMEOUT", 20*time.Second),

		AttachmentDir:      env("CC_ATTACHMENT_DIR", "./data/attachments"),
		AttachmentMaxMB:    int64(envInt("CC_ATTACHMENT_MAX_MB", 25)),
		BootstrapAdminSubs:   envList("CC_BOOTSTRAP_ADMIN_SUBS"),
		BootstrapAdminEmails: envList("CC_BOOTSTRAP_ADMIN_EMAILS"),

		DB: DBConfig{
			DSN:             env("CC_DB_DSN", "cityconnect:cityconnect@tcp(127.0.0.1:3306)/cityconnect?charset=utf8mb4&parseTime=True&loc=Local"),
			MaxOpenConns:    envInt("CC_DB_MAX_OPEN", 25),
			MaxIdleConns:    envInt("CC_DB_MAX_IDLE", 5),
			ConnMaxLifetime: envDuration("CC_DB_CONN_MAX_LIFETIME", time.Hour),
			AutoMigrate:     envBool("CC_DB_AUTOMIGRATE", true),
		},

		C2: C2Config{
			PortalOrigin:          strings.TrimSuffix(env("CC_C2_PORTAL_ORIGIN", "http://localhost:5173"), "/"),
			Issuer:                strings.TrimSuffix(env("CC_C2_ISSUER", ""), "/"),
			ClientID:              env("CC_C2_CLIENT_ID", ""),
			ClientSecret:          env("CC_C2_CLIENT_SECRET", ""),
			RedirectURL:           env("CC_C2_REDIRECT_URL", ""),
			PostLogoutRedirectURL: env("CC_C2_POST_LOGOUT_REDIRECT_URL", ""),
			Scopes:                envListOr("CC_C2_SCOPES", []string{"openid", "profile", "email"}),

			PartnerBaseURL:      strings.TrimSuffix(env("CC_C2_PARTNER_BASE_URL", ""), "/"),
			NotifyAudience:      env("CC_C2_NOTIFY_AUDIENCE", ""),
			ClientPrivateKeyPEM: envFileOrValue("CC_C2_CLIENT_PRIVATE_KEY_PEM", "CC_C2_CLIENT_PRIVATE_KEY_FILE"),
			ClientKeyID:         env("CC_C2_CLIENT_KID", ""),

			CalloutAppKey:       env("CC_C2_CALLOUT_APP_KEY", ""),
			CalloutAppSecret:    env("CC_C2_CALLOUT_APP_SECRET", ""),
			CalloutAllowAppKey:  envBool("CC_C2_CALLOUT_ALLOW_APP_KEY", false),
			CalloutCacheTTL:     envDuration("CC_C2_CALLOUT_CACHE_TTL", 20*time.Second),
			CalloutMaxTasks:     envInt("CC_C2_CALLOUT_MAX_TASKS", 10),
			DiscoveryCacheTTL:   envDuration("CC_C2_DISCOVERY_TTL", time.Hour),
			JWKSMinRefreshEvery: envDuration("CC_C2_JWKS_MIN_REFRESH", time.Minute),
			HTTPTimeout:         envDuration("CC_C2_HTTP_TIMEOUT", 10*time.Second),
			ClockSkew:           envDuration("CC_C2_CLOCK_SKEW", 60*time.Second),
		},

		Sec: SecurityConfig{
			SessionCookieName: env("CC_SESSION_COOKIE", "cc_session"),
			SessionTTL:        envDuration("CC_SESSION_TTL", 8*time.Hour),
			SessionIdleTTL:    envDuration("CC_SESSION_IDLE_TTL", 2*time.Hour),
			CookieSecure:      envBool("CC_COOKIE_SECURE", false),
			CookieDomain:      env("CC_COOKIE_DOMAIN", ""),
			CORSOrigins:       envList("CC_CORS_ORIGINS"),
			RateLimitPerMin:   envInt("CC_RATE_LIMIT_PER_MIN", 600),
			TrustProxyHeaders: envBool("CC_TRUST_PROXY_HEADERS", true),
		},

		Job: JobConfig{
			Enabled:           envBool("CC_JOBS_ENABLED", true),
			OutboxInterval:    envDuration("CC_JOB_OUTBOX_INTERVAL", 15*time.Second),
			WebhookInterval:   envDuration("CC_JOB_WEBHOOK_INTERVAL", 15*time.Second),
			SLAInterval:       envDuration("CC_JOB_SLA_INTERVAL", time.Minute),
			RollupInterval:    envDuration("CC_JOB_ROLLUP_INTERVAL", 15*time.Minute),
			RetentionInterval: envDuration("CC_JOB_RETENTION_INTERVAL", 24*time.Hour),
			AutoCloseAfter:    envDuration("CC_JOB_AUTOCLOSE_AFTER", 14*24*time.Hour),
			OutboxMaxAttempts: envInt("CC_JOB_OUTBOX_MAX_ATTEMPTS", 8),
		},
	}

	// The issuer defaults to the portal origin plus /oidc. Configuring it as
	// the internal API host is the classic `iss` mismatch: discovery still
	// works, then every token fails validation.
	if c.C2.Issuer == "" {
		c.C2.Issuer = c.C2.PortalOrigin + "/oidc"
	}
	if c.C2.RedirectURL == "" {
		c.C2.RedirectURL = c.PublicURL + c.BasePath + "/api/auth/callback"
	}
	if c.C2.PostLogoutRedirectURL == "" {
		c.C2.PostLogoutRedirectURL = c.PublicURL + c.BasePath + "/"
	}
	if c.C2.PartnerBaseURL == "" {
		c.C2.PartnerBaseURL = c.C2.PortalOrigin
	}
	if c.C2.NotifyAudience == "" {
		c.C2.NotifyAudience = c.C2.PortalOrigin
	}
	if !hasScope(c.C2.Scopes, "openid") {
		c.C2.Scopes = append([]string{"openid"}, c.C2.Scopes...)
	}

	return c, c.Validate()
}

// Validate rejects configurations that would fail at runtime in ways that are
// hard to diagnose.
func (c *Config) Validate() error {
	var problems []string

	if c.C2.PortalOrigin == "" {
		problems = append(problems, "CC_C2_PORTAL_ORIGIN is required")
	}
	if strings.Contains(c.C2.Issuer, ":8088") {
		problems = append(problems,
			"CC_C2_ISSUER points at what looks like C2's internal API port; the issuer is the portal origin")
	}
	if c.IsProd() {
		if c.C2.ClientID == "" {
			problems = append(problems, "CC_C2_CLIENT_ID is required outside dev")
		}
		if c.C2.ClientSecret == "" && c.C2.ClientPrivateKeyPEM == "" {
			problems = append(problems, "one of CC_C2_CLIENT_SECRET or CC_C2_CLIENT_PRIVATE_KEY_PEM is required outside dev")
		}
		if !c.Sec.CookieSecure {
			problems = append(problems, "CC_COOKIE_SECURE must be true outside dev")
		}
		if c.DB.AutoMigrate {
			problems = append(problems, "CC_DB_AUTOMIGRATE must be false outside dev; use versioned migrations")
		}
	}
	if c.C2.ClientPrivateKeyPEM != "" && c.C2.ClientKeyID == "" {
		problems = append(problems, "CC_C2_CLIENT_KID is required when a client private key is configured")
	}
	if c.Sec.SessionTTL <= 0 {
		problems = append(problems, "CC_SESSION_TTL must be positive")
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// IsProd reports whether this is a non-development deployment.
func (c *Config) IsProd() bool { return c.Env != "dev" && c.Env != "test" }

// DiscoveryURL is the OIDC discovery document for the configured issuer.
func (c C2Config) DiscoveryURL() string {
	return c.Issuer + "/.well-known/openid-configuration"
}

// PartnerNotificationsURL is the endpoint that accepts one citizen
// notification per call.
func (c C2Config) PartnerNotificationsURL() string {
	return c.PartnerBaseURL + "/partner/notifications"
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// envAllowEmpty distinguishes "set to empty" from "not set", for the settings
// where an empty string is a real choice rather than an omission.
func envAllowEmpty(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// envFileOrValue prefers an inline value but falls back to reading a file, so
// private keys can be mounted as secrets rather than passed in the environment.
func envFileOrValue(valueKey, fileKey string) string {
	if v := env(valueKey, ""); v != "" {
		return v
	}
	if path := env(fileKey, ""); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return string(b)
		}
	}
	return ""
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(env(key, "")); err == nil {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(env(key, "")); err == nil {
		return v
	}
	return def
}

func envList(key string) []string {
	raw := env(key, "")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envListOr(key string, def []string) []string {
	if v := envList(key); len(v) > 0 {
		return v
	}
	return def
}
