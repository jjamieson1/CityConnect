// Package config loads and validates all runtime configuration from the
// environment. Everything is resolved and validated once, at boot, so a
// misconfigured deployment fails immediately rather than on first request.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully-resolved application configuration.
type Config struct {
	Env       string // dev | staging | prod
	Addr      string // listen address, e.g. ":4021"
	BasePath  string // public base path, e.g. "/cityconnect"
	PublicURL string // externally reachable base URL of the staff console
	// PortalPublicURL is the citizen portal's own origin.
	//
	// A separate origin, not a path: it gives the public app its own cookie
	// jar, so script running there has no ambient authority over a staff
	// session in the same browser. The API is served under both origins, which
	// keeps each app's calls same-origin and avoids SameSite=None.
	PortalPublicURL string
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
	// PortalRedirectURL is the callback on the citizen origin. C2 matches
	// redirect URIs exactly, so a second origin needs its own registration.
	PortalRedirectURL string
	// PostLogoutRedirectURL is where C2 sends the browser after RP-initiated
	// logout. Must be pre-registered; C2 matches redirect URIs exactly.
	PostLogoutRedirectURL string
	// PortalPostLogoutRedirectURL is the same, for the citizen origin. A
	// citizen signing out must land back on the portal, not on the staff
	// console — and since C2 matches these exactly, sending the console's
	// value from the portal is rejected outright rather than merely landing
	// in the wrong place.
	PortalPostLogoutRedirectURL string
	Scopes                      []string

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
	CalloutAppKey      string
	CalloutAppSecret   string
	CalloutAllowAppKey bool
	CalloutCacheTTL    time.Duration
	CalloutMaxTasks    int
	// CalloutQuickLinks are service-type codes offered on the Service Card as
	// "start a new request" shortcuts, alongside the citizen's open requests.
	// Codes rather than labels: the name and wording come from the catalogue,
	// so renaming a service in the console follows through to the card.
	CalloutQuickLinks   []string
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
		Addr: env("CC_ADDR", ""),
		// An explicitly empty base path is meaningful — it deploys at the
		// root — so it must not fall through to the default the way a blank
		// value does everywhere else.
		BasePath:        strings.TrimSuffix(envAllowEmpty("CC_BASE_PATH", "/cityconnect"), "/"),
		PublicURL:       strings.TrimSuffix(env("CC_PUBLIC_URL", "http://localhost:4021"), "/"),
		PortalPublicURL: strings.TrimSuffix(env("CC_PORTAL_PUBLIC_URL", ""), "/"),
		ShutdownTimeout: envDuration("CC_SHUTDOWN_TIMEOUT", 20*time.Second),

		AttachmentDir:        env("CC_ATTACHMENT_DIR", "./data/attachments"),
		AttachmentMaxMB:      int64(envInt("CC_ATTACHMENT_MAX_MB", 25)),
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
			PortalOrigin:                strings.TrimSuffix(env("CC_C2_PORTAL_ORIGIN", "http://localhost:5173"), "/"),
			Issuer:                      strings.TrimSuffix(env("CC_C2_ISSUER", ""), "/"),
			ClientID:                    env("CC_C2_CLIENT_ID", ""),
			ClientSecret:                env("CC_C2_CLIENT_SECRET", ""),
			RedirectURL:                 env("CC_C2_REDIRECT_URL", ""),
			PortalRedirectURL:           env("CC_C2_PORTAL_REDIRECT_URL", ""),
			PostLogoutRedirectURL:       env("CC_C2_POST_LOGOUT_REDIRECT_URL", ""),
			PortalPostLogoutRedirectURL: env("CC_C2_PORTAL_POST_LOGOUT_REDIRECT_URL", ""),
			Scopes:                      envListOr("CC_C2_SCOPES", []string{"openid", "profile", "email"}),

			PartnerBaseURL:      strings.TrimSuffix(env("CC_C2_PARTNER_BASE_URL", ""), "/"),
			NotifyAudience:      env("CC_C2_NOTIFY_AUDIENCE", ""),
			ClientPrivateKeyPEM: envFileOrValue("CC_C2_CLIENT_PRIVATE_KEY_PEM", "CC_C2_CLIENT_PRIVATE_KEY_FILE"),
			ClientKeyID:         env("CC_C2_CLIENT_KID", ""),

			CalloutAppKey:      env("CC_C2_CALLOUT_APP_KEY", ""),
			CalloutAppSecret:   env("CC_C2_CALLOUT_APP_SECRET", ""),
			CalloutAllowAppKey: envBool("CC_C2_CALLOUT_ALLOW_APP_KEY", false),
			CalloutCacheTTL:    envDuration("CC_C2_CALLOUT_CACHE_TTL", 20*time.Second),
			CalloutMaxTasks:    envInt("CC_C2_CALLOUT_MAX_TASKS", 10),
			CalloutQuickLinks: envListOr("CC_C2_CALLOUT_QUICK_LINKS",
				[]string{"GENERAL", "MISSED-COLLECTION"}),
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

	// Production binds loopback. Everything C2 and every browser reaches goes
	// through Apache, which proxies to 127.0.0.1 on the same host, so the
	// listening port has no reason to be reachable from anywhere else — and
	// the security of the two-origin split assumes exactly that.
	//
	// Development binds all interfaces, so the console can be opened from a
	// phone on the same network to check how a form behaves on a real device.
	if c.Addr == "" {
		if c.IsProd() {
			c.Addr = "127.0.0.1:4021"
		} else {
			c.Addr = ":4021"
		}
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
	// Without a distinct portal origin the two apps share one, which is the
	// single-app arrangement: the portal keeps working, it simply does not get
	// its own cookie jar.
	if c.PortalPublicURL == "" {
		c.PortalPublicURL = c.PublicURL + c.BasePath
	}
	if c.C2.PortalRedirectURL == "" {
		c.C2.PortalRedirectURL = c.PortalPublicURL + "/api/auth/callback"
	}

	// The post-logout return addresses are deliberately NOT derived, unlike
	// the redirect URIs above.
	//
	// C2 exact-matches both against its registration list, so a derived value
	// is one nobody registered — C2 answers `post_logout_redirect_uri invalid`
	// and the user is stranded on an error page. Sending nothing is legal
	// OIDC: C2 ends the session and shows its own signed-out page. Worse
	// looking, but it works.
	//
	// The asymmetry is about when the failure shows up. A wrong redirect_uri
	// breaks sign-in, so it is found before anyone can use the system. A wrong
	// post_logout_redirect_uri breaks nothing until the first person signs
	// out, by which time the deployment looks healthy. Defaulting to a value
	// that cannot work turns a missing nicety into a broken one, so these stay
	// unset until an operator registers them and says so.
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

// PubliclyBound reports whether the listener accepts connections from beyond
// this host.
//
// It is not an error — a container or a deployment behind an external load
// balancer must bind an outward interface, and refusing to start would be
// wrong. It is worth saying out loud at boot, because the arrangement this
// system relies on puts Apache in front of everything.
func (c *Config) PubliclyBound() bool {
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		// Unparseable. Report the worse case rather than staying quiet about an
		// address nobody can reason about.
		return true
	}
	if host == "localhost" {
		return false
	}
	// An empty host, 0.0.0.0 or :: all mean every interface. Any other address
	// is a specific interface, which is only loopback if it says so.
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
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
