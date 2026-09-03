package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/portal"
	"github.com/jjamieson1/CityConnect/internal/requests"
)

// ctxCitizen carries the signed-in citizen. It is a distinct context key from
// ctxPrincipal, so a portal session can never be mistaken for a staff one by a
// handler that reads the wrong value.
const ctxCitizen ctxKey = "citizen"

func citizenFrom(ctx context.Context) *domain.Contact {
	if c, ok := ctx.Value(ctxCitizen).(*domain.Contact); ok {
		return c
	}
	return nil
}

// mountPortal wires the public, citizen-facing surface.
//
// It sits outside the staff group entirely and has its own authentication
// middleware. Nothing under /api/portal consults the staff Principal, and
// nothing outside it accepts a portal cookie.
func (s *Server) mountPortal(r chi.Router) {
	r.Route("/portal", func(p chi.Router) {
		// Sign-in is public by definition.
		p.Get("/auth/login", s.handlePortalLogin)
		p.Get("/auth/callback", s.handlePortalCallback)
		p.Post("/auth/logout", s.handlePortalLogout)

		// The catalogue is readable without signing in, so somebody can see
		// what the city offers before deciding to.
		p.Get("/catalog", s.handlePortalCatalog)

		// Tracking is public by design: most people who report a pothole never
		// create an account, and telling them to sign in to find out what
		// happened is how a service centre gets phoned instead.
		p.Post("/requests/track", s.handlePortalTrack)

		p.Group(func(auth chi.Router) {
			auth.Use(s.requireCitizen)

			auth.Get("/me", s.handlePortalMe)
			auth.Get("/requests", s.handlePortalRequests)
			auth.Post("/requests", s.handlePortalCreate)
			auth.Get("/requests/{reference}", s.handlePortalRequest)
			auth.Post("/requests/{reference}/comments", s.handlePortalComment)
			auth.Post("/requests/{reference}/cancel", s.handlePortalCancel)
			auth.Post("/requests/{reference}/rating", s.handlePortalRate)
		})
	})
}

// requireCitizen authenticates a portal session.
//
// It reads a different cookie from the staff session and resolves against a
// different table, so the two surfaces cannot be confused for one another even
// by mistake.
func (s *Server) requireCitizen(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.portalCookieName())
		if err != nil {
			writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}

		contact, _, err := s.Portal.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			s.clearPortalCookie(w)
			writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}

		ctx := context.WithValue(r.Context(), ctxCitizen, contact)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) handlePortalLogin(w http.ResponseWriter, r *http.Request) {
	url, err := s.Portal.StartLogin(r.Context(), r.URL.Query().Get("returnTo"), queryBool(r, "silent"))
	if err != nil {
		s.log.ErrorContext(r.Context(), "could not start portal sign-in", "error", err)
		writeProblem(w, r, http.StatusServiceUnavailable, "c2_unavailable",
			"Sign-in is unavailable at the moment.")
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// handlePortalCallback serves deployments that register a second redirect_uri
// for the portal. The shared /api/auth/callback dispatches by audience and is
// the path used by default.
func (s *Server) handlePortalCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errCode := q.Get("error"); errCode != "" {
		s.redirectToApp(w, r, "/portal?reason="+errCode)
		return
	}
	s.completePortalLogin(w, r, q.Get("code"), q.Get("state"))
}

func (s *Server) handlePortalLogout(w http.ResponseWriter, r *http.Request) {
	var endSessionURL string
	if cookie, err := r.Cookie(s.portalCookieName()); err == nil {
		endSessionURL, _ = s.Portal.Logout(r.Context(), cookie.Value)
	}
	s.clearPortalCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "signed_out", "endSessionUrl": endSessionURL,
	})
}

func (s *Server) handlePortalCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Portal.Catalog(r.Context())
	if err != nil {
		failPortal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing(entries))
}

// handlePortalTrack answers "where is my request" for somebody with no account.
//
// A POST rather than a GET, deliberately. The verification value is a secret in
// this exchange, and a secret in a query string is written to browser history,
// proxy logs, referrer headers and our own access log — four places it survives
// long after the request it authorised.
func (s *Server) handlePortalTrack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ReferenceNumber   string `json:"referenceNumber"`
		VerificationValue string `json:"verificationValue"`
	}
	if !decode(w, r, &body) {
		return
	}

	// Throttle on the *normalised* reference, not the text as typed. Lookup
	// folds O to 0 and I and L to 1, so BBY-7K4M-2QX0 and BBY-7K4M-2QXO reach
	// the same request; counting the raw strings would let a caller multiply
	// their attempts by varying characters the fold makes equivalent.
	reference := requests.NormalizeReference(body.ReferenceNumber)
	ip := clientIP(r)

	// Two buckets. Per-IP stops one host sweeping many references; per-reference
	// stops a distributed attempt at one known reference's verification value.
	// Both are consumed before either is judged, so a caller cannot learn which
	// limit they hit.
	byIP := s.tracker.allow("track|ip|" + ip)
	byRef := s.tracker.allow("track|ref|" + reference)
	if !byIP || !byRef {
		w.Header().Set("Retry-After", "60")
		writeProblem(w, r, http.StatusTooManyRequests, "rate_limited",
			"Too many attempts. Wait a minute and try again.")
		return
	}

	view, err := s.Portal.Track(r.Context(), portal.TrackInput{
		Reference:    body.ReferenceNumber,
		Verification: body.VerificationValue,
	})
	if err != nil {
		// One message for every failure. A reference that does not exist, one
		// belonging to somebody else, an anonymous report, and a wrong contact
		// detail are indistinguishable here on purpose — anything else answers
		// "does this reference exist?" for a caller who has not proved they may
		// ask. The copy has to carry the guidance the status code cannot.
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"We could not find a request matching that reference and contact detail. "+
				"Check both against your confirmation message. Reports submitted "+
				"anonymously cannot be tracked.")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handlePortalMe(w http.ResponseWriter, r *http.Request) {
	profile, err := s.Portal.Profile(r.Context(), citizenFrom(r.Context()))
	if err != nil {
		failPortal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (s *Server) handlePortalRequests(w http.ResponseWriter, r *http.Request) {
	items, err := s.Portal.MyRequests(r.Context(), citizenFrom(r.Context()).ID, queryBool(r, "openOnly"))
	if err != nil {
		failPortal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing(items))
}

func (s *Server) handlePortalRequest(w http.ResponseWriter, r *http.Request) {
	view, err := s.Portal.Request(r.Context(),
		citizenFrom(r.Context()).ID, chi.URLParam(r, "reference"))
	if err != nil {
		failPortal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type portalCreateBody struct {
	ServiceTypeID string         `json:"serviceTypeId"`
	Subject       string         `json:"subject,omitempty"`
	Description   string         `json:"description,omitempty"`
	Address1      string         `json:"address1,omitempty"`
	City          string         `json:"city,omitempty"`
	PostalCode    string         `json:"postalCode,omitempty"`
	Ward          string         `json:"ward,omitempty"`
	FormData      domain.JSONMap `json:"formData,omitempty"`
}

func (s *Server) handlePortalCreate(w http.ResponseWriter, r *http.Request) {
	var body portalCreateBody
	if !decode(w, r, &body) {
		return
	}
	view, err := s.Portal.Create(r.Context(), citizenFrom(r.Context()), portal.CreateInput{
		ServiceTypeID: body.ServiceTypeID, Subject: body.Subject,
		Description: body.Description, Address1: body.Address1, City: body.City,
		PostalCode: body.PostalCode, Ward: body.Ward, FormData: body.FormData,
	})
	if err != nil {
		failPortal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

type portalCommentBody struct {
	Body string `json:"body"`
}

func (s *Server) handlePortalComment(w http.ResponseWriter, r *http.Request) {
	var body portalCommentBody
	if !decode(w, r, &body) {
		return
	}
	err := s.Portal.Comment(r.Context(), citizenFrom(r.Context()),
		chi.URLParam(r, "reference"), body.Body)
	if err != nil {
		failPortal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "posted"})
}

type portalCancelBody struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) handlePortalCancel(w http.ResponseWriter, r *http.Request) {
	var body portalCancelBody
	if !decode(w, r, &body) {
		return
	}
	err := s.Portal.Cancel(r.Context(), citizenFrom(r.Context()),
		chi.URLParam(r, "reference"), body.Reason)
	if err != nil {
		failPortal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "withdrawn"})
}

type portalRatingBody struct {
	Score   int    `json:"score"`
	Comment string `json:"comment,omitempty"`
}

func (s *Server) handlePortalRate(w http.ResponseWriter, r *http.Request) {
	var body portalRatingBody
	if !decode(w, r, &body) {
		return
	}
	err := s.Portal.Rate(r.Context(), citizenFrom(r.Context()),
		chi.URLParam(r, "reference"), body.Score, body.Comment)
	if err != nil {
		failPortal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

// failPortal maps portal errors to responses.
//
// A request belonging to somebody else returns 404, identical to one that does
// not exist — distinguishing them would let anyone enumerate which references
// are real by watching the status code change.
func failPortal(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, portal.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", "We could not find that request.")
	case errors.Is(err, portal.ErrNoSession):
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
	case errors.Is(err, portal.ErrNotPermitted):
		writeProblem(w, r, http.StatusConflict, "not_permitted", err.Error())
	case errors.Is(err, portal.ErrInvalidInput):
		writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		fail(w, r, err)
	}
}

// ---------------------------------------------------------------------------
// Portal cookie
// ---------------------------------------------------------------------------

// portalCookieName is deliberately distinct from the staff cookie, so the two
// sessions coexist in one browser — an agent can be signed into the console and
// look at their own reports as a resident without one evicting the other.
func (s *Server) portalCookieName() string { return s.cfg.Sec.SessionCookieName + "_portal" }

func (s *Server) setPortalCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: s.portalCookieName(), Value: token,
		Path: s.cookiePath(), Domain: s.cfg.Sec.CookieDomain,
		Expires: expires, HttpOnly: true, Secure: s.cfg.Sec.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearPortalCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.portalCookieName(), Value: "",
		Path: s.cookiePath(), Domain: s.cfg.Sec.CookieDomain,
		MaxAge: -1, HttpOnly: true, Secure: s.cfg.Sec.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}
