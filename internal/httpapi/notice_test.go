package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// noticeFor reads the collection notice the portal would show for a service.
func noticeFor(t *testing.T, e *env, code string) (body, id string) {
	t.Helper()

	var catalog struct {
		Items []struct {
			Code             string `json:"code"`
			CollectionNotice string `json:"collectionNotice"`
			NoticeID         string `json:"noticeId"`
		} `json:"items"`
	}
	if status := doJSON(t, newJarClient(), http.MethodGet,
		e.api.URL+"/api/portal/catalog", nil, &catalog); status != 200 {
		t.Fatalf("catalog -> %d", status)
	}
	for _, c := range catalog.Items {
		if c.Code == code {
			return c.CollectionNotice, c.NoticeID
		}
	}
	t.Fatalf("%s not in the catalogue", code)
	return "", ""
}

// A resident must be able to read why their details are being collected before
// they give them, not afterwards and not behind a link.
func TestCollectionNoticeIsShownWithTheForm(t *testing.T) {
	e := newEnv(t)

	body, id := noticeFor(t, e, "POTHOLE")
	if body == "" {
		t.Error("no collection notice offered with the intake form")
	}
	if id == "" {
		t.Error("no notice version identified; the record cannot say what was read")
	}
}

// The record has to be reproducible per submission. This is the requirement
// that makes "we told them" mean something years later.
func TestSubmissionRecordsTheNoticeItWasShown(t *testing.T) {
	e := newEnv(t)

	shown, noticeID := noticeFor(t, e, "POTHOLE")

	var created struct {
		Reference string `json:"reference"`
	}
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       "Pothole with contact details",
		"address1":      "44 Elm Street",
		"formData":      map[string]any{"size": "Medium"},
		"formToken":     formToken(t, e),
		"contactEmail":  "resident@example.com",
		"noticeId":      noticeID,
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}

	req, err := e.lookupByReference(created.Reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	var record domain.RequestNotice
	if err := e.db.First(&record, "request_id = ?", req.ID).Error; err != nil {
		t.Fatalf("no notice recorded against the submission: %v", err)
	}
	if record.Body != shown {
		t.Errorf("recorded wording differs from what was shown:\n got: %q\nwant: %q",
			record.Body, shown)
	}
	if record.NoticeID != noticeID {
		t.Errorf("recorded version %q, want %q", record.NoticeID, noticeID)
	}
	if record.ShownAt.IsZero() {
		t.Error("no timestamp on the record")
	}
}

// The client selects a version; it must never supply the words. Otherwise the
// compliance record is authored by whoever is submitting.
func TestClientCannotWriteItsOwnWordingIntoTheRecord(t *testing.T) {
	e := newEnv(t)
	shown, _ := noticeFor(t, e, "POTHOLE")

	var created struct {
		Reference string `json:"reference"`
	}
	// A body field for the notice text does not exist, so an attempt to send
	// one is refused outright by the decoder — which is the strongest possible
	// version of this guarantee.
	if code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests",
		map[string]any{
			"serviceTypeId":    publicServiceType(t, e, "POTHOLE"),
			"subject":          "Trying to author the record",
			"address1":         "1 Nowhere Road",
			"formData":         map[string]any{"size": "Medium"},
			"formToken":        formToken(t, e),
			"contactEmail":     "resident@example.com",
			"collectionNotice": "You agreed to absolutely anything.",
		}, nil); code != http.StatusBadRequest {
		t.Errorf("a body carrying notice wording -> %d, want 400", code)
	}

	// An unknown version id degrades to the current notice rather than
	// recording nothing at all.
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests",
		map[string]any{
			"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
			"subject":       "Unknown notice version",
			"address1":      "1 Nowhere Road",
			"formData":      map[string]any{"size": "Medium"},
			"formToken":     formToken(t, e),
			"contactEmail":  "resident@example.com",
			"noticeId":      "00000000-0000-0000-0000-000000000000",
		}, &created)
	if code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}

	req, err := e.lookupByReference(created.Reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	var record domain.RequestNotice
	if err := e.db.First(&record, "request_id = ?", req.ID).Error; err != nil {
		t.Fatalf("an unknown version recorded nothing: %v", err)
	}
	if record.Body != shown {
		t.Errorf("recorded %q, want the current notice", record.Body)
	}
}

// An anonymous report collects no personal information, so there is nothing to
// give notice about — and a record claiming otherwise would be misleading.
func TestAnonymousSubmissionRecordsNoNotice(t *testing.T) {
	e := newEnv(t)

	created := anonymousReport(t, e, "No details given")
	reference, _ := created["reference"].(string)
	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	var count int64
	if err := e.db.Model(&domain.RequestNotice{}).
		Where("request_id = ?", req.ID).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("%d notice record(s) for a report that collected nothing", count)
	}
}

// A signed-in resident gives their details too, so the same record applies.
func TestSignedInSubmissionAlsoRecordsTheNotice(t *testing.T) {
	e := newEnv(t)
	client := e.portalSignIn(t, "citizen-notice")

	_, noticeID := noticeFor(t, e, "POTHOLE")
	var created struct {
		Reference string `json:"reference"`
	}
	if code := doJSON(t, client, http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       "Filed while signed in",
		"address1":      "9 Oak Street",
		"formData":      map[string]any{"size": "Medium"},
		"noticeId":      noticeID,
	}, &created); code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}

	req, err := e.lookupByReference(created.Reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	var count int64
	e.db.Model(&domain.RequestNotice{}).Where("request_id = ?", req.ID).Count(&count)
	if count != 1 {
		t.Errorf("%d notice records for a signed-in submission, want 1", count)
	}
}
