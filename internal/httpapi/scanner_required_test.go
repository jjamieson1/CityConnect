package httpapi_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// uploadTo posts one file to a report and returns the status code.
func uploadTo(t *testing.T, e *env, client *http.Client, reference, grant string) int {
	t.Helper()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "pothole.jpg")
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	// A JPEG magic number, so the content sniff agrees with the declared type.
	part.Write([]byte("\xff\xd8\xff\xe0JFIF and then some pixels"))
	w.Close()

	url := e.api.URL + "/api/portal/requests/" + reference + "/attachments"
	if grant != "" {
		url += "?grant=" + grant
	}
	req, _ := http.NewRequest(http.MethodPost, url, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// The point of the whole change. Quarantining a file we already know can never
// be scanned tells the resident their photo arrived and then loses it — the
// quarantine never drains, and nobody finds out until somebody goes looking
// for a photograph that was never viewable.
func TestUploadIsRefusedWhenNothingCanScanIt(t *testing.T) {
	e := newEnvScannerless(t)

	created := anonymousReport(t, e, "Pothole with a photo")
	reference, _ := created["reference"].(string)
	grant, _ := created["uploadGrant"].(string)

	if code := uploadTo(t, e, newJarClient(), reference, grant); code != http.StatusServiceUnavailable {
		t.Errorf("upload with no scanner -> %d, want 503", code)
	}

	// Refused, not silently swallowed: nothing may reach the store.
	var count int64
	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	e.db.Model(&domain.Attachment{}).Where("request_id = ?", req.ID).Count(&count)
	if count != 0 {
		t.Errorf("%d attachment row(s) created by a refused upload", count)
	}
}

// The report itself must still be accepted. A resident who cannot send a
// photograph has still told the City about a hazard, and losing the report
// over the attachment would be the wrong trade.
func TestTheReportStillSucceedsWithoutAScanner(t *testing.T) {
	e := newEnvScannerless(t)

	created := anonymousReport(t, e, "Hazard, no photo possible")
	if reference, _ := created["reference"].(string); reference == "" {
		t.Fatal("the report was refused along with the attachment")
	}
}

// A quarantine with one door left open is not a quarantine. An agent must not
// be able to attach a file the scanner never saw either.
func TestStaffUploadIsRefusedTooWhenNothingCanScanIt(t *testing.T) {
	e := newEnvScannerless(t)
	e.signIn("staff-agent", "agent@city.example", domain.RoleAdmin)

	req := e.seedRequest(t)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "evidence.jpg")
	part.Write([]byte("\xff\xd8\xff\xe0JFIF"))
	w.Close()

	code := e.do(http.MethodPost, "/api/requests/"+req.ID+"/attachments", nil, nil)
	if code == http.StatusOK || code == http.StatusCreated {
		t.Errorf("staff upload with no scanner -> %d, want a refusal", code)
	}
}

// The portal must not offer a control the upload endpoint would refuse. Being
// asked for a photograph and then told it cannot be taken is worse than never
// being asked.
func TestTheCatalogueSaysWhetherFilesCanBeSent(t *testing.T) {
	withScanner := portalCatalog(t, newEnv(t))
	without := portalCatalog(t, newEnvScannerless(t))

	if !withScanner["POTHOLE"].AcceptsFiles {
		t.Error("a deployment with a scanner is not offering attachments")
	}
	if without["POTHOLE"].AcceptsFiles {
		t.Error("a deployment with no scanner is still offering a photo control")
	}
}

// A service with attachments switched off says so even where scanning works,
// which is the half of this that has nothing to do with the scanner.
func TestAServiceWithAttachmentsOffSaysSo(t *testing.T) {
	e := newEnv(t)

	if err := e.db.Model(&domain.ServiceType{}).Where("code = ?", "POTHOLE").
		UpdateColumn("allows_attachments", false).Error; err != nil {
		t.Fatalf("switch off: %v", err)
	}

	if portalCatalog(t, e)["POTHOLE"].AcceptsFiles {
		t.Error("a service with AllowsAttachments=false is still offering a photo control")
	}
}
