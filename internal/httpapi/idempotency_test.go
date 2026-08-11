package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// postWithKey sends a request carrying an Idempotency-Key.
func (e *env) postWithKey(t *testing.T, path, key string, body any, out any) (int, http.Header) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, e.api.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, resp.Header
}

// newRequestBody builds a valid create-request payload.
func (e *env) newRequestBody(t *testing.T) map[string]any {
	t.Helper()

	var types struct {
		Items []domain.ServiceType `json:"items"`
	}
	e.get("/api/service-types", &types)
	var pothole string
	for _, st := range types.Items {
		if st.Code == "POTHOLE" {
			pothole = st.ID
		}
	}

	var contact domain.Contact
	e.post("/api/contacts", map[string]any{
		"displayName": "Idempotency Tester", "primaryEmail": "idem@example.gov",
	}, &contact)

	return map[string]any{
		"contactId": contact.ID, "serviceTypeId": pothole,
		"subject": "Pothole on Oak Street", "address1": "12 Oak Street",
		"formData": map[string]any{"size": "Medium"},
	}
}

// TestIdempotentRetryCreatesOneRequest is the whole point: a connected system
// that retries after a timeout must not file a second service request, because
// a duplicate request is a second crew dispatched to a pothole that has already
// been filled.
func TestIdempotentRetryCreatesOneRequest(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	body := e.newRequestBody(t)
	const key = "partner-retry-0001"

	var first domain.Request
	status, headers := e.postWithKey(t, "/api/requests", key, body, &first)
	if status != http.StatusCreated {
		t.Fatalf("first call -> %d", status)
	}
	if headers.Get("Idempotent-Replay") != "" {
		t.Error("the first call should not be marked as a replay")
	}

	var second domain.Request
	status, headers = e.postWithKey(t, "/api/requests", key, body, &second)
	if status != http.StatusCreated {
		t.Fatalf("retry -> %d, want the original 201", status)
	}
	if headers.Get("Idempotent-Replay") != "true" {
		t.Error("the retry was not flagged as a replay")
	}
	if second.Reference != first.Reference {
		t.Errorf("retry produced %s, want the original %s", second.Reference, first.Reference)
	}

	var n int64
	e.db.Model(&domain.Request{}).Where("subject = ?", "Pothole on Oak Street").Count(&n)
	if n != 1 {
		t.Errorf("%d requests were created, want exactly 1", n)
	}
}

// TestIdempotencyKeyReuseWithDifferentBodyIsRejected catches a client bug
// rather than hiding it: replaying the first result would silently discard the
// second request.
func TestIdempotencyKeyReuseWithDifferentBodyIsRejected(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	body := e.newRequestBody(t)
	const key = "partner-reused-key"

	if status, _ := e.postWithKey(t, "/api/requests", key, body, nil); status != http.StatusCreated {
		t.Fatalf("first call -> %d", status)
	}

	body["subject"] = "A completely different problem"
	status, _ := e.postWithKey(t, "/api/requests", key, body, nil)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("reusing a key with a new body -> %d, want 422", status)
	}

	var n int64
	e.db.Model(&domain.Request{}).Where("subject = ?", "A completely different problem").Count(&n)
	if n != 0 {
		t.Error("the second, different request was created despite the reused key")
	}
}

// TestIdempotencyIsScopedPerCaller stops one client replaying another's
// response by guessing a key.
func TestIdempotencyIsScopedPerCaller(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-one", "one@city.example", domain.RoleAdmin)

	body := e.newRequestBody(t)
	const key = "a-guessable-key"

	var first domain.Request
	if status, _ := e.postWithKey(t, "/api/requests", key, body, &first); status != http.StatusCreated {
		t.Fatal("first call failed")
	}

	// A different member of staff, same key, same payload.
	e.signIn("staff-two", "two@city.example", domain.RoleAdmin)

	var second domain.Request
	status, headers := e.postWithKey(t, "/api/requests", key, body, &second)
	if status != http.StatusCreated {
		t.Fatalf("second caller -> %d", status)
	}
	if headers.Get("Idempotent-Replay") == "true" {
		t.Error("one caller replayed another caller's response")
	}
	if second.Reference == first.Reference {
		t.Error("the second caller received the first caller's request")
	}
}

// TestFailedRequestDoesNotConsumeKey lets a caller fix a bad payload and retry
// with the same key, rather than being answered with the original failure
// forever.
func TestFailedRequestDoesNotConsumeKey(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	const key = "fix-and-retry"

	// Missing contactId: rejected.
	status, _ := e.postWithKey(t, "/api/requests", key, map[string]any{"subject": "No contact"}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid payload -> %d, want 400", status)
	}

	// Corrected, same key: should now succeed rather than replay the failure.
	body := e.newRequestBody(t)
	var created domain.Request
	status, _ = e.postWithKey(t, "/api/requests", key, body, &created)
	if status != http.StatusCreated {
		t.Fatalf("corrected retry -> %d, want 201", status)
	}
	if created.Reference == "" {
		t.Error("the corrected retry did not create a request")
	}
}

// TestConcurrentRetriesCreateOneRequest covers the race the reservation exists
// for: two deliveries arriving together would both pass a read-then-write check
// and both execute.
func TestConcurrentRetriesCreateOneRequest(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	body := e.newRequestBody(t)
	const key = "simultaneous-delivery"

	var wg sync.WaitGroup
	statuses := make([]int, 4)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], _ = e.postWithKey(t, "/api/requests", key, body, nil)
		}(i)
	}
	wg.Wait()

	created, conflicts := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created+conflicts != len(statuses) {
		t.Errorf("unexpected statuses: %v", statuses)
	}

	// Whatever the mix of replays and in-progress conflicts, the work happened
	// once.
	var n int64
	e.db.Model(&domain.Request{}).Where("subject = ?", "Pothole on Oak Street").Count(&n)
	if n != 1 {
		t.Errorf("%d requests created by concurrent retries, want exactly 1 (statuses %v)", n, statuses)
	}
}

// TestWithoutKeyBehavesNormally checks the header stays optional: a caller that
// does not send one is unaffected, including being able to file twice on purpose.
func TestWithoutKeyBehavesNormally(t *testing.T) {
	e := newEnv(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	body := e.newRequestBody(t)
	for i := range 2 {
		if status, _ := e.postWithKey(t, "/api/requests", "", body, nil); status != http.StatusCreated {
			t.Fatalf("call %d -> %d", i, status)
		}
	}

	var n int64
	e.db.Model(&domain.Request{}).Where("subject = ?", "Pothole on Oak Street").Count(&n)
	if n != 2 {
		t.Errorf("%d requests without an idempotency key, want 2", n)
	}
}
