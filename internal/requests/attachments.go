package requests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// allowedTypes is the upload allowlist. It is an allowlist rather than a
// blocklist because a citizen-facing intake surface that accepts arbitrary
// content types is a file-serving vulnerability waiting to be found.
var allowedTypes = map[string]string{
	"image/jpeg":         ".jpg",
	"image/png":          ".png",
	"image/gif":          ".gif",
	"image/webp":         ".webp",
	"image/heic":         ".heic",
	"application/pdf":    ".pdf",
	"text/plain":         ".txt",
	"text/csv":           ".csv",
	"application/msword": ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": ".xlsx",
}

// quarantineDir holds an upload until the scanner has cleared it. It sits
// under the same root so promoting a clean file is a rename on one filesystem,
// which is atomic — a half-copied file must never appear in the served tree.
const quarantineDir = "quarantine"

// AttachmentStore persists uploaded files.
type AttachmentStore struct {
	dir   string
	maxMB int64
	scan  ScanFunc
}

// ScanResult is a scanner's verdict on one file.
type ScanResult struct {
	// Status is one of domain.ScanClean, domain.ScanInfected or
	// domain.ScanFailed. Anything else is treated as not-cleared.
	Status string
	Note   string
}

// ScanFunc is the hook for malware scanning.
//
// It is handed the quarantined path and must answer whether the file may be
// stored. There is deliberately no way to express "I did not look" as an
// approval: an implementation that cannot reach its scanner returns
// domain.ScanPending and the file stays quarantined.
type ScanFunc func(ctx context.Context, path string) ScanResult

// NewAttachmentStore builds a file store rooted at dir.
//
// A nil scan function quarantines everything. That is deliberate and it is the
// safe default: an unconfigured deployment holds uploads rather than storing
// files nobody has looked at, and the operator finds out because attachments
// stop appearing — not because one turns up in an inbox.
func NewAttachmentStore(dir string, maxMB int64, scan ScanFunc) (*AttachmentStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, quarantineDir), 0o750); err != nil {
		return nil, fmt.Errorf("requests: create attachment dir: %w", err)
	}
	if scan == nil {
		scan = func(context.Context, string) ScanResult {
			return ScanResult{Status: domain.ScanPending, Note: "no scanner configured"}
		}
	}
	if maxMB <= 0 {
		maxMB = 25
	}
	return &AttachmentStore{dir: dir, maxMB: maxMB, scan: scan}, nil
}

// Path returns the absolute path of a stored attachment.
func (a *AttachmentStore) Path(rel string) string {
	return filepath.Join(a.dir, filepath.Clean("/"+rel))
}

// AttachInput describes an upload.
type AttachInput struct {
	Filename    string
	ContentType string
	Visibility  domain.CommentVisibility
	Reader      io.Reader
	Size        int64
}

// Attach stores a file against a request.
func (s *Service) Attach(ctx context.Context, actor audit.Actor, store_ *AttachmentStore, requestID string, in AttachInput) (*domain.Attachment, error) {
	req, err := s.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}

	contentType := strings.ToLower(strings.TrimSpace(strings.Split(in.ContentType, ";")[0]))
	ext, ok := allowedTypes[contentType]
	if !ok {
		return nil, fmt.Errorf("%w: %s files are not accepted", ErrInvalidInput, contentType)
	}
	maxBytes := store_.maxMB << 20
	if in.Size > maxBytes {
		return nil, fmt.Errorf("%w: file exceeds the %d MB limit", ErrInvalidInput, store_.maxMB)
	}

	// Every upload lands in quarantine first. The name is generated: the
	// original filename is metadata only, because an upload called
	// "../../etc/passwd" must not get to decide where it goes.
	now := time.Now().UTC()
	name := domain.NewID() + ext
	quarantineRel := filepath.Join(quarantineDir, name)
	quarantinePath := filepath.Join(store_.dir, quarantineRel)

	f, err := os.OpenFile(quarantinePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, fmt.Errorf("requests: create attachment: %w", err)
	}

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(in.Reader, maxBytes+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(quarantinePath)
		return nil, fmt.Errorf("requests: write attachment: %w", errOr(err, closeErr))
	}
	if written > maxBytes {
		_ = os.Remove(quarantinePath)
		return nil, fmt.Errorf("%w: file exceeds the %d MB limit", ErrInvalidInput, store_.maxMB)
	}

	// The declared content type got us this far; the bytes have to agree.
	//
	// An allow-list keyed on what the client says it is sending is bypassed by
	// saying something else, and this endpoint is reachable without an account.
	// Sniffing is only decisive for formats with a real magic number, so the
	// check is asymmetric on purpose: claim an image and it must sniff as one.
	// A .docx is a zip and sniffs as one, so office formats are left to the
	// scanner and to the fact that nothing is ever served inline.
	if strings.HasPrefix(contentType, "image/") {
		sniffed, err := sniffType(quarantinePath)
		if err != nil {
			_ = os.Remove(quarantinePath)
			return nil, fmt.Errorf("requests: inspect upload: %w", err)
		}
		if !strings.HasPrefix(sniffed, "image/") {
			_ = os.Remove(quarantinePath)
			s.log.InfoContext(ctx, "rejected an upload whose bytes contradict its type",
				"declared", contentType, "sniffed", sniffed)
			return nil, fmt.Errorf("%w: that file is not the kind of image it claims to be", ErrInvalidInput)
		}
	}

	verdict := store_.scan(ctx, quarantinePath)

	// Infected files are deleted, not stored and flagged. There is no reason to
	// keep one, and a copy on disk is a copy that can be served by mistake.
	if verdict.Status == domain.ScanInfected {
		_ = os.Remove(quarantinePath)
		s.log.WarnContext(ctx, "rejected an infected upload",
			"request", requestID, "signature", verdict.Note)
		return nil, fmt.Errorf("%w: that file did not pass a malware scan", ErrInvalidInput)
	}

	// Only a cleared file is promoted out of quarantine into the served tree.
	// Anything else — the scanner unreachable, or unable to decide — stays put:
	// the request is still accepted, the file simply waits. Losing a resident's
	// whole report because a daemon was restarting would be the worse failure,
	// and so would storing a file nobody looked at.
	relPath := quarantineRel
	scanStatus := verdict.Status
	if scanStatus != domain.ScanClean && scanStatus != domain.ScanFailed {
		scanStatus = domain.ScanPending
	}

	if verdict.Status == domain.ScanClean {
		relDir := filepath.Join(now.Format("2006"), now.Format("01"))
		if err := os.MkdirAll(filepath.Join(store_.dir, relDir), 0o750); err != nil {
			_ = os.Remove(quarantinePath)
			return nil, fmt.Errorf("requests: create attachment dir: %w", err)
		}
		promoted := filepath.Join(relDir, name)
		if err := os.Rename(quarantinePath, filepath.Join(store_.dir, promoted)); err != nil {
			_ = os.Remove(quarantinePath)
			return nil, fmt.Errorf("requests: store attachment: %w", err)
		}
		relPath = promoted
	}

	visibility := in.Visibility
	if visibility == "" {
		visibility = domain.VisibilityInternal
	}

	att := &domain.Attachment{
		RequestID: requestID, Filename: filepath.Base(in.Filename),
		ContentType: contentType, SizeBytes: written, StoragePath: relPath,
		Checksum: hex.EncodeToString(hash.Sum(nil)), UploadedByID: actor.ID,
		Visibility: visibility, ScanStatus: scanStatus, ScanNote: verdict.Note,
	}

	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Create(att).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.Request{}).Where("id = ?", requestID).
			UpdateColumn("last_activity_at", now).Error; err != nil {
			return err
		}
		return s.addEvent(ctx, tx, requestID, domain.EvtAttachmentAdded, actor,
			"attached "+att.Filename, "", att.Filename, nil,
			visibility == domain.VisibilityCitizen)
	})
	if err != nil {
		_ = os.Remove(filepath.Join(store_.dir, relPath))
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "request.attachment_added", TargetType: "request", TargetID: requestID,
		Summary: req.Reference + ": " + att.Filename,
	})
	return att, nil
}

// Attachments lists a request's files.
func (s *Service) Attachments(ctx context.Context, requestID string) ([]domain.Attachment, error) {
	var out []domain.Attachment
	err := s.db.WithContext(ctx).Where("request_id = ?", requestID).
		Order("created_at ASC").Find(&out).Error
	return out, store.Translate(err)
}

// GetAttachment loads one file's record.
func (s *Service) GetAttachment(ctx context.Context, requestID, id string) (*domain.Attachment, error) {
	var att domain.Attachment
	err := s.db.WithContext(ctx).
		Where("id = ? AND request_id = ?", id, requestID).First(&att).Error
	if err != nil {
		return nil, ErrNotFound
	}
	return &att, nil
}

// DeleteAttachment removes a file record and its bytes.
func (s *Service) DeleteAttachment(ctx context.Context, actor audit.Actor, store_ *AttachmentStore, requestID, id string) error {
	att, err := s.GetAttachment(ctx, requestID, id)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Delete(&domain.Attachment{}, "id = ?", id).Error; err != nil {
		return store.Translate(err)
	}
	if err := os.Remove(store_.Path(att.StoragePath)); err != nil && !os.IsNotExist(err) {
		s.log.WarnContext(ctx, "attachment row deleted but file remains",
			"path", att.StoragePath, "error", err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "request.attachment_deleted", TargetType: "request", TargetID: requestID,
		Summary: att.Filename,
	})
	return nil
}

func errOr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// sniffType reports what a file actually looks like, from its leading bytes.
func sniffType(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// 512 bytes is what http.DetectContentType examines; reading more wastes IO
	// and reading less weakens the answer.
	head := make([]byte, 512)
	n, err := f.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	media, _, err := mime.ParseMediaType(http.DetectContentType(head[:n]))
	if err != nil {
		return "", err
	}
	return media, nil
}
