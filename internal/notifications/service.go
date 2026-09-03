// Package notifications owns citizen messaging: template rendering, the
// durable outbox, and the dispatcher that drains it into C2.
//
// Nothing sends synchronously. Every message becomes an outbox row first, so a
// C2 outage degrades into a queue rather than lost messages, the dispatcher
// can pace itself against C2's per-IP rate limit, and every send — including
// the refused ones — leaves a record that reconciles against C2's own audit
// log.
package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/c2/notify"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var (
	ErrNotFound     = errors.New("notifications: not found")
	ErrInvalidInput = errors.New("notifications: invalid input")
	ErrSuppressed   = errors.New("notifications: suppressed")
)

// Service implements the outbox and dispatcher.
type Service struct {
	db       *gorm.DB
	cfg      *config.Config
	client   *notify.Client
	catalog  *catalog.Service
	contacts *contacts.Service
	audit    *audit.Service
	log      *slog.Logger
}

// NewService builds the notifications service.
func NewService(
	db *gorm.DB, cfg *config.Config, client *notify.Client,
	cat *catalog.Service, cont *contacts.Service, aud *audit.Service, log *slog.Logger,
) *Service {
	return &Service{
		db: db, cfg: cfg, client: client, catalog: cat, contacts: cont,
		audit: aud, log: log.With("component", "notifications"),
	}
}

// QueueForRequest renders the template for an event and enqueues it.
//
// It implements requests.Notifier. Failures here are reported but never
// propagate into the caller's transaction: a request that was successfully
// created must not be rolled back because a template was missing.
func (s *Service) QueueForRequest(ctx context.Context, event string, req *domain.Request, extra map[string]string) error {
	// An anonymous report has nobody to notify, and that is the arrangement the
	// resident accepted rather than a failure to record.
	//
	// Returning before the suppression path matters: a suppression says "we
	// meant to reach this person and could not", which an operator is expected
	// to look at. Writing one per event for every anonymous request would bury
	// the real ones — the console would fill with rows nobody can act on.
	if req.Anonymous() {
		return nil
	}

	contact, err := s.contacts.Get(ctx, req.ContactID)
	if err != nil {
		return err
	}

	sub, err := s.c2SubFor(ctx, contact.ID)
	if err != nil {
		// No C2 identity means no channel to reach them through. Recorded as
		// a suppression so the console can show why nothing was sent, rather
		// than leaving a silent gap.
		return s.recordSuppression(ctx, contact, req, event, domain.SuppressNoC2Link)
	}

	allowed, reason, err := s.contacts.MayContact(ctx, contact.ID,
		domain.ConsentServiceUpdates, domain.ChannelEmail)
	if err != nil {
		return err
	}
	if !allowed {
		return s.recordSuppression(ctx, contact, req, event, reason)
	}

	tmpl, err := s.catalog.FindTemplate(ctx, event, req.ServiceTypeID, contact.PreferredLanguage)
	if err != nil {
		s.log.DebugContext(ctx, "no notification template for event", "event", event)
		return nil
	}

	rendered, err := catalog.Render(tmpl, s.buildContext(req, contact, extra))
	if err != nil {
		return fmt.Errorf("notifications: render %s: %w", event, err)
	}

	return s.Enqueue(ctx, EnqueueInput{
		ContactID:  contact.ID,
		RequestID:  req.ID,
		C2Sub:      sub,
		Event:      event,
		Subject:    rendered.Subject,
		Body:       rendered.Body,
		ShortBody:  rendered.ShortBody,
		Category:   rendered.Category,
		TemplateID: tmpl.ID,
	})
}

func (s *Service) buildContext(req *domain.Request, contact *domain.Contact, extra map[string]string) catalog.TemplateContext {
	ctx := catalog.TemplateContext{
		Reference: req.Reference, Subject: req.Subject,
		Status: string(req.Status), StatusLabel: catalog.StatusLabel(req.Status),
		Priority:       req.Priority,
		ContactName:    contact.DisplayName,
		Ward:           req.Ward,
		OpenedAt:       catalog.FormatDate(&req.OpenedAt),
		UpdatedAt:      catalog.FormatDate(&req.LastActivityA),
		DueAt:          catalog.FormatDate(req.DueAt),
		ResolvedAt:     catalog.FormatDate(req.ResolvedAt),
		ResolutionNote: req.ResolutionNote,
		CityName:       "the City",
		PortalURL:      s.cfg.PortalPublicURL,
		RequestURL:     fmt.Sprintf("%s/requests/%s", s.cfg.PortalPublicURL, req.Reference),
	}

	ctx.ContactFirst = contact.GivenName
	if ctx.ContactFirst == "" {
		ctx.ContactFirst = strings.Fields(contact.DisplayName + " ")[0]
	}
	if req.ServiceType != nil {
		ctx.ServiceType = req.ServiceType.Name
	}
	if req.Department != nil {
		ctx.Department = req.Department.Name
		if req.Department.PublicName != "" {
			ctx.Department = req.Department.PublicName
		}
	}
	if req.Queue != nil {
		ctx.Queue = req.Queue.Name
	}
	if req.AssigneeUser != nil {
		ctx.Assignee = req.AssigneeUser.Name
	}
	if addr := strings.TrimSpace(strings.Join([]string{req.Address1, req.City}, ", ")); addr != "," {
		ctx.Address = strings.Trim(addr, ", ")
	}
	for k, v := range extra {
		if k == "comment" {
			ctx.Comment = v
		}
	}
	return ctx
}

// EnqueueInput describes a message to queue.
type EnqueueInput struct {
	ContactID   string
	RequestID   string
	C2Sub       string
	Event       string
	Subject     string
	Body        string
	ShortBody   string
	Category    string
	TemplateID  string
	CreatedByID string
}

// Enqueue adds a message to the outbox.
//
// Identical messages for the same request within a short window are collapsed:
// a burst of status changes should not produce four near-identical emails to
// the same citizen within a minute.
func (s *Service) Enqueue(ctx context.Context, in EnqueueInput) error {
	if in.C2Sub == "" || strings.TrimSpace(in.Subject) == "" || strings.TrimSpace(in.Body) == "" {
		return fmt.Errorf("%w: sub, subject and body are required", ErrInvalidInput)
	}
	if in.Category == "" {
		in.Category = "BUSINESS"
	}

	fingerprint := hashOf(in.C2Sub, in.RequestID, in.Subject, in.Body)

	var dupe int64
	s.db.WithContext(ctx).Model(&domain.NotificationOutbox{}).
		Where("idempotency_hash = ? AND created_at > ?", fingerprint, time.Now().UTC().Add(-5*time.Minute)).
		Count(&dupe)
	if dupe > 0 {
		s.log.DebugContext(ctx, "collapsed a duplicate notification", "event", in.Event)
		return nil
	}

	row := &domain.NotificationOutbox{
		ContactID: in.ContactID, RequestID: in.RequestID, C2Sub: in.C2Sub,
		Event: in.Event, Subject: in.Subject, Body: in.Body,
		ShortBody: in.ShortBody, Category: in.Category, TemplateID: in.TemplateID,
		State: domain.OutboxPending, NextAttemptAt: time.Now().UTC(),
		CreatedByID: in.CreatedByID, IdempotencyHash: fingerprint,
	}
	return store.Translate(s.db.WithContext(ctx).Create(row).Error)
}

func (s *Service) recordSuppression(ctx context.Context, contact *domain.Contact, req *domain.Request, event, reason string) error {
	row := &domain.NotificationOutbox{
		ContactID: contact.ID, C2Sub: "", Event: event,
		Subject: "(suppressed)", Body: "(suppressed)",
		State: domain.OutboxSuppressed, SuppressReason: reason,
		NextAttemptAt: time.Now().UTC(),
	}
	if req != nil {
		row.RequestID = req.ID
		row.Subject = req.Reference + " — " + event
		row.Body = "No notification was sent."
	}
	return store.Translate(s.db.WithContext(ctx).Create(row).Error)
}

func (s *Service) c2SubFor(ctx context.Context, contactID string) (string, error) {
	var ident domain.ContactIdentity
	err := s.db.WithContext(ctx).
		Where("contact_id = ? AND provider = ?", contactID, domain.ProviderC2).
		First(&ident).Error
	if err != nil {
		return "", ErrNotFound
	}
	return ident.ExternalID, nil
}

// ---------------------------------------------------------------------------
// Dispatcher
// ---------------------------------------------------------------------------

// DrainResult summarises one dispatcher pass.
type DrainResult struct {
	Attempted  int `json:"attempted"`
	Sent       int `json:"sent"`
	Suppressed int `json:"suppressed"`
	Retrying   int `json:"retrying"`
	Failed     int `json:"failed"`
}

// Drain sends the messages that are due.
//
// It processes a bounded batch per pass and stops early on a rate limit,
// because C2 limits per source IP and pushing through a 429 only extends the
// backoff for every other message behind it.
func (s *Service) Drain(ctx context.Context, batch int) (*DrainResult, error) {
	if batch <= 0 || batch > 200 {
		batch = 25
	}

	var rows []domain.NotificationOutbox
	err := s.db.WithContext(ctx).
		Where("state = ? AND next_attempt_at <= ?", domain.OutboxPending, time.Now().UTC()).
		Order("next_attempt_at ASC").Limit(batch).Find(&rows).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	res := &DrainResult{}
	for i := range rows {
		row := &rows[i]
		res.Attempted++

		result := s.client.Send(ctx, notify.Request{
			Sub: row.C2Sub, Subject: row.Subject, Body: row.Body,
			ShortBody: row.ShortBody, Category: row.Category,
		})

		s.applyResult(ctx, row, result, res)

		if result.Outcome == notify.OutcomeRetry && result.StatusCode == 429 {
			s.log.WarnContext(ctx, "C2 rate limit reached; pausing this drain pass",
				"remaining", len(rows)-i-1)
			break
		}
	}
	return res, nil
}

func (s *Service) applyResult(ctx context.Context, row *domain.NotificationOutbox, result notify.Result, res *DrainResult) {
	now := time.Now().UTC()
	updates := map[string]any{
		"attempts":         row.Attempts + 1,
		"last_status_code": result.StatusCode,
	}
	if result.Err != nil {
		updates["last_error"] = truncate(result.Err.Error(), 600)
	}

	switch result.Outcome {
	case notify.OutcomeSent:
		updates["state"] = domain.OutboxSent
		updates["sent_at"] = now
		updates["last_error"] = ""
		if result.Response != nil {
			updates["c2_notification_id"] = result.Response.NotificationID
			updates["channels"] = domain.StringList(result.Response.Channels)
		}
		res.Sent++

		// C2 accepted it, so the citizen is reachable again — clear any stale
		// unreachable flag from an earlier refusal.
		if row.ContactID != "" {
			_ = s.contacts.SetC2Reachable(ctx, row.ContactID, true, "")
		}
		s.recordOnTimeline(ctx, row, result)

	case notify.OutcomeNoConsent, notify.OutcomeUnknown:
		reason := domain.SuppressNoConsent
		if result.Outcome == notify.OutcomeUnknown {
			reason = domain.SuppressUnknownSub
		}
		updates["state"] = domain.OutboxSuppressed
		updates["suppress_reason"] = reason
		res.Suppressed++

		// Flag the contact so an agent can see at a glance that this citizen
		// must be phoned or posted rather than notified.
		if row.ContactID != "" {
			_ = s.contacts.SetC2Reachable(ctx, row.ContactID, false, reason)
		}
		s.log.InfoContext(ctx, "notification suppressed by C2",
			"reason", reason, "sub", row.C2Sub, "event", row.Event)

	case notify.OutcomeRejected:
		updates["state"] = domain.OutboxFailed
		updates["suppress_reason"] = "rejected"
		res.Failed++
		s.log.ErrorContext(ctx, "C2 rejected a notification",
			"status", result.StatusCode, "error", result.Err)

	default: // retry
		attempts := row.Attempts + 1
		if attempts >= s.cfg.Job.OutboxMaxAttempts {
			updates["state"] = domain.OutboxFailed
			updates["suppress_reason"] = domain.SuppressMaxAttempts
			res.Failed++
		} else {
			updates["next_attempt_at"] = now.Add(backoff(attempts, result.RetryAfter))
			res.Retrying++
		}
	}

	if err := s.db.WithContext(ctx).Model(&domain.NotificationOutbox{}).
		Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		s.log.ErrorContext(ctx, "could not record notification outcome", "id", row.ID, "error", err)
	}
}

func (s *Service) recordOnTimeline(ctx context.Context, row *domain.NotificationOutbox, result notify.Result) {
	if row.RequestID == "" {
		return
	}
	detail := domain.JSONMap{"event": row.Event}
	if result.Response != nil {
		detail["notificationId"] = result.Response.NotificationID
		detail["channels"] = result.Response.Channels
	}
	ev := domain.RequestEvent{
		RequestID: row.RequestID, Kind: domain.EvtNotificationSnt,
		ActorType: audit.ActorJob, ActorName: "notification dispatcher",
		Summary: "notified the citizen: " + row.Subject, Detail: detail,
		CitizenVis: true,
	}
	if err := s.db.WithContext(ctx).Create(&ev).Error; err != nil {
		s.log.WarnContext(ctx, "could not write notification timeline entry", "error", err)
	}
}

// backoff returns the delay before the next attempt, honouring C2's
// Retry-After when it supplied one and adding jitter so a queue that built up
// during an outage does not stampede on recovery.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter + time.Duration(rand.Int64N(5_000))*time.Millisecond
	}
	base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	if base > 30*time.Minute {
		base = 30 * time.Minute
	}
	jitter := time.Duration(rand.Int64N(int64(base / 2)))
	return base + jitter
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
