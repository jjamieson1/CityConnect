package httpapi_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// jpegBytes is a minimal file that both declares and sniffs as a JPEG, so the
// tests exercise the same path a phone camera would.
var jpegBytes = append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("a photo of a pothole")...)

// uploadPhoto posts a file to a report, optionally presenting an upload grant.
func uploadPhoto(t *testing.T, e *env, client *http.Client, reference, grant, name string, body []byte) int {
	t.Helper()

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)

	// A browser sets the part's Content-Type; the server's allow-list reads it.
	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	headers.Set("Content-Type", "image/jpeg")
	part, err := form.CreatePart(headers)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := form.Close(); err != nil {
		t.Fatalf("close form: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost,
		e.api.URL+"/api/portal/requests/"+reference+"/attachments", &buf)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if grant != "" {
		req.Header.Set("X-Upload-Grant", grant)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// anonymousReportWithGrant files anonymously and returns the reference plus the
// upload grant the create response carried.
func anonymousReportWithGrant(t *testing.T, e *env) (reference, grant string) {
	t.Helper()

	var created struct {
		Reference   string `json:"reference"`
		UploadGrant string `json:"uploadGrant"`
	}
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       "Pothole with a photo",
		"address1":      "44 Elm Street",
		"formData":      map[string]any{"size": "Medium"},
		"formToken":     formToken(t, e),
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}
	if created.UploadGrant == "" {
		t.Fatal("no upload grant issued; an anonymous reporter cannot attach anything")
	}
	return created.Reference, created.UploadGrant
}

// The demo beat: report a pothole with no account, then send the photo.
func TestAnonymousReporterCanAttachAPhoto(t *testing.T) {
	e := newEnv(t)
	reference, grant := anonymousReportWithGrant(t, e)

	code := uploadPhoto(t, e, newJarClient(), reference, grant, "pothole.jpg", jpegBytes)
	if code != http.StatusCreated {
		t.Fatalf("upload -> %d, want 201", code)
	}

	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	var count int64
	if err := e.db.Model(&domain.Attachment{}).Where("request_id = ?", req.ID).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("%d attachments recorded, want 1", count)
	}
}

// Without the grant there is nothing authorising the upload, and a reference is
// not a credential — anyone could quote one.
func TestAnonymousUploadNeedsTheGrant(t *testing.T) {
	e := newEnv(t)
	reference, _ := anonymousReportWithGrant(t, e)

	if code := uploadPhoto(t, e, newJarClient(), reference, "", "x.jpg", jpegBytes); code != http.StatusNotFound {
		t.Errorf("upload with no grant -> %d, want 404", code)
	}
	if code := uploadPhoto(t, e, newJarClient(), reference, "0.notasignature", "x.jpg", jpegBytes); code != http.StatusNotFound {
		t.Errorf("upload with a forged grant -> %d, want 404", code)
	}
}

// A grant is bound to one report. Turning it on another is the obvious attack.
func TestUploadGrantIsBoundToItsOwnReport(t *testing.T) {
	e := newEnv(t)
	_, grantForFirst := anonymousReportWithGrant(t, e)
	secondReference, _ := anonymousReportWithGrant(t, e)

	code := uploadPhoto(t, e, newJarClient(), secondReference, grantForFirst, "x.jpg", jpegBytes)
	if code != http.StatusNotFound {
		t.Errorf("grant reused against another report -> %d, want 404", code)
	}
}

// A signed-in resident is authorised by their session, and must not be able to
// attach to somebody else's report.
func TestSignedInUploadRequiresOwnership(t *testing.T) {
	e := newEnv(t)

	owner := e.portalSignIn(t, "citizen-owner")
	var created struct {
		Reference string `json:"reference"`
	}
	if code := doJSON(t, owner, http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       "My own report",
		"address1":      "9 Oak Street",
		"formData":      map[string]any{"size": "Medium"},
	}, &created); code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}

	if code := uploadPhoto(t, e, owner, created.Reference, "", "mine.jpg", jpegBytes); code != http.StatusCreated {
		t.Errorf("the owner could not attach to their own report -> %d", code)
	}

	stranger := e.portalSignIn(t, "citizen-stranger")
	if code := uploadPhoto(t, e, stranger, created.Reference, "", "theirs.jpg", jpegBytes); code != http.StatusNotFound {
		t.Errorf("a stranger attached to someone else's report -> %d, want 404", code)
	}
}

// The count cap from CIT-14's scope, which had nothing to limit until now.
func TestPublicAttachmentsAreCapped(t *testing.T) {
	e := newEnv(t)
	reference, grant := anonymousReportWithGrant(t, e)

	var refused bool
	for i := 0; i < 10; i++ {
		code := uploadPhoto(t, e, newJarClient(), reference, grant, "photo.jpg", jpegBytes)
		if code == http.StatusBadRequest {
			refused = true
			break
		}
		if code != http.StatusCreated {
			t.Fatalf("upload %d -> %d", i, code)
		}
	}
	if !refused {
		t.Error("a report accepted ten files; the count cap is not enforced")
	}
}

// The grant authorises uploads and nothing else. It must not become a way to
// read a report back.
func TestUploadGrantDoesNotGrantReadAccess(t *testing.T) {
	e := newEnv(t)
	reference, grant := anonymousReportWithGrant(t, e)

	client := newJarClient()
	req, _ := http.NewRequest(http.MethodGet, e.api.URL+"/api/portal/requests/"+reference, nil)
	req.Header.Set("X-Upload-Grant", grant)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reading a report with an upload grant -> %d, want 401", resp.StatusCode)
	}
}
