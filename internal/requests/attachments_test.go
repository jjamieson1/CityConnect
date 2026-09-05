package requests

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/storetest"
)

// eicar is the standard antivirus test string. Every scanner detects it and it
// is completely harmless, which is the whole point of it existing.
const eicar = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// attachEnv is the smallest thing that can store an attachment: a database, a
// request to hang it on, and a store with whichever scanner the test needs.
type attachEnv struct {
	svc   *Service
	store *AttachmentStore
	db    *gorm.DB
	dir   string
	reqID string
}

func newAttachEnv(t *testing.T, scan ScanFunc) *attachEnv {
	t.Helper()

	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &Service{db: db, audit: audit.NewService(db, log), log: log}

	dir := t.TempDir()
	store, err := NewAttachmentStore(dir, 5, scan)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	req := &domain.Request{
		Reference: "SR-TEST-0001", ServiceTypeID: domain.NewID(),
		Channel: domain.ChannelAnonymous, Subject: "Pothole",
		Status: domain.StatusNew, Priority: domain.PriorityNormal, Version: 1,
	}
	if err := db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}

	return &attachEnv{svc: svc, store: store, db: db, dir: dir, reqID: req.ID}
}

// jpeg wraps content in a JPEG magic number, so a file declared as an image
// also sniffs as one.
func jpeg(content string) string {
	return string([]byte{0xff, 0xd8, 0xff, 0xe0}) + content
}

// attach uploads content as a JPEG photo, which is what a resident sends.
func (e *attachEnv) attach(t *testing.T, content string) (*domain.Attachment, error) {
	t.Helper()
	return e.attachAs(t, "photo.jpg", "image/jpeg", jpeg(content))
}

// attachAs uploads arbitrary bytes under a declared type, for the cases where
// the type is the point.
func (e *attachEnv) attachAs(t *testing.T, filename, contentType, content string) (*domain.Attachment, error) {
	t.Helper()
	return e.svc.Attach(context.Background(), audit.JobActor("test"), e.store, e.reqID, AttachInput{
		Filename:    filename,
		ContentType: contentType,
		Reader:      strings.NewReader(content),
		Size:        int64(len(content)),
	})
}

// quarantined lists what is still sitting in quarantine.
func (e *attachEnv) quarantined(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.dir, quarantineDir))
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func cleanScanner(ScanResult) ScanFunc {
	return func(context.Context, string) ScanResult {
		return ScanResult{Status: domain.ScanClean}
	}
}

// A cleared photo is promoted out of quarantine and can be served.
func TestCleanAttachmentIsPromotedOutOfQuarantine(t *testing.T) {
	e := newAttachEnv(t, cleanScanner(ScanResult{}))

	att, err := e.attach(t, "a photo of a pothole")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if att.ScanStatus != domain.ScanClean {
		t.Errorf("status = %q, want %q", att.ScanStatus, domain.ScanClean)
	}
	if !att.Servable() {
		t.Error("a cleared file is not servable")
	}
	if strings.HasPrefix(att.StoragePath, quarantineDir) {
		t.Errorf("storage path %q is still inside quarantine", att.StoragePath)
	}
	if _, err := os.Stat(e.store.Path(att.StoragePath)); err != nil {
		t.Errorf("promoted file is not on disk: %v", err)
	}
	if left := e.quarantined(t); len(left) != 0 {
		t.Errorf("quarantine still holds %v", left)
	}
}

// An infected file is refused and leaves nothing behind. Not stored and
// flagged — a copy on disk is a copy that can be served by mistake.
func TestInfectedAttachmentIsRejectedAndNeverStored(t *testing.T) {
	e := newAttachEnv(t, func(_ context.Context, path string) ScanResult {
		body, _ := os.ReadFile(path)
		if strings.Contains(string(body), "EICAR-STANDARD-ANTIVIRUS-TEST-FILE") {
			return ScanResult{Status: domain.ScanInfected, Note: "Eicar-Test-Signature"}
		}
		return ScanResult{Status: domain.ScanClean}
	})

	// Sent as text/plain, which it is. Declaring it an image would be refused
	// by the type check before the scanner ever saw it, and this test would
	// pass without proving anything about scanning.
	if _, err := e.attachAs(t, "eicar.txt", "text/plain", eicar); err == nil {
		t.Fatal("an infected upload was accepted")
	}

	if left := e.quarantined(t); len(left) != 0 {
		t.Errorf("the infected file is still in quarantine: %v", left)
	}
	var rows int64
	if err := e.db.Model(&domain.Attachment{}).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d attachment row(s) recorded for a rejected file", rows)
	}

	// And the request survives — one bad file must not lose the report.
	var req domain.Request
	if err := e.db.First(&req, "id = ?", e.reqID).Error; err != nil {
		t.Errorf("the request was lost with the attachment: %v", err)
	}
}

// The scanner being down must not lose a resident's report, and must not store
// a file nobody looked at. Both at once, which is what quarantine is for.
func TestUnavailableScannerQuarantinesWithoutLosingTheReport(t *testing.T) {
	e := newAttachEnv(t, func(context.Context, string) ScanResult {
		return ScanResult{Status: domain.ScanPending, Note: "scanner unavailable"}
	})

	att, err := e.attach(t, "a photo taken while clamd was restarting")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if att.ScanStatus != domain.ScanPending {
		t.Errorf("status = %q, want %q", att.ScanStatus, domain.ScanPending)
	}
	if att.Servable() {
		t.Error("an unscanned file is servable")
	}
	if !strings.HasPrefix(att.StoragePath, quarantineDir) {
		t.Errorf("storage path %q left quarantine without being cleared", att.StoragePath)
	}
	if left := e.quarantined(t); len(left) != 1 {
		t.Errorf("quarantine holds %v, want exactly the one file", left)
	}
}

// A scanner that ran and could not decide is not a clean result either.
func TestFailedScanIsNotServable(t *testing.T) {
	e := newAttachEnv(t, func(context.Context, string) ScanResult {
		return ScanResult{Status: domain.ScanFailed, Note: "Can't read file"}
	})

	att, err := e.attach(t, "something the scanner choked on")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if att.Servable() {
		t.Error("a file the scanner could not read is servable")
	}
	if !strings.HasPrefix(att.StoragePath, quarantineDir) {
		t.Errorf("storage path %q left quarantine", att.StoragePath)
	}
}

// The old default recorded "skipped", which reads as a decision that was made.
// Nothing may produce it now: an unconfigured deployment quarantines instead.
func TestNoScannerConfiguredQuarantinesRatherThanSkipping(t *testing.T) {
	e := newAttachEnv(t, nil)

	att, err := e.attach(t, "a photo on a deployment with no scanner")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if att.ScanStatus == "skipped" {
		t.Fatal(`status is "skipped"; an unscanned file must never look decided`)
	}
	if att.ScanStatus != domain.ScanPending {
		t.Errorf("status = %q, want %q", att.ScanStatus, domain.ScanPending)
	}
	if att.Servable() {
		t.Error("a file from a deployment with no scanner is servable")
	}
}

// The scanner sees the bytes as stored, not a claim about them.
func TestScannerReceivesTheStoredFile(t *testing.T) {
	var scanned string
	e := newAttachEnv(t, func(_ context.Context, path string) ScanResult {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("scanner could not read the quarantined file: %v", err)
		}
		scanned = string(body)
		return ScanResult{Status: domain.ScanClean}
	})

	const content = "the exact bytes that were uploaded"
	if _, err := e.attach(t, content); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if scanned != jpeg(content) {
		t.Errorf("scanner saw %q, want %q", scanned, jpeg(content))
	}
}

// The allow-list reads a content type the client chose, so an unauthenticated
// caller can simply claim a different one. For formats with a real magic
// number the bytes have to agree.
func TestUploadDeclaringAnImageMustActuallyBeOne(t *testing.T) {
	e := newAttachEnv(t, cleanScanner(ScanResult{}))

	_, err := e.attachAs(t, "not-really.jpg", "image/jpeg",
		"<html><script>alert(1)</script></html>")
	if err == nil {
		t.Fatal("HTML declared as a JPEG was accepted")
	}
	if left := e.quarantined(t); len(left) != 0 {
		t.Errorf("the rejected file is still on disk: %v", left)
	}
}

// A real image still has to get through — the check must not cost residents
// their photos.
func TestGenuineImageIsAccepted(t *testing.T) {
	e := newAttachEnv(t, cleanScanner(ScanResult{}))

	att, err := e.attachAs(t, "real.jpg", "image/jpeg", jpeg("photo"))
	if err != nil {
		t.Fatalf("a genuine JPEG was rejected: %v", err)
	}
	if !att.Servable() {
		t.Error("a clean genuine image is not servable")
	}
}
