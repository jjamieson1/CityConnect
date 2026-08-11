// Package audit records every consequential action in a hash-chained,
// append-only log.
//
// The chain matters because C2 keeps a tamper-evident record of every
// notification it accepts from us. Without an equivalent on our side the two
// logs cannot be reconciled, and "CityConnect says it sent that" carries no
// weight. Each row commits to the previous row's hash, so an edit or deletion
// inside the chain is detectable by replaying it.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// Actor types.
const (
	ActorUser   = "user"
	ActorSystem = "system"
	ActorC2     = "c2"
	ActorJob    = "job"
)

// Actor identifies who performed an action.
type Actor struct {
	Type  string
	ID    string
	Label string
	IP    string
}

// UserActor builds an Actor for a staff user.
func UserActor(id, label, ip string) Actor {
	return Actor{Type: ActorUser, ID: id, Label: label, IP: ip}
}

// SystemActor builds an Actor for a connected system.
func SystemActor(id, label, ip string) Actor {
	return Actor{Type: ActorSystem, ID: id, Label: label, IP: ip}
}

// JobActor builds an Actor for a background job.
func JobActor(name string) Actor {
	return Actor{Type: ActorJob, Label: name}
}

// C2Actor builds an Actor for an action C2 initiated.
func C2Actor(sub string) Actor {
	return Actor{Type: ActorC2, ID: sub, Label: "C2"}
}

// Entry describes one auditable action.
type Entry struct {
	Action     string
	TargetType string
	TargetID   string
	Summary    string
	Changes    domain.JSONMap
	RequestID  string
}

// Service appends to and verifies the audit chain.
type Service struct {
	db  *gorm.DB
	log *slog.Logger

	// mu serialises appends so the chain stays linear. Audit writes are far
	// from the hot path, and a fork in the chain would make it unverifiable.
	mu sync.Mutex
}

// NewService builds an audit service.
func NewService(db *gorm.DB, log *slog.Logger) *Service {
	return &Service{db: db, log: log.With("component", "audit")}
}

// Record appends an entry. Audit failures are logged but never propagated:
// losing an audit row is bad, but failing the user's action because the audit
// write failed is worse, and the gap is itself detectable when the chain is
// verified.
func (s *Service) Record(ctx context.Context, actor Actor, e Entry) {
	if err := s.record(ctx, s.db, actor, e); err != nil {
		s.log.ErrorContext(ctx, "audit write failed",
			"action", e.Action, "target", e.TargetID, "error", err)
	}
}

// RecordTx appends an entry inside an existing transaction, so the audit row
// commits or rolls back with the action it describes.
func (s *Service) RecordTx(ctx context.Context, tx *gorm.DB, actor Actor, e Entry) error {
	return s.record(ctx, tx, actor, e)
}

func (s *Service) record(ctx context.Context, db *gorm.DB, actor Actor, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var prev domain.AuditLog
	err := db.WithContext(ctx).Order("seq DESC").Limit(1).Find(&prev).Error
	if err != nil {
		return fmt.Errorf("audit: read chain head: %w", err)
	}

	// Normalise before hashing: a nil map marshals to "null" on the way in but
	// reads back as "{}", which would make every entry fail its own hash
	// check on verification.
	if e.Changes == nil {
		e.Changes = domain.JSONMap{}
	}

	row := domain.AuditLog{
		Seq:        prev.Seq + 1,
		ActorType:  actor.Type,
		ActorID:    actor.ID,
		ActorLabel: actor.Label,
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetID:   e.TargetID,
		Summary:    truncate(e.Summary, 400),
		Changes:    e.Changes,
		IP:         actor.IP,
		RequestID:  e.RequestID,
		PrevHash:   prev.Hash,
	}
	row.ID = domain.NewID()
	row.Hash = hashEntry(&row)

	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("audit: append: %w", err)
	}
	return nil
}

// hashEntry computes the chain hash over the fields that must not change,
// including the sequence number and the link to the previous entry.
func hashEntry(row *domain.AuditLog) string {
	changes, _ := json.Marshal(row.Changes)
	h := sha256.New()
	for _, part := range []string{
		row.PrevHash, strconv.FormatUint(row.Seq, 10), row.ID,
		row.ActorType, row.ActorID, row.Action,
		row.TargetType, row.TargetID, row.Summary, string(changes),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0x1f}) // unit separator, so field boundaries are unambiguous
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyResult reports the outcome of a chain verification.
type VerifyResult struct {
	Checked    int64  `json:"checked"`
	Valid      bool   `json:"valid"`
	BrokenAt   uint64 `json:"brokenAtSeq,omitempty"`
	BrokenID   string `json:"brokenId,omitempty"`
	BrokenWhy  string `json:"brokenReason,omitempty"`
	HeadHash   string `json:"headHash,omitempty"`
}

// Verify replays the chain and reports the first inconsistency. This is what
// makes the log tamper-evident rather than merely append-only.
func (s *Service) Verify(ctx context.Context) (*VerifyResult, error) {
	res := &VerifyResult{Valid: true}
	var prevHash string

	rows := make([]domain.AuditLog, 0, 500)
	batch := 500
	for offset := 0; ; offset += batch {
		rows = rows[:0]
		err := s.db.WithContext(ctx).Order("seq ASC").Limit(batch).Offset(offset).Find(&rows).Error
		if err != nil {
			return nil, fmt.Errorf("audit: verify read: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			row := &rows[i]
			res.Checked++

			if row.PrevHash != prevHash {
				res.Valid = false
				res.BrokenAt, res.BrokenID = row.Seq, row.ID
				res.BrokenWhy = "previous-hash link does not match the preceding entry"
				return res, nil
			}
			if row.Seq != uint64(res.Checked) {
				res.Valid = false
				res.BrokenAt, res.BrokenID = row.Seq, row.ID
				res.BrokenWhy = "sequence number is out of order; an entry is missing"
				return res, nil
			}
			if want := hashEntry(row); want != row.Hash {
				res.Valid = false
				res.BrokenAt, res.BrokenID = row.Seq, row.ID
				res.BrokenWhy = "entry contents do not match its recorded hash"
				return res, nil
			}
			prevHash = row.Hash
		}
		if len(rows) < batch {
			break
		}
	}

	res.HeadHash = prevHash
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
