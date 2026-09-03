package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

// trackURL is the public endpoint under test: no session, no cookie, nothing
// but a reference and a contact detail.
func trackURL(base string) string { return base + "/api/portal/requests/track" }

// fileRequest signs a citizen in, files a report, and returns its reference
// along with the email address the report was filed under.
func fileRequest(t *testing.T, e *env, sub string) (reference, email string) {
	t.Helper()

	client := e.portalSignIn(t, sub)

	var catalog struct {
		Items []struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"items"`
	}
	if code := doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/catalog", nil, &catalog); code != 200 {
		t.Fatalf("catalog -> %d", code)
	}
	var serviceType string
	for _, c := range catalog.Items {
		if c.Code == "POTHOLE" {
			serviceType = c.ID
		}
	}
	if serviceType == "" {
		t.Fatal("POTHOLE is not in the public catalogue")
	}

	var created struct {
		Reference string `json:"reference"`
	}
	code := doJSON(t, client, http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": serviceType,
		"subject":       "Pothole outside my house",
		"description":   "Deep and getting worse.",
		"address1":      "44 Elm Street",
		"formData":      map[string]any{"size": "Medium"},
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}
	return created.Reference, sub + "@example.gov"
}

// The whole point of the story: somebody with no account gets their status
// back from a reference plus the contact detail they filed under.
func TestPublicTrackingResolvesWithReferenceAndEmail(t *testing.T) {
	e := newEnv(t)
	reference, email := fileRequest(t, e, "citizen-tracker")

	// A fresh client with an empty cookie jar — no portal session at all.
	anonymous := newJarClient()

	var view map[string]any
	code := doJSON(t, anonymous, http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   reference,
		"verificationValue": email,
	}, &view)
	if code != http.StatusOK {
		t.Fatalf("track -> %d, want 200", code)
	}
	if view["reference"] != reference {
		t.Errorf("reference = %v, want %q", view["reference"], reference)
	}
	if view["statusLabel"] == nil || view["statusLabel"] == "" {
		t.Error("no status was returned; tracking exists to answer exactly that")
	}
}

// The projection is the security boundary. Internal bookkeeping reaching an
// unauthenticated caller is the failure this endpoint most needs to not have.
func TestPublicTrackingLeaksNoInternalFields(t *testing.T) {
	e := newEnv(t)
	reference, email := fileRequest(t, e, "citizen-private")

	var view map[string]any
	code := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   reference,
		"verificationValue": email,
	}, &view)
	if code != http.StatusOK {
		t.Fatalf("track -> %d", code)
	}

	for _, leaked := range []string{
		"queueId", "queue", "assigneeUserId", "assigneeUser", "assigneeSystemId",
		"departmentId", "slaPolicyId", "slaBreached", "slaWarned", "priority",
		"tags", "contactId", "internalNotes", "version", "responseDueAt",
		"pausedMinutes", "reopenCount", "mergedIntoId", "source",
	} {
		if _, present := view[leaked]; present {
			t.Errorf("citizen view carries internal field %q", leaked)
		}
	}
}

// An oracle that distinguishes "no such reference" from "wrong answer" tells a
// caller which references are real, which is the enumeration the random
// reference format exists to prevent. The two must be indistinguishable.
func TestPublicTrackingFailsIdentically(t *testing.T) {
	e := newEnv(t)
	reference, email := fileRequest(t, e, "citizen-uniform")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"real reference, wrong email", map[string]any{
			"referenceNumber": reference, "verificationValue": "someone.else@example.gov"}},
		{"reference that does not exist", map[string]any{
			"referenceNumber": "SR-ZZZZ-ZZZZ", "verificationValue": email}},
		{"neither is real", map[string]any{
			"referenceNumber": "SR-YYYY-YYYY", "verificationValue": "nobody@example.gov"}},
		{"empty verification", map[string]any{
			"referenceNumber": reference, "verificationValue": ""}},
	}

	var first map[string]any
	for i, c := range cases {
		var body map[string]any
		code := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), c.body, &body)
		if code != http.StatusNotFound {
			t.Fatalf("%s -> %d, want 404", c.name, code)
		}
		if i == 0 {
			first = body
			continue
		}
		if body["detail"] != first["detail"] || body["title"] != first["title"] {
			t.Errorf("%s returned a distinguishable failure:\n got %v\nwant %v",
				c.name, body, first)
		}
	}
}

// The fold that lets a resident mistype O for 0 must not also let them reach
// somebody else's report — it rescues the reference, never the verification.
func TestPublicTrackingToleratesAMistypedReference(t *testing.T) {
	e := newEnv(t)
	reference, email := fileRequest(t, e, "citizen-typo")

	// Lower case throughout, zeros typed as the letter O and ones as the letter
	// L — the substitutions a resident actually makes reading a reference off a
	// screen or hearing it over the phone.
	mistyped := strings.ToLower(reference)
	mistyped = strings.ReplaceAll(mistyped, "0", "o")
	mistyped = strings.ReplaceAll(mistyped, "1", "l")

	// References are random, so a given one may contain no foldable digit. Say
	// which property this run actually exercised rather than quietly asserting
	// less than the name promises.
	if mistyped == strings.ToLower(reference) {
		t.Logf("reference %q has no 0 or 1; this run exercises case folding only", reference)
	}

	var view map[string]any
	code := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   mistyped,
		"verificationValue": strings.ToUpper(email), // and the email shouted
	}, &view)
	if code != http.StatusOK {
		t.Fatalf("track with a mistyped reference -> %d, want 200 (sent %q for %q)",
			code, mistyped, reference)
	}
	if view["reference"] != reference {
		t.Errorf("reference = %v, want %q", view["reference"], reference)
	}
}

// The fold is a courtesy on the reference, never on the secret. An email that
// differs only by a folded character is still the wrong email.
func TestPublicTrackingDoesNotFoldTheVerificationValue(t *testing.T) {
	e := newEnv(t)
	reference, email := fileRequest(t, e, "citizen-l0")

	// citizen-l0@example.gov with the zero typed as an O. If the fold were
	// applied to the verification value too, this would wrongly succeed.
	folded := strings.ReplaceAll(email, "0", "o")
	if folded == email {
		t.Fatalf("fixture email %q has no zero to fold; the test proves nothing", email)
	}

	code := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   reference,
		"verificationValue": folded,
	}, nil)
	if code != http.StatusNotFound {
		t.Errorf("track with %q -> %d, want 404: the fold must not reach the secret",
			folded, code)
	}
}

// The reference's entropy is sized against a throttled endpoint, so the
// throttle is part of the security argument rather than capacity management.
func TestPublicTrackingIsRateLimited(t *testing.T) {
	e := newEnv(t)
	reference, _ := fileRequest(t, e, "citizen-flood")

	client := newJarClient()
	var throttled bool
	for i := 0; i < 20; i++ {
		code := doJSON(t, client, http.MethodPost, trackURL(e.api.URL), map[string]any{
			"referenceNumber":   reference,
			"verificationValue": "guess@example.gov",
		}, nil)
		if code == http.StatusTooManyRequests {
			throttled = true
			break
		}
		if code != http.StatusNotFound {
			t.Fatalf("attempt %d -> %d, want 404 or 429", i, code)
		}
	}
	if !throttled {
		t.Error("20 wrong answers in a row were never throttled; " +
			"the reference format's entropy assumes this limit exists")
	}
}

// Tracking must not become a second way into the authenticated surface: it is
// a read of one request, and a session it does not have cannot be inferred.
func TestPublicTrackingNeedsNoSessionAndGrantsNone(t *testing.T) {
	e := newEnv(t)
	reference, email := fileRequest(t, e, "citizen-nosession")

	anonymous := newJarClient()
	if code := doJSON(t, anonymous, http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   reference,
		"verificationValue": email,
	}, nil); code != http.StatusOK {
		t.Fatalf("track -> %d", code)
	}

	// Having tracked, the same client still has no portal session.
	if code := doJSON(t, anonymous, http.MethodGet, e.api.URL+"/api/portal/me", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("GET /portal/me after tracking -> %d, want 401", code)
	}
	if code := doJSON(t, anonymous, http.MethodGet, e.api.URL+"/api/portal/requests", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("GET /portal/requests after tracking -> %d, want 401", code)
	}
}
