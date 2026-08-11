package requests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

// AttachmentStore persists uploaded files.
type AttachmentStore struct {
	dir    string
	maxMB  int64
	scan   ScanFunc
}

// ScanFunc is the hook for virus scanning. It returns a status and a note.
// The default implementation marks uploads "skipped" rather than "clean",
// because claiming a file was scanned when no scanner exists is worse than
// admitting it was not.
type ScanFunc func(path string) (status, note string)

// NewAttachmentStore builds a file store rooted at dir.
func NewAttachmentStore(dir string, maxMB int64, scan ScanFunc) (*AttachmentStore, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("requests: create attachment dir: %w", err)
	}
	if scan == nil {
		scan = func(string) (string, string) { return "skipped", "no scanner configured" }
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

	// Store under a generated name in a date-sharded directory. The original
	// filename is metadata only — using it on disk would let an upload named
	// "../../etc/passwd" decide where it lands.
	now := time.Now().UTC()
	relDir := filepath.Join(now.Format("2006"), now.Format("01"))
	if err := os.MkdirAll(filepath.Join(store_.dir, relDir), 0o750); err != nil {
		return nil, fmt.Errorf("requests: create attachment dir: %w", err)
	}
	relPath := filepath.Join(relDir, domain.NewID()+ext)
	absPath := filepath.Join(store_.dir, relPath)

	f, err := os.OpenFile(absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, fmt.Errorf("requests: create attachment: %w", err)
	}

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(in.Reader, maxBytes+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(absPath)
		return nil, fmt.Errorf("requests: write attachment: %w", errOr(err, closeErr))
	}
	if written > maxBytes {
		_ = os.Remove(absPath)
		return nil, fmt.Errorf("%w: file exceeds the %d MB limit", ErrInvalidInput, store_.maxMB)
	}

	scanStatus, scanNote := store_.scan(absPath)
	if scanStatus == "infected" {
		_ = os.Remove(absPath)
		return nil, fmt.Errorf("%w: the uploaded file failed a malware scan", ErrInvalidInput)
	}

	visibility := in.Visibility
	if visibility == "" {
		visibility = domain.VisibilityInternal
	}

	att := &domain.Attachment{
		RequestID: requestID, Filename: filepath.Base(in.Filename),
		ContentType: contentType, SizeBytes: written, StoragePath: relPath,
		Checksum: hex.EncodeToString(hash.Sum(nil)), UploadedByID: actor.ID,
		Visibility: visibility, ScanStatus: scanStatus, ScanNote: scanNote,
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
		_ = os.Remove(absPath)
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
