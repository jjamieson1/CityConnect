package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// submitAnonymously posts a report body and returns the status and the parsed
// problem/response, so refusals can be compared to one another.
func submitAnonymously(t *testing.T, e *env, body map[string]any) (int, map[string]any) {
	t.Helper()

	full := map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       "A report",
		"address1":      "44 Elm Street",
		"formData":      map[string]any{"size": "Medium"},
	}
	for k, v := range body {
		full[k] = v
	}
	var out map[string]any
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests", full, &out)
	return code, out
}

// Without a token there is no evidence a form was ever opened, which is the
// whole basis of the control.
func TestAnonymousSubmissionNeedsAFormToken(t *testing.T) {
	e := newEnv(t)

	code, _ := submitAnonymously(t, e, map[string]any{})
	if code != http.StatusBadRequest {
		t.Errorf("submission with no form token -> %d, want 400", code)
	}
}

// One token, one report. A flood has to pay for a token per attempt, and
// issuance is where the ceiling is.
func TestFormTokenCannotBeReusedAcrossSubmissions(t *testing.T) {
	e := newEnv(t)
	token := formToken(t, e)

	if code, _ := submitAnonymously(t, e, map[string]any{"formToken": token}); code != http.StatusCreated {
		t.Fatalf("first use -> %d, want 201", code)
	}
	if code, _ := submitAnonymously(t, e, map[string]any{"formToken": token}); code != http.StatusBadRequest {
		t.Errorf("second use of the same token -> %d, want 400", code)
	}
}

// The honeypot is hidden from sight, from the keyboard and from assistive
// technology, so nothing a resident does can fill it.
func TestHoneypotRejectsFilledTraps(t *testing.T) {
	e := newEnv(t)

	code, _ := submitAnonymously(t, e, map[string]any{
		"formToken":  formToken(t, e),
		"websiteUrl": "http://spam.example",
	})
	if code != http.StatusBadRequest {
		t.Errorf("submission with the honeypot filled -> %d, want 400", code)
	}
}

// Telling a caller which check stopped them is telling them what to change.
func TestSubmissionRefusalsAreIndistinguishable(t *testing.T) {
	e := newEnv(t)

	noToken, missing := submitAnonymously(t, e, map[string]any{})
	badToken, forged := submitAnonymously(t, e, map[string]any{
		"formToken": "AAAA.1700000000.notasignature",
	})
	honeypot, trapped := submitAnonymously(t, e, map[string]any{
		"formToken": formToken(t, e), "websiteUrl": "http://spam.example",
	})

	if noToken != badToken || badToken != honeypot {
		t.Errorf("statuses differ: none=%d forged=%d honeypot=%d", noToken, badToken, honeypot)
	}
	if missing["detail"] != forged["detail"] || forged["detail"] != trapped["detail"] {
		t.Errorf("refusal messages differ:\n  none:     %v\n  forged:   %v\n  honeypot: %v",
			missing["detail"], forged["detail"], trapped["detail"])
	}
	// And the message has to be useful to the resident who hit it by accident.
	if detail, _ := missing["detail"].(string); detail == "" {
		t.Error("the refusal gives a resident nothing to act on")
	}
}

// A scripted flood is refused. The demo shows exactly this.
func TestAnonymousSubmissionIsRateLimited(t *testing.T) {
	e := newEnv(t)
	serviceType := publicServiceType(t, e, "POTHOLE")

	var throttled bool
	for i := 0; i < 40; i++ {
		var out map[string]any
		code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests",
			map[string]any{
				"serviceTypeId": serviceType,
				"subject":       "Flood",
				"address1":      "1 Flood Street",
				"formData":      map[string]any{"size": "Medium"},
				"formToken":     "AAAA.1700000000.notasignature",
			}, &out)
		if code == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Error("40 rejected submissions in a row were never throttled")
	}
}

// Issuance is the real ceiling: however fast a script posts, it cannot report
// faster than it is handed tokens.
func TestFormTokenIssuanceIsRateLimited(t *testing.T) {
	e := newEnv(t)

	var throttled bool
	for i := 0; i < 40; i++ {
		code := doJSON(t, newJarClient(), http.MethodGet, e.api.URL+"/api/portal/form-token", nil, nil)
		if code == http.StatusTooManyRequests {
			throttled = true
			break
		}
		if code != http.StatusOK {
			t.Fatalf("form-token attempt %d -> %d", i, code)
		}
	}
	if !throttled {
		t.Error("token issuance was never throttled; the sustained ceiling does not exist")
	}
}

// A signed-in resident has an account behind them and must not be made to
// fetch a token — the control exists for callers with no other identity.
func TestSignedInSubmissionNeedsNoFormToken(t *testing.T) {
	e := newEnv(t)
	client := e.portalSignIn(t, "citizen-no-token")

	var created map[string]any
	code := doJSON(t, client, http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       "Filed while signed in",
		"address1":      "9 Oak Street",
		"formData":      map[string]any{"size": "Medium"},
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("signed-in submission -> %d, want 201", code)
	}
	if trackable, _ := created["trackable"].(bool); !trackable {
		t.Error("a signed-in report should still be trackable")
	}
}

// "Double-clicking submit creates one request." The idempotency middleware
// already existed; this proves the public path actually reaches it.
func TestRepeatedSubmitWithOneIdempotencyKeyFilesOneRequest(t *testing.T) {
	e := newEnv(t)

	body := map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       "Impatient double click",
		"address1":      "12 Impatient Way",
		"formData":      map[string]any{"size": "Medium"},
		"formToken":     formToken(t, e),
	}

	client := newJarClient()
	for attempt := 0; attempt < 2; attempt++ {
		code := doJSONWithHeaders(t, client, http.MethodPost,
			e.api.URL+"/api/portal/requests",
			map[string]string{"Idempotency-Key": "double-click-1"}, body, nil)
		if code != http.StatusCreated {
			t.Fatalf("attempt %d -> %d, want 201", attempt, code)
		}
	}

	var filed int64
	if err := e.db.Model(&domain.Request{}).
		Where("subject = ?", "Impatient double click").Count(&filed).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if filed != 1 {
		t.Errorf("%d requests filed from one double click, want 1", filed)
	}
}
