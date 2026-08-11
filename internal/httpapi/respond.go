// Package httpapi is the HTTP surface: the chi router, its middleware, and
// the handlers that decode requests, call services, and map service errors to
// status codes.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/interactions"
	"github.com/jjamieson1/CityConnect/internal/notifications"
	"github.com/jjamieson1/CityConnect/internal/reports"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/routing"
	"github.com/jjamieson1/CityConnect/internal/store"
	"github.com/jjamieson1/CityConnect/internal/webhooks"
)

// maxBodyBytes bounds decoded request bodies. Uploads use their own limit.
const maxBodyBytes = 2 << 20

// Problem is an RFC 7807 error body.
type Problem struct {
	Type      string `json:"type,omitempty"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	// Code is a stable machine-readable slug the SPA switches on, so client
	// behaviour never depends on parsing prose.
	Code string `json:"code,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("could not encode response", "error", err)
	}
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// writeProblem renders an error as problem+json.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	p := Problem{
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    detail,
		Code:      code,
		Instance:  r.URL.Path,
		RequestID: RequestIDFrom(r.Context()),
	}
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// fail maps a service error onto an HTTP response.
//
// Every service package exports sentinels rather than HTTP knowledge, so this
// is the single place that decides status codes. A new service error that is
// not listed here becomes a 500, which is the right default: an unclassified
// failure is a bug, not a client mistake.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		return

	// Not found
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, agents.ErrNotFound),
		errors.Is(err, contacts.ErrNotFound),
		errors.Is(err, requests.ErrNotFound),
		errors.Is(err, catalog.ErrNotFound),
		errors.Is(err, routing.ErrNotFound),
		errors.Is(err, interactions.ErrNotFound),
		errors.Is(err, notifications.ErrNotFound),
		errors.Is(err, webhooks.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", "The requested resource does not exist.")

	// Authentication
	case errors.Is(err, agents.ErrNoSession):
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
	case errors.Is(err, agents.ErrNoAccess):
		writeProblem(w, r, http.StatusForbidden, "no_access",
			"This identity has no CityConnect access. Ask an administrator for an invitation.")
	case errors.Is(err, agents.ErrSuspended):
		writeProblem(w, r, http.StatusForbidden, "suspended", "This account is suspended.")
	case errors.Is(err, agents.ErrForbidden):
		writeProblem(w, r, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, requests.ErrForbidden):
		writeProblem(w, r, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, agents.ErrInvalidState):
		writeProblem(w, r, http.StatusBadRequest, "invalid_login_state",
			"That sign-in attempt has expired. Please try again.")

	// Concurrency — a distinct code so the SPA can offer a reload rather than
	// a generic error.
	case errors.Is(err, contacts.ErrStale), errors.Is(err, requests.ErrStale):
		writeProblem(w, r, http.StatusConflict, "stale_version",
			"Somebody else changed this record while you were editing it. Reload and try again.")

	// Workflow
	case errors.Is(err, requests.ErrBadTransition):
		writeProblem(w, r, http.StatusConflict, "bad_transition", err.Error())
	case errors.Is(err, requests.ErrAlreadyMerged):
		writeProblem(w, r, http.StatusConflict, "already_merged", err.Error())
	case errors.Is(err, agents.ErrLastAdmin):
		writeProblem(w, r, http.StatusConflict, "last_admin",
			"This is the last active administrator; promote somebody else first.")

	// Conflicts
	case errors.Is(err, store.ErrDuplicate):
		writeProblem(w, r, http.StatusConflict, "duplicate", "That record already exists.")
	case errors.Is(err, agents.ErrConflict),
		errors.Is(err, contacts.ErrConflict),
		errors.Is(err, catalog.ErrConflict),
		errors.Is(err, routing.ErrConflict),
		errors.Is(err, requests.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error())

	// Suppression is a legitimate outcome, not a failure — it carries its own
	// code so the console can explain the consent gate rather than show an error.
	case errors.Is(err, notifications.ErrSuppressed):
		writeProblem(w, r, http.StatusUnprocessableEntity, "suppressed", err.Error())

	// Validation
	case errors.Is(err, agents.ErrInvalidInput),
		errors.Is(err, contacts.ErrInvalidInput),
		errors.Is(err, requests.ErrInvalidInput),
		errors.Is(err, catalog.ErrInvalidInput),
		errors.Is(err, routing.ErrInvalidInput),
		errors.Is(err, interactions.ErrInvalidInput),
		errors.Is(err, notifications.ErrInvalidInput),
		errors.Is(err, reports.ErrInvalidInput),
		errors.Is(err, requests.ErrSelfLink):
		writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())

	default:
		slog.ErrorContext(r.Context(), "unhandled service error",
			"path", r.URL.Path, "error", err, "request_id", RequestIDFrom(r.Context()))
		writeProblem(w, r, http.StatusInternalServerError, "internal",
			"Something went wrong. The error has been logged.")
	}
}

// decode reads a JSON body, rejecting unknown fields so a client typo in a
// field name surfaces immediately instead of being silently ignored.
func decode(w http.ResponseWriter, r *http.Request, dest any) bool {
	defer r.Body.Close()

	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dest); err != nil {
		detail := "The request body is not valid JSON."
		var syntax *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntax):
			detail = "Malformed JSON at position " + strconv.FormatInt(syntax.Offset, 10) + "."
		case errors.As(err, &typeErr):
			detail = "Field " + typeErr.Field + " has the wrong type."
		case errors.Is(err, io.EOF):
			detail = "A request body is required."
		case strings.Contains(err.Error(), "unknown field"):
			detail = strings.TrimPrefix(err.Error(), "json: ")
		}
		writeProblem(w, r, http.StatusBadRequest, "invalid_json", detail)
		return false
	}
	return true
}

// pageFrom reads pagination and sorting from the query string.
func pageFrom(r *http.Request) store.Page {
	q := r.URL.Query()
	p := store.Page{SortBy: q.Get("sort")}

	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		p.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil {
		p.Offset = v
	}
	// Page numbers are one-based in the UI and convert to an offset here, so
	// the client never has to do the arithmetic.
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 1 {
		limit := p.Limit
		if limit <= 0 {
			limit = store.DefaultLimit
		}
		p.Offset = (v - 1) * limit
	}
	p.Desc = q.Get("dir") != "asc"
	return p
}

func queryBool(r *http.Request, key string) bool {
	v, err := strconv.ParseBool(r.URL.Query().Get(key))
	return err == nil && v
}

// queryBoolPtr distinguishes "not supplied" from "supplied as false", which
// matters for tri-state filters.
func queryBoolPtr(r *http.Request, key string) *bool {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &v
}

func queryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

func queryTime(r *http.Request, key string) *time.Time {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}

func queryList(r *http.Request, key string) []string {
	raw := r.URL.Query().Get(key)
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

func queryUint(r *http.Request, key string) uint {
	if v, err := strconv.ParseUint(r.URL.Query().Get(key), 10, 32); err == nil {
		return uint(v)
	}
	return 0
}

// listing wraps a slice as {"items": …}, guaranteeing an empty array rather
// than null.
//
// A nil slice in Go marshals to JSON `null`, so every list endpoint returned
// null before it had any data. Clients that iterate the field then throw at
// exactly the moment there is nothing to show — a fresh deployment — which is
// the worst possible time for a screen to break.
func listing[T any](items []T) map[string]any {
	if items == nil {
		items = []T{}
	}
	return map[string]any{"items": items}
}
