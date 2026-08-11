package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/jjamieson1/CityConnect/internal/agents"
)

type ctxKey string

const (
	ctxRequestID ctxKey = "requestID"
	ctxPrincipal ctxKey = "principal"
)

// RequestIDFrom returns the correlation id attached to a request.
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

// principalFrom returns the authenticated caller.
func principalFrom(ctx context.Context) *agents.Principal {
	if p, ok := ctx.Value(ctxPrincipal).(*agents.Principal); ok {
		return p
	}
	return nil
}

// requestID attaches a correlation id, preferring one supplied upstream so a
// trace survives the Apache hop.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// statusWriter captures the status code for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// requestLogger emits one structured line per request.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Health and metrics are polled constantly; logging them buries
			// everything that matters.
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", sw.bytes,
				"ip", clientIP(r),
				"request_id", RequestIDFrom(r.Context()),
			}
			if p := principalFrom(r.Context()); p != nil {
				attrs = append(attrs, "actor", p.Label())
			}

			switch {
			case sw.status >= 500:
				log.ErrorContext(r.Context(), "request failed", attrs...)
			case sw.status >= 400:
				log.WarnContext(r.Context(), "request rejected", attrs...)
			default:
				log.InfoContext(r.Context(), "request", attrs...)
			}
		})
	}
}

// recoverer turns a panic into a 500 rather than a dropped connection.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					if errors.Is(p.(error), http.ErrAbortHandler) {
						panic(p)
					}
					log.ErrorContext(r.Context(), "handler panicked",
						"path", r.URL.Path, "panic", p,
						"request_id", RequestIDFrom(r.Context()))
					writeProblem(w, r, http.StatusInternalServerError, "internal",
						"Something went wrong. The error has been logged.")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders applies the defaults appropriate to a JSON API behind a SPA.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// Auth resolves the caller from a session cookie or a bearer token and
// attaches the principal. It never rejects on its own — RequireAuth does that
// — so a route can be public while still knowing who is calling.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)

		if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") {
			raw := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
			if strings.HasPrefix(raw, agents.TokenPrefix) {
				tp, err := s.agents.AuthenticateToken(r.Context(), raw)
				if err == nil {
					ctx := context.WithValue(r.Context(), ctxPrincipal, agents.NewTokenPrincipal(tp, ip))
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
		}

		if cookie, err := r.Cookie(s.cfg.Sec.SessionCookieName); err == nil {
			user, _, err := s.agents.Authenticate(r.Context(), cookie.Value)
			if err == nil {
				ctx := context.WithValue(r.Context(), ctxPrincipal, agents.NewUserPrincipal(user, ip))
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// A stale cookie is cleared so the browser stops sending it.
			if errors.Is(err, agents.ErrNoSession) {
				s.clearSessionCookie(w)
			}
		}

		next.ServeHTTP(w, r)
	})
}

// requireAuth rejects unauthenticated callers.
func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalFrom(r.Context()) == nil {
			writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// require builds middleware that enforces a permission.
func require(perm agents.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := principalFrom(r.Context())
			if p == nil {
				writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
				return
			}
			if !p.Can(perm) {
				writeProblem(w, r, http.StatusForbidden, "forbidden",
					"Your role does not permit this action ("+string(perm)+").")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rateLimiter applies a per-IP token bucket.
//
// This protects the two surfaces that are reachable without a staff session:
// the callout endpoint C2 calls, and the partner API. It is a coarse defence,
// deliberately — the real protection is authentication, and this exists so an
// unauthenticated flood cannot exhaust the database pool.
type rateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	perMin   int
	lastSwep time.Time
}

type bucket struct {
	limiter *rate.Limiter
	seen    time.Time
}

func newRateLimiter(perMin int) *rateLimiter {
	if perMin <= 0 {
		perMin = 600
	}
	return &rateLimiter{buckets: map[string]*bucket{}, perMin: perMin, lastSwep: time.Now()}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if now.Sub(rl.lastSwep) > 10*time.Minute {
		for k, b := range rl.buckets {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(rl.buckets, k)
			}
		}
		rl.lastSwep = now
	}

	b, ok := rl.buckets[ip]
	if !ok {
		// Burst equals the per-minute allowance so a page that fires a dozen
		// parallel requests on load is not immediately throttled.
		b = &bucket{limiter: rate.NewLimiter(rate.Limit(float64(rl.perMin)/60.0), rl.perMin)}
		rl.buckets[ip] = b
	}
	b.seen = now
	return b.limiter.Allow()
}

func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeProblem(w, r, http.StatusTooManyRequests, "rate_limited",
				"Too many requests. Try again shortly.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP resolves the caller's address, trusting X-Forwarded-For only when
// configured to — behind Apache the direct peer is always the proxy.
var trustProxyHeaders = true

func clientIP(r *http.Request) string {
	if trustProxyHeaders {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			// The left-most entry is the original client.
			if first, _, found := strings.Cut(fwd, ","); found {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(fwd)
		}
		if real := r.Header.Get("X-Real-Ip"); real != "" {
			return strings.TrimSpace(real)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
