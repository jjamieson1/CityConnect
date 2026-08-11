package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

const (
	// idempotencyTTL is how long a key's result is replayable. Long enough to
	// cover a partner's retry schedule and an operator re-running a failed
	// batch the next morning; short enough that the table stays small.
	idempotencyTTL = 24 * time.Hour

	// maxReplayBody caps what is stored. A response too large to keep is still
	// executed exactly once — the key is recorded — it simply cannot be
	// replayed verbatim, which is the right trade against unbounded rows.
	maxReplayBody = 64 << 10
)

// idempotency makes a mutating request safe to retry.
//
// Connected systems retry: a timeout, a dropped connection, a queue redelivery.
// Without this, each retry files another service request, and a duplicate
// request is a duplicate work order — a second crew dispatched to a pothole
// that has already been filled.
//
// The header is optional. A caller that sends one gets exactly-once semantics;
// one that does not is unaffected.
func (s *Server) idempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || !mutating(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > 200 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key",
				"Idempotency-Key must be 200 characters or fewer.")
			return
		}

		// The body is read here so it can be fingerprinted, then restored for
		// the handler.
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_body", "Could not read the request body.")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		clientKey := idempotencyClient(r)
		fingerprint := fingerprintRequest(r, body)

		record, replay, err := s.claimIdempotencyKey(r, clientKey, key, fingerprint)
		if err != nil {
			fail(w, r, err)
			return
		}

		switch {
		case replay != nil && replay.StatusCode == 0:
			// The first attempt is still running. Answering now would either
			// duplicate the work or return a half-finished result.
			w.Header().Set("Retry-After", "2")
			writeProblem(w, r, http.StatusConflict, "idempotency_in_progress",
				"A request with this Idempotency-Key is still being processed. Retry shortly.")
			return

		case replay != nil && replay.Fingerprint != fingerprint:
			// Same key, different payload. Replaying the first result would
			// silently discard the second request; the caller has a bug.
			writeProblem(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused",
				"This Idempotency-Key was already used with a different request body.")
			return

		case replay != nil:
			w.Header().Set("Idempotent-Replay", "true")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(replay.StatusCode)
			_, _ = w.Write([]byte(replay.ResponseBody))
			return
		}

		// First time through: run the handler, capturing the response so a
		// retry can be answered with exactly what the caller missed.
		rec := &recordingWriter{ResponseWriter: w, limit: maxReplayBody}
		next.ServeHTTP(rec, r)

		s.completeIdempotencyKey(r, record, rec)
	})
}

func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// idempotencyClient identifies the caller a key belongs to. Keys are scoped to
// it so one client cannot read back another's response by guessing a value.
func idempotencyClient(r *http.Request) string {
	if p := principalFrom(r.Context()); p != nil {
		if id := p.ID(); id != "" {
			return id
		}
	}
	if c := citizenFrom(r.Context()); c != nil {
		return "citizen:" + c.ID
	}
	return "anonymous:" + clientIP(r)
}

func fingerprintRequest(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0x1f})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0x1f})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// claimIdempotencyKey reserves a key, or returns the existing record.
//
// The reservation is an insert against a unique index rather than a
// read-then-write: two retries arriving together would both pass a read check
// and both execute. The database decides the winner.
func (s *Server) claimIdempotencyKey(r *http.Request, clientKey, key, fingerprint string) (*domain.IdempotencyKey, *domain.IdempotencyKey, error) {
	now := time.Now().UTC()

	// Drop an expired record first so a reused key after the TTL behaves as a
	// fresh one rather than colliding forever.
	//
	// Unscoped, because these rows carry the soft-delete column every model
	// embeds. A soft delete leaves the row in place, so the unique index still
	// holds the key while the lookup — which hides deleted rows — cannot find
	// it: the re-insert collides and the read returns nothing.
	s.DB.WithContext(r.Context()).Unscoped().
		Where("client_key = ? AND `key` = ? AND expires_at < ?", clientKey, key, now).
		Delete(&domain.IdempotencyKey{})

	record := &domain.IdempotencyKey{
		ClientKey: clientKey, Key: key, Fingerprint: fingerprint,
		StatusCode: 0, ExpiresAt: now.Add(idempotencyTTL),
	}

	err := s.DB.WithContext(r.Context()).Create(record).Error
	if err == nil {
		return record, nil, nil
	}

	translated := store.Translate(err)
	if !errors.Is(translated, store.ErrDuplicate) {
		return nil, nil, translated
	}

	var existing domain.IdempotencyKey
	if err := s.DB.WithContext(r.Context()).
		Where("client_key = ? AND `key` = ?", clientKey, key).
		First(&existing).Error; err != nil {
		return nil, nil, store.Translate(err)
	}
	return nil, &existing, nil
}

// completeIdempotencyKey stores the outcome, or releases the key.
func (s *Server) completeIdempotencyKey(r *http.Request, record *domain.IdempotencyKey, rec *recordingWriter) {
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}

	// A failed attempt does not consume the key. The caller is expected to fix
	// the problem and retry with the same value, and holding the key would
	// answer that retry with the original failure forever.
	if status >= 400 || rec.overflowed {
		// Unscoped: a soft delete would leave the key claimed forever, and the
		// caller's corrected retry would collide with a row nobody can read.
		s.DB.WithContext(r.Context()).Unscoped().Delete(record)
		return
	}

	err := s.DB.WithContext(r.Context()).Model(record).Updates(map[string]any{
		"status_code":   status,
		"response_body": rec.body.String(),
	}).Error
	if err != nil {
		s.log.ErrorContext(r.Context(), "could not record an idempotency result",
			"key", record.Key, "error", err)
	}
}

// recordingWriter tees the response so it can be replayed later.
type recordingWriter struct {
	http.ResponseWriter
	status     int
	body       bytes.Buffer
	limit      int
	overflowed bool
}

func (w *recordingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(b) > w.limit {
		w.overflowed = true
	} else {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// PurgeIdempotencyKeys removes expired reservations.
func PurgeIdempotencyKeys(db *gorm.DB) (int64, error) {
	res := db.Unscoped().Where("expires_at < ?", time.Now().UTC()).Delete(&domain.IdempotencyKey{})
	return res.RowsAffected, store.Translate(res.Error)
}
