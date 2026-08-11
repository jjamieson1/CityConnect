package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/domain"
)

func (s *Server) mountAuth(r chi.Router) {
	r.Route("/auth", func(a chi.Router) {
		a.Get("/login", s.handleLogin)
		a.Get("/callback", s.handleCallback)
		a.Post("/logout", s.handleLogout)
		a.Get("/me", s.handleMe)
		a.Get("/sessions", s.handleMySessions)
	})
}

// handleLogin redirects the browser into C2's authorization endpoint.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// An already-authenticated caller does not need a round trip.
	if p := principalFrom(r.Context()); p != nil && p.User != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already_signed_in"})
		return
	}

	url, err := s.agents.StartLogin(r.Context(), r.URL.Query().Get("returnTo"), queryBool(r, "silent"))
	if err != nil {
		s.log.ErrorContext(r.Context(), "could not start login", "error", err)
		writeProblem(w, r, http.StatusServiceUnavailable, "c2_unavailable",
			"Sign-in is unavailable because C2 could not be reached.")
		return
	}

	// An XHR caller wants the URL; a browser wants the redirect.
	if r.Header.Get("Accept") == "application/json" || queryBool(r, "json") {
		writeJSON(w, http.StatusOK, map[string]string{"authorizeUrl": url})
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// handleCallback completes the authorization-code exchange.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// C2 reports failures as a redirect parameter rather than a status code.
	if errCode := q.Get("error"); errCode != "" {
		// login_required is the expected answer to a silent prompt=none check,
		// not a failure worth showing anybody.
		if errCode == "login_required" || errCode == "interaction_required" {
			s.redirectToApp(w, r, "/login?reason=session_expired")
			return
		}
		s.log.WarnContext(r.Context(), "C2 returned an authorization error",
			"error", errCode, "description", q.Get("error_description"))
		s.redirectToApp(w, r, "/login?reason="+errCode)
		return
	}

	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_callback",
			"The sign-in response was missing its code or state.")
		return
	}

	res, err := s.agents.CompleteLogin(r.Context(), code, state, r.UserAgent(), clientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, agents.ErrNoAccess):
			s.redirectToApp(w, r, "/login?reason=no_access")
		case errors.Is(err, agents.ErrSuspended):
			s.redirectToApp(w, r, "/login?reason=suspended")
		case errors.Is(err, agents.ErrInvalidState):
			s.redirectToApp(w, r, "/login?reason=expired")
		default:
			s.log.ErrorContext(r.Context(), "login failed", "error", err)
			s.redirectToApp(w, r, "/login?reason=failed")
		}
		return
	}

	s.setSessionCookie(w, res.SessionToken, res.ExpiresAt)

	target := res.ReturnTo
	if target == "" {
		target = "/"
	}
	s.redirectToApp(w, r, target)
}

// redirectToApp sends the browser back into the SPA under the configured base
// path.
func (s *Server) redirectToApp(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, s.cfg.BasePath+path, http.StatusFound)
}

// handleLogout ends the local session and reports where to send the browser so
// the C2 session ends too.
//
// Both halves are needed: clearing only the local session leaves C2's alive,
// so the user stays SSO'd and silently walks straight back in.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(s.cfg.Sec.SessionCookieName)
	if err != nil {
		s.clearSessionCookie(w)
		writeJSON(w, http.StatusOK, map[string]any{"status": "signed_out"})
		return
	}

	endSessionURL, err := s.agents.Logout(r.Context(), cookie.Value, clientIP(r))
	s.clearSessionCookie(w)

	if err != nil && !errors.Is(err, agents.ErrNoSession) {
		s.log.WarnContext(r.Context(), "logout encountered an error", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "signed_out",
		"endSessionUrl": endSessionURL,
	})
}

// meResponse is the identity and capability payload the SPA boots from.
type meResponse struct {
	User        *domain.User        `json:"user"`
	Department  *domain.Department  `json:"department,omitempty"`
	Queues      []domain.Queue      `json:"queues"`
	Permissions []agents.Permission `json:"permissions"`
	IsSystem    bool                `json:"isSystem"`
	CrossDept   bool                `json:"crossDepartment"`
}

// handleMe returns the caller's identity and capabilities.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}

	out := meResponse{
		User:      p.User,
		IsSystem:  p.IsSystem(),
		CrossDept: p.CanCrossDepartment(),
		Queues:    []domain.Queue{},
	}

	// The SPA renders from this list rather than duplicating the role table,
	// so a permission change here reaches the UI without a client release.
	for _, perm := range []agents.Permission{
		agents.PermRequestRead, agents.PermRequestWrite, agents.PermRequestAssign,
		agents.PermRequestTransfer, agents.PermContactRead, agents.PermContactWrite,
		agents.PermContactMerge, agents.PermNotifySend, agents.PermReportRead,
		agents.PermConfigRead, agents.PermConfigWrite, agents.PermUserManage,
		agents.PermAuditRead, agents.PermSystemManage,
	} {
		if p.Can(perm) {
			out.Permissions = append(out.Permissions, perm)
		}
	}

	if p.User != nil {
		if full, err := s.agents.GetUser(r.Context(), p.User.ID); err == nil {
			out.User = full
			out.Department = full.Department
			if full.Queues != nil {
				out.Queues = full.Queues
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMySessions(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || p.User == nil {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}
	sessions, err := s.agents.ListSessions(r.Context(), p.User.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": sessions})
}

// ---------------------------------------------------------------------------
// C2 inbound
// ---------------------------------------------------------------------------

func (s *Server) mountC2(r chi.Router) {
	r.Route("/c2", func(c chi.Router) {
		c.Post("/backchannel-logout", s.handleBackchannelLogout)
		c.Get("/jwks", s.handleOurJWKS)
	})
	// The callout path is shaped for C2's {sub} placeholder.
	r.Get("/citizens/{sub}/status", s.handleCallout)
}

// handleBackchannelLogout accepts C2's server-to-server logout notification.
//
// The response contract is narrow and worth following exactly: 200 on success,
// 400 on a validation failure, no-store, and never a redirect.
func (s *Server) handleBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	if err := r.ParseForm(); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Could not parse the form body.")
		return
	}
	token := r.PostFormValue("logout_token")
	if token == "" {
		writeProblem(w, r, http.StatusBadRequest, "missing_token", "logout_token is required.")
		return
	}

	if err := s.agents.BackchannelLogout(r.Context(), token); err != nil {
		s.log.WarnContext(r.Context(), "back-channel logout rejected", "error", err)
		writeProblem(w, r, http.StatusBadRequest, "invalid_token", "The logout token failed validation.")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleOurJWKS publishes the public half of the key we sign client assertions
// with, which is how C2 verifies our partner notification calls.
func (s *Server) handleOurJWKS(w http.ResponseWriter, r *http.Request) {
	if s.Notify == nil {
		writeJSON(w, http.StatusOK, map[string]any{"keys": []any{}})
		return
	}
	set, err := s.Notify.PublicJWKS()
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, set)
}

// handleCallout serves C2's Service Card callout.
//
// C2 gives this about five seconds and falls back to static card content on
// any error, so the handler bounds its own work and prefers a clean status
// code to a partial body.
func (s *Server) handleCallout(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	sub, mode, err := s.Callout.Authenticate(r.Context(), r)
	if err != nil {
		s.Callout.Log(r.Context(), domain.CalloutLog{
			AuthMode: mode, StatusCode: http.StatusUnauthorized,
			Outcome: "unauthorized", DurationMS: time.Since(start).Milliseconds(),
		})
		writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "")
		return
	}

	// In app_key mode the assertion carries no subject, so the path supplies
	// it. In signed_jwt mode the verified token wins over the URL, which is
	// what stops a caller reading another citizen's data by editing the path.
	pathSub := chi.URLParam(r, "sub")
	if sub == "" {
		sub = pathSub
	} else if pathSub != "" && pathSub != sub {
		s.log.WarnContext(r.Context(), "callout path subject does not match the assertion",
			"path_sub", pathSub, "token_sub", sub)
		writeProblem(w, r, http.StatusForbidden, "subject_mismatch", "")
		return
	}

	bundle, err := s.Callout.Build(r.Context(), sub)
	if err != nil {
		s.log.ErrorContext(r.Context(), "callout build failed", "sub", sub, "error", err)
		s.Callout.Log(r.Context(), domain.CalloutLog{
			C2Sub: sub, AuthMode: mode, StatusCode: http.StatusServiceUnavailable,
			Outcome: "error", Error: err.Error(), DurationMS: time.Since(start).Milliseconds(),
		})
		// A clean 5xx beats a malformed 200: C2 shows the static fallback.
		writeProblem(w, r, http.StatusServiceUnavailable, "unavailable", "")
		return
	}

	s.Callout.Log(r.Context(), domain.CalloutLog{
		C2Sub: sub, AuthMode: mode, StatusCode: http.StatusOK, Outcome: "ok",
		OpenRequests: len(bundle.Tasks), DurationMS: time.Since(start).Milliseconds(),
	})
	writeJSON(w, http.StatusOK, bundle)
}
