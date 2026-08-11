package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// collectionEndpoints is every GET that returns a collection, or a report
// containing them.
//
// Adding a new one here is the point: the list is the contract, and a reviewer
// who adds an endpoint without adding it here is the failure mode this guards
// against. Keep it exhaustive.
var collectionEndpoints = []string{
	// Work
	"/api/requests",
	"/api/interactions",

	// People and organisations
	"/api/contacts",
	"/api/contact-groups",
	"/api/users",
	"/api/departments",
	"/api/connected-systems",
	"/api/tokens",

	// Catalogue and routing
	"/api/service-types",
	"/api/sla-policies",
	"/api/business-calendars",
	"/api/macros",
	"/api/notification-templates",
	"/api/queues",
	"/api/routing-rules",

	// Communications
	"/api/notifications",
	"/api/webhooks",

	// Reporting and operations
	"/api/reports/volume",
	"/api/reports/sla",
	"/api/reports/agents",
	"/api/reports/csat",
	"/api/reports/geo",
	"/api/audit",
	"/api/jobs",
	"/api/saved-views",
	"/api/search?q=nothing-matches-this",
}

// TestEmptyStateReturnsNoNulls is the structural guard against the crash that
// took the console down on a fresh install.
//
// A nil Go slice marshals to JSON `null`. Every client then has to guard each
// list before iterating it, and the one that forgets throws at exactly the
// moment there is no data — a brand-new deployment, the first time anyone
// opens the screen. An empty collection is `[]`.
//
// The database here is deliberately unseeded, because that is the state no
// other test covers and the only one that reproduces the bug.
func TestEmptyStateReturnsNoNulls(t *testing.T) {
	e := newEnvWith(t, false)
	e.signIn("staff-admin", "admin@city.example", domain.RoleAdmin)

	for _, path := range collectionEndpoints {
		t.Run(strings.TrimPrefix(path, "/api/"), func(t *testing.T) {
			body, status := e.raw(t, path)
			if status != http.StatusOK {
				t.Fatalf("GET %s returned %d: %s", path, status, body)
			}

			var decoded any
			if err := json.Unmarshal([]byte(body), &decoded); err != nil {
				t.Fatalf("GET %s returned unparseable JSON: %v", path, err)
			}

			if nulls := findNulls(decoded, "$"); len(nulls) > 0 {
				sort.Strings(nulls)
				t.Errorf("GET %s returned null at %s\n"+
					"A nil slice or map marshals to null; clients iterating it crash on an "+
					"empty database. Initialise the collection before returning it "+
					"(reports.nonNil, httpapi.listing, or store.Paginate).\n"+
					"body: %s",
					path, strings.Join(nulls, ", "), truncateBody(body))
			}
		})
	}
}

// TestEmptyStateListingsAreArrays checks the shape as well as the absence of
// nulls: an endpoint that answers `{"items": {}}` would pass the null check
// and still break every caller that iterates.
func TestEmptyStateListingsAreArrays(t *testing.T) {
	e := newEnvWith(t, false)
	e.signIn("staff-admin", "admin@city.example", domain.RoleAdmin)

	for _, path := range collectionEndpoints {
		body, status := e.raw(t, path)
		if status != http.StatusOK {
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			continue // a report may be an object of scalars; covered above
		}

		items, present := payload["items"]
		if !present {
			continue
		}
		if _, ok := items.([]any); !ok {
			t.Errorf("GET %s: items is %T, want an array", path, items)
		}
	}
}

// TestSeededStateReturnsNoNulls runs the same sweep with the baseline
// configuration present, so a collection that is only nil once populated —
// an association left unloaded, say — is caught too.
func TestSeededStateReturnsNoNulls(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-admin", "admin@city.example", domain.RoleAdmin)
	e.seedRequest(t)

	for _, path := range collectionEndpoints {
		body, status := e.raw(t, path)
		if status != http.StatusOK {
			t.Errorf("GET %s returned %d", path, status)
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Errorf("GET %s: %v", path, err)
			continue
		}
		if nulls := findNulls(decoded, "$"); len(nulls) > 0 {
			sort.Strings(nulls)
			t.Errorf("GET %s returned null at %s", path, strings.Join(nulls, ", "))
		}
	}
}

// findNulls walks a decoded JSON value and returns the path of every null.
//
// It reports *all* of them rather than the first, so one run tells you
// everything to fix instead of one round trip per field.
func findNulls(v any, path string) []string {
	switch typed := v.(type) {
	case nil:
		return []string{path}
	case map[string]any:
		var found []string
		for key, value := range typed {
			found = append(found, findNulls(value, path+"."+key)...)
		}
		return found
	case []any:
		var found []string
		for i, value := range typed {
			found = append(found, findNulls(value, fmt.Sprintf("%s[%d]", path, i))...)
		}
		return found
	}
	return nil
}

// raw performs a GET and returns the body and status without decoding, so a
// null survives to be inspected rather than being smoothed away by a typed
// unmarshal.
func (e *env) raw(t *testing.T, path string) (string, int) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, e.api.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

func truncateBody(s string) string {
	if len(s) <= 400 {
		return s
	}
	return s[:400] + "…"
}
