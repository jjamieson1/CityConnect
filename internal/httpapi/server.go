package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/c2/callout"
	"github.com/jjamieson1/CityConnect/internal/c2/notify"
	"github.com/jjamieson1/CityConnect/internal/c2/oidc"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/interactions"
	"github.com/jjamieson1/CityConnect/internal/jobs"
	"github.com/jjamieson1/CityConnect/internal/notifications"
	"github.com/jjamieson1/CityConnect/internal/reports"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/routing"
	"github.com/jjamieson1/CityConnect/internal/webhooks"
)

// Deps are everything the HTTP layer needs.
type Deps struct {
	DB            *gorm.DB
	Config        *config.Config
	Log           *slog.Logger
	OIDC          *oidc.Provider
	Notify        *notify.Client
	Agents        *agents.Service
	Audit         *audit.Service
	Contacts      *contacts.Service
	Interactions  *interactions.Service
	Catalog       *catalog.Service
	Routing       *routing.Service
	Requests      *requests.Service
	Notifications *notifications.Service
	Webhooks      *webhooks.Service
	Reports       *reports.Service
	Callout       *callout.Service
	Jobs          *jobs.Runner
	Attachments   *requests.AttachmentStore
}

// Server owns the router and its dependencies.
type Server struct {
	Deps
	cfg     *config.Config
	log     *slog.Logger
	agents  *agents.Service
	router  chi.Router
	limiter *rateLimiter
	started time.Time
}

// New builds the HTTP server.
func New(d Deps) *Server {
	trustProxyHeaders = d.Config.Sec.TrustProxyHeaders

	s := &Server{
		Deps:    d,
		cfg:     d.Config,
		log:     d.Log,
		agents:  d.Agents,
		limiter: newRateLimiter(d.Config.Sec.RateLimitPerMin),
		started: time.Now(),
	}
	s.routes()
	return s
}

// Handler exposes the router.
func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) routes() {
	r := chi.NewRouter()

	r.Use(requestID)
	r.Use(recoverer(s.log))
	r.Use(requestLogger(s.log))
	r.Use(securityHeaders)
	r.Use(middleware.Compress(5))
	r.Use(middleware.Timeout(60 * time.Second))

	if len(s.cfg.Sec.CORSOrigins) > 0 {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins:   s.cfg.Sec.CORSOrigins,
			AllowedMethods:   []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization", "X-Request-Id", "Idempotency-Key"},
			ExposedHeaders:   []string{"X-Request-Id"},
			AllowCredentials: true,
			MaxAge:           300,
		}))
	}

	// Operational endpoints, deliberately outside /api so a proxy can expose
	// or hide them independently of the application surface.
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/api", func(api chi.Router) {
		api.Use(s.limiter.middleware)
		api.Use(s.auth)

		s.mountAuth(api)
		s.mountC2(api)

		api.Group(func(p chi.Router) {
			p.Use(requireAuth)

			s.mountContacts(p)
			s.mountInteractions(p)
			s.mountRequests(p)
			s.mountCatalog(p)
			s.mountRouting(p)
			s.mountAdmin(p)
			s.mountNotifications(p)
			s.mountReports(p)
			s.mountSearch(p)
		})
	})

	s.router = r
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"uptime": time.Since(s.started).Round(time.Second).String(),
	})
}

// handleReady reports whether the service can actually do its job.
//
// C2 reachability is part of readiness rather than a detail, because staff
// SSO is the only login path: if discovery is unreachable, nobody can sign in,
// and that must be diagnosable in seconds rather than mistaken for a
// CityConnect fault.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	report := map[string]any{"status": "ok"}
	healthy := true

	if sqlDB, err := s.DB.DB(); err != nil || sqlDB.PingContext(ctx) != nil {
		report["database"] = "unreachable"
		healthy = false
	} else {
		report["database"] = "ok"
	}

	if err := s.OIDC.Check(ctx); err != nil {
		report["c2"] = map[string]any{
			"status": "unreachable",
			"error":  err.Error(),
			"hint": "Staff sign-in requires C2. Check the portal origin and issuer " +
				"configuration; the issuer is the portal origin, not C2's internal API host.",
		}
		healthy = false
	} else {
		doc, _ := s.OIDC.Discovery(ctx)
		info := map[string]any{"status": "ok", "issuer": s.cfg.C2.Issuer}
		if doc != nil {
			info["tokenEndpoint"] = doc.TokenEndpoint
			info["jwksUri"] = doc.JWKSURI
		}
		report["c2"] = info
	}

	status := http.StatusOK
	if !healthy {
		report["status"] = "degraded"
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, report)
}

// ---------------------------------------------------------------------------
// Session cookie
// ---------------------------------------------------------------------------

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.Sec.SessionCookieName,
		Value:    token,
		Path:     s.cookiePath(),
		Domain:   s.cfg.Sec.CookieDomain,
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.cfg.Sec.CookieSecure,
		// Lax rather than Strict: the session cookie must survive the
		// top-level redirect back from C2, which Strict would drop.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.Sec.SessionCookieName,
		Value:    "",
		Path:     s.cookiePath(),
		Domain:   s.cfg.Sec.CookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Sec.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) cookiePath() string {
	if s.cfg.BasePath == "" {
		return "/"
	}
	return s.cfg.BasePath
}
