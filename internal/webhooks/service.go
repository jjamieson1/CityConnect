// Package webhooks delivers request events to connected systems, with HMAC
// signing, exponential backoff and a dead-letter state an admin can inspect
// and replay.
//
// A partner outage must not silently drop the events that happened during it.
// The permitting system being down for an afternoon is routine; the tickets it
// missed while down are not recoverable from anywhere else.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var (
	ErrNotFound = errors.New("webhooks: not found")
)

// maxAttempts bounds retries before a delivery is dead-lettered.
const maxAttempts = 8

// Service publishes and delivers webhooks.
type Service struct {
	db    *gorm.DB
	audit *audit.Service
	http  *http.Client
	log   *slog.Logger
}

// NewService builds the webhooks service.
func NewService(db *gorm.DB, aud *audit.Service, log *slog.Logger) *Service {
	return &Service{
		db: db, audit: aud,
		http: &http.Client{Timeout: 15 * time.Second},
		log:  log.With("component", "webhooks"),
	}
}

// Payload is what a connected system receives.
type Payload struct {
	Event     string    `json:"event"`
	At        time.Time `json:"at"`
	RequestID string    `json:"requestId"`
	Reference string    `json:"reference"`
	Status    string    `json:"status"`
	Priority  string    `json:"priority"`
	Subject   string    `json:"subject"`

	ServiceTypeCode string `json:"serviceTypeCode,omitempty"`
	QueueID         string `json:"queueId,omitempty"`
	DepartmentID    string `json:"departmentId,omitempty"`
	AssigneeUserID  string `json:"assigneeUserId,omitempty"`
	ExternalRef     string `json:"externalRef,omitempty"`

	ContactID   string     `json:"contactId"`
	OpenedAt    time.Time  `json:"openedAt"`
	DueAt       *time.Time `json:"dueAt,omitempty"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
	LastUpdated time.Time  `json:"lastUpdatedAt"`
}

// Publish queues a delivery for every system subscribed to the event.
//
// It implements requests.WebhookPublisher. Only systems that either own the
// request or explicitly subscribe receive it, so a connected system does not
// become a firehose of the whole city's traffic.
func (s *Service) Publish(ctx context.Context, event string, req *domain.Request) error {
	var systems []domain.ConnectedSystem
	err := s.db.WithContext(ctx).
		Where("active = ? AND webhook_url <> ''", true).Find(&systems).Error
	if err != nil {
		return store.Translate(err)
	}
	if len(systems) == 0 {
		return nil
	}

	payload := buildPayload(event, req)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhooks: encode payload: %w", err)
	}

	now := time.Now().UTC()
	var rows []domain.WebhookDelivery

	for _, sys := range systems {
		if !s.subscribed(&sys, event, req) {
			continue
		}
		rows = append(rows, domain.WebhookDelivery{
			SystemID: sys.ID, Event: event, RequestID: req.ID,
			URL: sys.WebhookURL, Payload: string(body),
			State: domain.WebhookPending, NextAttemptAt: now,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return store.Translate(s.db.WithContext(ctx).Create(&rows).Error)
}

// subscribed decides whether a system should hear about this event.
func (s *Service) subscribed(sys *domain.ConnectedSystem, event string, req *domain.Request) bool {
	// The assigned system always hears about its own work, regardless of the
	// subscription list — otherwise it cannot know what it has been given.
	if req.AssigneeSystemID == sys.ID {
		return true
	}
	if len(sys.WebhookEvents) == 0 {
		return false
	}
	if sys.WebhookEvents.Contains("*") || sys.WebhookEvents.Contains(event) {
		// A department-scoped system only hears about its own department.
		return sys.DepartmentID == "" || sys.DepartmentID == req.DepartmentID
	}
	return false
}

func buildPayload(event string, req *domain.Request) Payload {
	p := Payload{
		Event: event, At: time.Now().UTC(),
		RequestID: req.ID, Reference: req.Reference,
		Status: string(req.Status), Priority: req.Priority, Subject: req.Subject,
		QueueID: req.QueueID, DepartmentID: req.DepartmentID,
		AssigneeUserID: req.AssigneeUserID, ExternalRef: req.ExternalRef,
		ContactID: req.ContactID, OpenedAt: req.OpenedAt,
		DueAt: req.DueAt, ResolvedAt: req.ResolvedAt,
		LastUpdated: req.LastActivityA,
	}
	if req.ServiceType != nil {
		p.ServiceTypeCode = req.ServiceType.Code
	}
	return p
}

// DrainResult summarises a delivery pass.
type DrainResult struct {
	Attempted int `json:"attempted"`
	Delivered int `json:"delivered"`
	Retrying  int `json:"retrying"`
	Dead      int `json:"dead"`
}

// Drain attempts the deliveries that are due.
func (s *Service) Drain(ctx context.Context, batch int) (*DrainResult, error) {
	if batch <= 0 || batch > 200 {
		batch = 50
	}

	var rows []domain.WebhookDelivery
	err := s.db.WithContext(ctx).
		Where("state = ? AND next_attempt_at <= ?", domain.WebhookPending, time.Now().UTC()).
		Order("next_attempt_at ASC").Limit(batch).Find(&rows).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	// Cache secrets so a batch of deliveries to one system is not one lookup
	// per row.
	secrets := map[string]string{}
	res := &DrainResult{}

	for i := range rows {
		row := &rows[i]
		res.Attempted++

		secret, ok := secrets[row.SystemID]
		if !ok {
			var sys domain.ConnectedSystem
			if err := s.db.WithContext(ctx).First(&sys, "id = ?", row.SystemID).Error; err == nil {
				secret = sys.WebhookSecret
			}
			secrets[row.SystemID] = secret
		}

		status, err := s.deliver(ctx, row, secret)
		s.applyResult(ctx, row, status, err, res)
	}
	return res, nil
}

// deliver posts one payload with an HMAC signature the receiver can verify.
func (s *Service) deliver(ctx context.Context, row *domain.WebhookDelivery, secret string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, row.URL, bytes.NewReader([]byte(row.Payload)))
	if err != nil {
		return 0, err
	}

	timestamp := fmt.Sprint(time.Now().UTC().Unix())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CityConnect-Webhooks/1")
	req.Header.Set("X-CityConnect-Event", row.Event)
	req.Header.Set("X-CityConnect-Delivery", row.ID)
	req.Header.Set("X-CityConnect-Timestamp", timestamp)
	if secret != "" {
		// Sign timestamp and body together so a captured signature cannot be
		// replayed with a different body or at a later time.
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(timestamp))
		mac.Write([]byte{'.'})
		mac.Write([]byte(row.Payload))
		req.Header.Set("X-CityConnect-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("webhooks: receiver returned %d", resp.StatusCode)
}

func (s *Service) applyResult(ctx context.Context, row *domain.WebhookDelivery, status int, err error, res *DrainResult) {
	attempts := row.Attempts + 1
	updates := map[string]any{"attempts": attempts, "last_status_code": status}

	switch {
	case err == nil:
		updates["state"] = domain.WebhookSent
		updates["delivered_at"] = time.Now().UTC()
		updates["last_error"] = ""
		res.Delivered++

	case status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests:
		// A 4xx means the receiver rejected the payload itself. Retrying the
		// same bytes cannot change that, so it goes straight to the dead
		// letter queue where somebody will actually look at it.
		updates["state"] = domain.WebhookDead
		updates["last_error"] = truncate(err.Error(), 600)
		res.Dead++

	case attempts >= maxAttempts:
		updates["state"] = domain.WebhookDead
		updates["last_error"] = truncate(err.Error(), 600)
		res.Dead++
		s.log.WarnContext(ctx, "webhook dead-lettered after repeated failures",
			"delivery", row.ID, "system", row.SystemID, "attempts", attempts)

	default:
		updates["next_attempt_at"] = time.Now().UTC().Add(backoff(attempts))
		updates["last_error"] = truncate(err.Error(), 600)
		res.Retrying++
	}

	if err := s.db.WithContext(ctx).Model(&domain.WebhookDelivery{}).
		Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		s.log.ErrorContext(ctx, "could not record webhook outcome", "id", row.ID, "error", err)
	}
}

func backoff(attempt int) time.Duration {
	base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	if base > time.Hour {
		base = time.Hour
	}
	return base + time.Duration(rand.Int64N(int64(base/2)))
}

// Filter narrows the delivery log.
type Filter struct {
	SystemID string
	State    string
	Event    string
}

// List returns a page of deliveries — the dead-letter view an admin works from.
func (s *Service) List(ctx context.Context, f Filter, page store.Page) (store.Result[domain.WebhookDelivery], error) {
	q := s.db.WithContext(ctx).Model(&domain.WebhookDelivery{})
	if f.SystemID != "" {
		q = q.Where("system_id = ?", f.SystemID)
	}
	if f.State != "" {
		q = q.Where("state = ?", f.State)
	}
	if f.Event != "" {
		q = q.Where("event = ?", f.Event)
	}

	var rows []domain.WebhookDelivery
	return store.Paginate(q, page, map[string]string{
		"createdAt":   "created_at",
		"deliveredAt": "delivered_at",
		"state":       "state",
	}, "created_at", &rows)
}

// Replay re-queues a delivery as a new attempt, preserving the original for
// the record.
func (s *Service) Replay(ctx context.Context, actor audit.Actor, id string) (*domain.WebhookDelivery, error) {
	var original domain.WebhookDelivery
	if err := s.db.WithContext(ctx).First(&original, "id = ?", id).Error; err != nil {
		return nil, ErrNotFound
	}

	// Re-read the URL: a partner that fixed their endpoint has usually changed
	// it, and replaying to the old address would fail identically.
	url := original.URL
	var sys domain.ConnectedSystem
	if err := s.db.WithContext(ctx).First(&sys, "id = ?", original.SystemID).Error; err == nil && sys.WebhookURL != "" {
		url = sys.WebhookURL
	}

	replay := &domain.WebhookDelivery{
		SystemID: original.SystemID, Event: original.Event,
		RequestID: original.RequestID, URL: url, Payload: original.Payload,
		State: domain.WebhookPending, NextAttemptAt: time.Now().UTC(),
		ReplayOfID: original.ID,
	}
	if err := s.db.WithContext(ctx).Create(replay).Error; err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "webhook.replayed", TargetType: "webhook_delivery", TargetID: original.ID,
		Summary: "replayed " + original.Event,
	})
	return replay, nil
}

// ReplayAllDead re-queues every dead delivery for a system, for use after the
// partner confirms their endpoint is back.
func (s *Service) ReplayAllDead(ctx context.Context, actor audit.Actor, systemID string) (int, error) {
	var dead []domain.WebhookDelivery
	q := s.db.WithContext(ctx).Where("state = ?", domain.WebhookDead)
	if systemID != "" {
		q = q.Where("system_id = ?", systemID)
	}
	if err := q.Limit(1000).Find(&dead).Error; err != nil {
		return 0, store.Translate(err)
	}

	count := 0
	for i := range dead {
		if _, err := s.Replay(ctx, actor, dead[i].ID); err == nil {
			count++
		}
	}
	return count, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
