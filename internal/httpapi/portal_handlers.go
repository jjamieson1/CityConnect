package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
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

		// Reporting is public for the same reason, and for a stronger one: a
		// hazard in the road is worth knowing about whether or not the person
		// who saw it wants an account. The handler reads the session if there
		// is one and files anonymously if there is not — it is deliberately
		// outside requireCitizen rather than branching inside it.
		p.Post("/requests", s.handlePortalCreate)

		// The token an anonymous submission has to present. Fetched when the
		// form opens, spent when it is sent.
		p.Get("/form-token", s.handlePortalFormToken)

		// Photos follow the report. Public for the same reason reporting is:
		// an anonymous reporter has no session, and presents the short-lived
		// grant issued when they filed instead.
		p.Post("/requests/{reference}/attachments", s.handlePortalUpload)

		p.Group(func(auth chi.Router) {
			auth.Use(s.requireCitizen)

			auth.Get("/me", s.handlePortalMe)
			auth.Get("/requests", s.handlePortalRequests)
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

	// FormToken is the single-use token from GET /portal/form-token. Required
	// for an anonymous submission and ignored for a signed-in one, which has an
	// account behind it already.
	FormToken string `json:"formToken,omitempty"`

	// WebsiteURL is a trap and is never read as data. The portal renders it
	// hidden from sight, from the keyboard and from assistive technology, so no
	// resident can fill it in and a form-filling script will.
	//
	// It must be declared here because the decoder rejects unknown fields —
	// which is itself most of the defence, since a bot cannot invent a field
	// name that gets through.
	WebsiteURL string `json:"websiteUrl,omitempty"`
}

// handlePortalFormToken hands out the token an anonymous submission must
// present.
//
// Rate limited harder than anything else on the public surface, because this is
// where the sustained ceiling actually is: every anonymous report needs a fresh
// token, so however many times a script posts, it can only go as fast as it is
// given tokens.
func (s *Server) handlePortalFormToken(w http.ResponseWriter, r *http.Request) {
	if !s.submitter.allow("form-token|" + clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeProblem(w, r, http.StatusTooManyRequests, "rate_limited",
			"Too many requests. Wait a minute and try again.")
		return
	}

	token, err := s.forms.issue(time.Now())
	if err != nil {
		s.log.ErrorContext(r.Context(), "could not issue a form token", "error", err)
		writeProblem(w, r, http.StatusServiceUnavailable, "unavailable",
			"Reporting is unavailable at the moment. Please try again shortly.")
		return
	}
	// No caching anywhere: a token is single-use, and a cached one is a token
	// that fails for the next person to be handed it.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// optionalCitizen resolves a portal session if the caller has one, and reports
// nothing if they do not.
//
// Distinct from requireCitizen on purpose. This is the only shape where a bad
// or expired cookie must not be an error: the resident is reporting a hazard,
// and refusing them because their session lapsed would lose the report to spare
// them a sign-in they did not ask for. It fails open into the anonymous path,
// which is the weaker deal, never the stronger one.
func (s *Server) optionalCitizen(r *http.Request) *domain.Contact {
	cookie, err := r.Cookie(s.portalCookieName())
	if err != nil {
		return nil
	}
	contact, _, err := s.Portal.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		return nil
	}
	return contact
}

// allowAnonymousSubmission runs the abuse checks that only apply when there is
// no account behind the request. It writes the response and reports false when
// the submission should not proceed.
//
// Three layers, cheapest first:
//
//  1. A per-address rate limit, as a backstop rather than the defence. It is
//     set generously because a municipal office, a library and a block of flats
//     all share one address — locking out a whole building to stop one script
//     is a worse failure than the script.
//  2. The honeypot, which costs nothing and catches the naive case.
//  3. The single-use, time-bound, signed form token, which is the actual
//     control and the reason the rate limit can afford to be generous.
//
// Every refusal answers the same way. Telling a caller which layer stopped them
// is telling them what to change.
func (s *Server) allowAnonymousSubmission(w http.ResponseWriter, r *http.Request, body portalCreateBody) bool {
	if !s.submitter.allow("submit|" + clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeProblem(w, r, http.StatusTooManyRequests, "rate_limited",
			"Too many reports from this connection just now. Wait a minute and try again.")
		return false
	}

	if strings.TrimSpace(body.WebsiteURL) != "" {
		s.log.InfoContext(r.Context(), "anonymous submission rejected: honeypot filled",
			"ip", clientIP(r))
		s.refuseAnonymousSubmission(w, r)
		return false
	}

	if err := s.forms.verify(strings.TrimSpace(body.FormToken), time.Now()); err != nil {
		// Logged with the reason, answered without it. An operator needs to
		// tell a clock-skew problem from a scripted flood; the caller does not.
		s.log.InfoContext(r.Context(), "anonymous submission rejected: form token",
			"reason", err.Error(), "ip", clientIP(r))
		s.refuseAnonymousSubmission(w, r)
		return false
	}

	return true
}

// refuseAnonymousSubmission is the single, uniform refusal.
//
// The copy has to do two jobs at once: give a real resident who hit this by
// accident — a stale tab, a double submission, a browser that dropped the
// token — something they can act on, without telling a script anything about
// which check it failed. "Start again" is the honest answer to all of them.
func (s *Server) refuseAnonymousSubmission(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusBadRequest, "submission_refused",
		"We could not accept that report. Please open the form again and re-enter it — "+
			"a form left open for a long time has to be started afresh.")
}

// handlePortalCreate files a report, with or without a session.
//
// Signed in, it is attributed to that resident and they get confirmations,
// updates and tracking. Signed out, it is filed anonymously and they get a
// reference and nothing else — which the response says plainly via `trackable`.
func (s *Server) handlePortalCreate(w http.ResponseWriter, r *http.Request) {
	var body portalCreateBody
	if !decode(w, r, &body) {
		return
	}

	in := portal.CreateInput{
		ServiceTypeID: body.ServiceTypeID, Subject: body.Subject,
		Description: body.Description, Address1: body.Address1, City: body.City,
		PostalCode: body.PostalCode, Ward: body.Ward, FormData: body.FormData,
	}

	var (
		view *portal.MyRequest
		err  error
	)
	// The session decides the channel, never the request body. A client that
	// could ask to be treated as signed-in would be choosing whose name goes on
	// the report.
	if contact := s.optionalCitizen(r); contact != nil {
		view, err = s.Portal.Create(r.Context(), contact, in)
	} else {
		if !s.allowAnonymousSubmission(w, r, body) {
			return
		}
		view, err = s.Portal.CreateAnonymous(r.Context(), in)
	}
	if err != nil {
		failPortal(w, r, err)
		return
	}

	// The upload grant rides on the create response and nowhere else. It must
	// never appear on a read — a tracking lookup returns the same projection,
	// and handing an upload credential to anyone who can quote a reference
	// would undo the point of having one.
	filed, err := s.Requests.GetByReference(r.Context(), view.Reference)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		*portal.MyRequest
		UploadGrant string `json:"uploadGrant"`
	}{
		MyRequest:   view,
		UploadGrant: s.forms.issueUpload(filed.ID, time.Now()),
	})
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
