package notifications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Filter narrows the delivery log.
type Filter struct {
	ContactID string
	RequestID string
	State     string
	Event     string
	Since     *time.Time
}

// List returns a page of the delivery log. This is the screen an admin opens
// when a citizen says they never heard anything, so it includes suppressed and
// failed rows, not just successful sends.
func (s *Service) List(ctx context.Context, f Filter, page store.Page) (store.Result[domain.NotificationOutbox], error) {
	q := s.db.WithContext(ctx).Model(&domain.NotificationOutbox{})

	if f.ContactID != "" {
		q = q.Where("contact_id = ?", f.ContactID)
	}
	if f.RequestID != "" {
		q = q.Where("request_id = ?", f.RequestID)
	}
	if f.State != "" {
		q = q.Where("state = ?", f.State)
	}
	if f.Event != "" {
		q = q.Where("event = ?", f.Event)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}

	var rows []domain.NotificationOutbox
	return store.Paginate(q, page, map[string]string{
		"createdAt": "created_at",
		"sentAt":    "sent_at",
		"state":     "state",
	}, "created_at", &rows)
}

// Get loads one delivery record.
func (s *Service) Get(ctx context.Context, id string) (*domain.NotificationOutbox, error) {
	var row domain.NotificationOutbox
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, ErrNotFound
	}
	return &row, nil
}

// Retry re-queues a failed message.
//
// Suppressed messages are not retryable through this path: a 403 means the
// citizen withdrew consent, and re-sending until it works is precisely what
// the consent gate exists to prevent. Clearing that state is the citizen's
// action in their C2 portal, not an operator's here.
func (s *Service) Retry(ctx context.Context, actor audit.Actor, id string) error {
	row, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if row.State == domain.OutboxSuppressed {
		return fmt.Errorf("%w: this message was refused by C2's consent gate; "+
			"the citizen must re-consent in their portal", ErrSuppressed)
	}
	if row.State == domain.OutboxSent {
		return fmt.Errorf("%w: already delivered", ErrInvalidInput)
	}

	err = s.db.WithContext(ctx).Model(&domain.NotificationOutbox{}).
		Where("id = ?", id).Updates(map[string]any{
		"state":           domain.OutboxPending,
		"attempts":        0,
		"next_attempt_at": time.Now().UTC(),
		"last_error":      "",
	}).Error
	if err != nil {
		return store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "notification.retried", TargetType: "notification", TargetID: id,
		Summary: row.Subject,
	})
	return nil
}

// SendAdHoc queues a one-off message an agent composed by hand.
func (s *Service) SendAdHoc(ctx context.Context, actor audit.Actor, contactID, requestID, subject, body, shortBody string) error {
	contact, err := s.contacts.Get(ctx, contactID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(body) == "" {
		return fmt.Errorf("%w: subject and body are required", ErrInvalidInput)
	}

	sub, err := s.c2SubFor(ctx, contactID)
	if err != nil {
		return fmt.Errorf("%w: this contact has no C2 identity, so there is no channel to reach them", ErrSuppressed)
	}

	allowed, reason, err := s.contacts.MayContact(ctx, contactID,
		domain.ConsentServiceUpdates, domain.ChannelEmail)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: %s", ErrSuppressed, reason)
	}

	if err := s.Enqueue(ctx, EnqueueInput{
		ContactID: contactID, RequestID: requestID, C2Sub: sub,
		Event: "adhoc", Subject: subject, Body: body, ShortBody: shortBody,
		CreatedByID: actor.ID,
	}); err != nil {
		return err
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "notification.adhoc_sent", TargetType: "contact", TargetID: contactID,
		Summary: "queued a manual notification to " + contact.DisplayName,
	})
	return nil
}

// Stats summarises the outbox for the admin dashboard.
type Stats struct {
	Pending    int64 `json:"pending"`
	Sent       int64 `json:"sent"`
	Suppressed int64 `json:"suppressed"`
	Failed     int64 `json:"failed"`
	Overdue    int64 `json:"overdue"`
}

// Stats returns outbox counts.
func (s *Service) Stats(ctx context.Context) (*Stats, error) {
	out := &Stats{}

	counts := []struct {
		state string
		dest  *int64
	}{
		{domain.OutboxPending, &out.Pending},
		{domain.OutboxSent, &out.Sent},
		{domain.OutboxSuppressed, &out.Suppressed},
		{domain.OutboxFailed, &out.Failed},
	}
	for _, c := range counts {
		if err := s.db.WithContext(ctx).Model(&domain.NotificationOutbox{}).
			Where("state = ?", c.state).Count(c.dest).Error; err != nil {
			return nil, store.Translate(err)
		}
	}

	// Anything pending well past its due time means the dispatcher is stuck or
	// C2 is unreachable — worth surfacing rather than hiding in the queue depth.
	err := s.db.WithContext(ctx).Model(&domain.NotificationOutbox{}).
		Where("state = ? AND next_attempt_at < ?", domain.OutboxPending,
			time.Now().UTC().Add(-15*time.Minute)).
		Count(&out.Overdue).Error
	return out, store.Translate(err)
}
