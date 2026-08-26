package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// newJarClient returns a client with its own cookie jar, so a portal session
// and a staff session in the same test cannot borrow each other's cookies.
func newJarClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

// doJSON performs a request with an explicit client and returns the status.
func doJSON(t *testing.T, client *http.Client, method, url string, body, out any) int {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode
}

// lookupByReference reads a request from the staff surface, to confirm a
// portal submission became a real, routed request.
func (e *env) lookupByReference(reference string) (*domain.Request, error) {
	var req domain.Request
	err := e.db.Where("reference = ?", reference).First(&req).Error
	return &req, err
}

// rawGet returns the response body verbatim alongside the status, for
// assertions that two refusals are byte-identical.
func rawGet(t *testing.T, client *http.Client, url string) (string, int) {
	t.Helper()

	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body), resp.StatusCode
}

// catalogID resolves a service-type code to the id the portal's create
// endpoint expects, through the public catalogue a citizen would actually use.
func catalogID(t *testing.T, e *env, client *http.Client, code string) string {
	t.Helper()

	var catalog struct {
		Items []struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"items"`
	}
	if got := doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/catalog", nil, &catalog); got != 200 {
		t.Fatalf("catalog -> %d", got)
	}
	for _, c := range catalog.Items {
		if c.Code == code {
			return c.ID
		}
	}
	t.Fatalf("%s is not in the public catalogue", code)
	return ""
}

// scrubProblem removes the fields of a problem document that differ between
// any two requests — the echoed path and the correlation id — so the rest can
// be compared for an information leak.
func scrubProblem(t *testing.T, body string) string {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("decode problem %q: %v", body, err)
	}
	delete(doc, "instance")
	delete(doc, "requestId")

	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	return string(out)
}
