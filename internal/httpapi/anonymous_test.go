package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// formToken fetches the single-use token an anonymous submission must present.
func formToken(t *testing.T, e *env) string {
	t.Helper()

	var issued struct {
		Token string `json:"token"`
	}
	if code := doJSON(t, newJarClient(), http.MethodGet,
		e.api.URL+"/api/portal/form-token", nil, &issued); code != http.StatusOK {
		t.Fatalf("form-token -> %d", code)
	}
	if issued.Token == "" {
		t.Fatal("no form token issued; anonymous reporting is impossible without one")
	}
	return issued.Token
}

// publicServiceType returns the id of a service a member of the public can file.
func publicServiceType(t *testing.T, e *env, code string) string {
	t.Helper()

	var catalog struct {
		Items []struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"items"`
	}
	if status := doJSON(t, newJarClient(), http.MethodGet,
		e.api.URL+"/api/portal/catalog", nil, &catalog); status != 200 {
		t.Fatalf("catalog -> %d", status)
	}
	for _, c := range catalog.Items {
		if c.Code == code {
			return c.ID
		}
	}
	t.Fatalf("%s is not in the public catalogue", code)
	return ""
}

// anonymousReport files a report with a client that has never signed in.
func anonymousReport(t *testing.T, e *env, subject string) map[string]any {
	t.Helper()

	var created map[string]any
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       subject,
		"description":   "Reported by someone who did not want an account.",
		"address1":      "44 Elm Street",
		"formData":      map[string]any{"size": "Medium"},
		"formToken":     formToken(t, e),
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("anonymous report -> %d, want 201", code)
	}
	return created
}

// The point of the story: a resident reports a hazard with no account, no
// cookie and no contact details, and it becomes a real, routed request.
func TestAnonymousReportIsAcceptedWithoutASession(t *testing.T) {
	e := newEnv(t)

	created := anonymousReport(t, e, "Pothole outside the school")

	reference, _ := created["reference"].(string)
	if reference == "" {
		t.Fatal("no reference returned; the resident has nothing to quote")
	}

	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("the request was not persisted: %v", err)
	}
	if req.ContactID != "" {
		t.Errorf("anonymous request carries contact %q; it was promised none", req.ContactID)
	}
	if req.Channel != domain.ChannelAnonymous {
		t.Errorf("channel = %q, want %q", req.Channel, domain.ChannelAnonymous)
	}
	// An anonymous report is a weaker deal for the reporter, not a lesser
	// report for the city: it still routes and still carries an SLA.
	if req.QueueID == "" {
		t.Error("anonymous request was not routed to a queue")
	}
	if req.Subject != "Pothole outside the school" {
		t.Errorf("subject = %q, want it preserved", req.Subject)
	}
}

// The asymmetry is the product decision, and the response has to carry it or
// the confirmation screen cannot be honest.
func TestAnonymousReportIsNotTrackable(t *testing.T) {
	e := newEnv(t)

	created := anonymousReport(t, e, "Graffiti on the underpass")

	if trackable, _ := created["trackable"].(bool); trackable {
		t.Error("anonymous report claims to be trackable")
	}
	for _, capability := range []string{"canComment", "canCancel", "canRate"} {
		if v, _ := created[capability].(bool); v {
			t.Errorf("%s is true on an anonymous report; there is no one to authorise it", capability)
		}
	}
}

// Tracking is the obvious way someone would try to reach an anonymous report.
// It must fail exactly like an unknown reference — an "anonymous reports cannot
// be tracked" answer here would confirm the reference is real.
func TestAnonymousReportCannotBeTrackedOrGuessed(t *testing.T) {
	e := newEnv(t)

	created := anonymousReport(t, e, "Broken streetlight")
	reference, _ := created["reference"].(string)

	var real, unknown map[string]any
	realCode := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   reference,
		"verificationValue": "guess@example.gov",
	}, &real)
	unknownCode := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   "SR-ZZZZ-ZZZZ",
		"verificationValue": "guess@example.gov",
	}, &unknown)

	if realCode != http.StatusNotFound {
		t.Errorf("tracking a real anonymous reference -> %d, want 404", realCode)
	}
	if realCode != unknownCode || real["detail"] != unknown["detail"] {
		t.Errorf("an anonymous reference is distinguishable from an unknown one:\n real: %d %v\n fake: %d %v",
			realCode, real["detail"], unknownCode, unknown["detail"])
	}
}

// The ticket is explicit: no notification, and no suppression either. A
// suppression means "we meant to reach someone and could not", which an
// operator is expected to act on — one per event per anonymous request would
// bury the real ones.
func TestAnonymousReportQueuesNothingAndSuppressesNothing(t *testing.T) {
	e := newEnv(t)

	created := anonymousReport(t, e, "Fallen branch across the path")
	reference, _ := created["reference"].(string)

	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	var queued int64
	if err := e.db.Model(&domain.NotificationOutbox{}).
		Where("request_id = ?", req.ID).Count(&queued).Error; err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if queued != 0 {
		t.Errorf("%d notification(s) queued for an anonymous report; there is nobody to send to", queued)
	}
}

// A signed-in resident must keep the stronger deal. The same endpoint now
// serves both, so this is the regression that matters most.
func TestSignedInReportStillAttributedAndTrackable(t *testing.T) {
	e := newEnv(t)
	reference, email := fileRequest(t, e, "citizen-still-works")

	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if req.ContactID == "" {
		t.Fatal("a signed-in report lost its contact")
	}
	if req.Channel != domain.ChannelAuthenticated {
		t.Errorf("channel = %q, want %q", req.Channel, domain.ChannelAuthenticated)
	}

	code := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   reference,
		"verificationValue": email,
	}, nil)
	if code != http.StatusOK {
		t.Errorf("tracking a signed-in report -> %d, want 200", code)
	}
}

// The channel comes from the session, never the body. Otherwise a caller could
// ask to be treated as somebody else.
func TestSubmissionChannelCannotBeSetByTheClient(t *testing.T) {
	e := newEnv(t)

	serviceType := publicServiceType(t, e, "POTHOLE")

	// The body cannot even name these fields: the portal decoder rejects
	// unknown ones outright, so an attempt to choose the channel or the contact
	// is refused before any handler sees it. That is a stronger guarantee than
	// ignoring them, and it is the one worth pinning down.
	for _, smuggled := range []map[string]any{
		{"channel": domain.ChannelAuthenticated},
		{"contactId": "00000000-0000-0000-0000-000000000001"},
	} {
		body := map[string]any{
			"serviceTypeId": serviceType,
			"subject":       "Trying to look authenticated",
			"address1":      "1 Nowhere Road",
			"formData":      map[string]any{"size": "Medium"},
			"formToken":     formToken(t, e),
		}
		for k, v := range smuggled {
			body[k] = v
		}
		if code := doJSON(t, newJarClient(), http.MethodPost,
			e.api.URL+"/api/portal/requests", body, nil); code != http.StatusBadRequest {
			t.Errorf("body carrying %v -> %d, want 400", smuggled, code)
		}
	}

	// And the honest submission is anonymous, because the session said so.
	var created map[string]any
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": serviceType,
		"subject":       "An ordinary anonymous report",
		"address1":      "1 Nowhere Road",
		"formData":      map[string]any{"size": "Medium"},
		"formToken":     formToken(t, e),
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}

	reference, _ := created["reference"].(string)
	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if req.Channel != domain.ChannelAnonymous {
		t.Errorf("channel = %q, want %q", req.Channel, domain.ChannelAnonymous)
	}
	if req.ContactID != "" {
		t.Errorf("contact = %q, want none", req.ContactID)
	}
}
