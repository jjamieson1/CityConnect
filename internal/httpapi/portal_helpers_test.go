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
