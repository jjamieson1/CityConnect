// Package interactions logs touchpoints with a contact — calls taken, emails
// sent, counter visits — and assembles the unified timeline that merges them
// with service-request activity.
package interactions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var (
	ErrNotFound     = errors.New("interactions: not found")
	ErrInvalidInput = errors.New("interactions: invalid input")
)

// Service implements interaction logging.
type Service struct {
	db    *gorm.DB
	audit *audit.Service
	log   *slog.Logger
}

// NewService builds the interactions service.
func NewService(db *gorm.DB, aud *audit.Service, log *slog.Logger) *Service {
	return &Service{db: db, audit: aud, log: log.With("component", "interactions")}
}

// Filter narrows an interaction listing.
type Filter struct {
	ContactID    string
	RequestID    string
	UserID       string
	DepartmentID string
	Kind         string
	Direction    string
	Since        *time.Time
	Until        *time.Time
	Query        string
}

// List returns a page of interactions.
func (s *Service) List(ctx context.Context, f Filter, page store.Page) (store.Result[domain.Interaction], error) {
	q := s.db.WithContext(ctx).Model(&domain.Interaction{}).Preload("User")

	if f.ContactID != "" {
		q = q.Where("contact_id = ?", f.ContactID)
	}
	if f.RequestID != "" {
		q = q.Where("request_id = ?", f.RequestID)
	}
	if f.UserID != "" {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.DepartmentID != "" {
		q = q.Where("department_id = ?", f.DepartmentID)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Direction != "" {
		q = q.Where("direction = ?", f.Direction)
	}
	if f.Since != nil {
		q = q.Where("occurred_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("occurred_at <= ?", *f.Until)
	}
	if f.Query != "" {
		like := "%" + store.LikeEscape(f.Query) + "%"
		q = q.Where("subject LIKE ? OR summary LIKE ?", like, like)
	}

	var rows []domain.Interaction
	return store.Paginate(q, page, map[string]string{
		"occurredAt": "occurred_at",
		"createdAt":  "created_at",
		"kind":       "kind",
	}, "occurred_at", &rows)
}

// Get loads one interaction.
func (s *Service) Get(ctx context.Context, id string) (*domain.Interaction, error) {
	var it domain.Interaction
	err := s.db.WithContext(ctx).Preload("User").Preload("Contact").First(&it, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &it, store.Translate(err)
}

// Create logs an interaction.
func (s *Service) Create(ctx context.Context, actor audit.Actor, it *domain.Interaction) (*domain.Interaction, error) {
	if it.ContactID == "" {
		return nil, fmt.Errorf("%w: contactId is required", ErrInvalidInput)
	}
	if it.Kind == "" {
		it.Kind = domain.InteractionNote
	}
	if it.Direction == "" {
		it.Direction = domain.DirectionInbound
	}
	if it.OccurredAt.IsZero() {
		it.OccurredAt = time.Now().UTC()
	}
	if it.UserID == "" && actor.Type == audit.ActorUser {
		it.UserID = actor.ID
	}
	it.Tags = it.Tags.Normalized()

	if err := s.db.WithContext(ctx).Create(it).Error; err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "interaction.logged", TargetType: "contact", TargetID: it.ContactID,
		Summary: fmt.Sprintf("%s %s: %s", it.Direction, it.Kind, truncate(it.Subject, 120)),
	})
	return s.Get(ctx, it.ID)
}

// Update edits an interaction's narrative fields.
func (s *Service) Update(ctx context.Context, actor audit.Actor, id string, in *domain.Interaction) (*domain.Interaction, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	updates := map[string]any{
		"subject":          in.Subject,
		"summary":          in.Summary,
		"kind":             in.Kind,
		"direction":        in.Direction,
		"outcome":          in.Outcome,
		"duration_seconds": in.DurationSeconds,
		"tags":             in.Tags.Normalized(),
	}
	if !in.OccurredAt.IsZero() {
		updates["occurred_at"] = in.OccurredAt
	}

	if err := s.db.WithContext(ctx).Model(&domain.Interaction{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "interaction.updated", TargetType: "contact", TargetID: existing.ContactID,
		Summary: "edited an interaction log",
	})
	return s.Get(ctx, id)
}

// Delete removes an interaction.
func (s *Service) Delete(ctx context.Context, actor audit.Actor, id string) error {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Delete(&domain.Interaction{}, "id = ?", id).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "interaction.deleted", TargetType: "contact", TargetID: existing.ContactID,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Unified timeline
// ---------------------------------------------------------------------------

// TimelineEntry is one item in a contact's combined history. Interactions and
// request activity are separate tables but one story, and an agent picking up
// a call needs them interleaved, not in two panes.
type TimelineEntry struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"` // interaction | request_event | comment | notification
	At        time.Time      `json:"at"`
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	ActorName string         `json:"actorName,omitempty"`
	RequestID string         `json:"requestId,omitempty"`
	Reference string         `json:"reference,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Detail    domain.JSONMap `json:"detail,omitempty"`
}

// Timeline assembles a contact's history across interactions, request events,
// citizen-visible comments and notifications.
func (s *Service) Timeline(ctx context.Context, contactID string, limit int) ([]TimelineEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := make([]TimelineEntry, 0, limit)

	var logs []domain.Interaction
	if err := s.db.WithContext(ctx).Preload("User").
		Where("contact_id = ?", contactID).
		Order("occurred_at DESC").Limit(limit).Find(&logs).Error; err != nil {
		return nil, store.Translate(err)
	}
	for _, it := range logs {
		e := TimelineEntry{
			ID: it.ID, Type: "interaction", At: it.OccurredAt,
			Title: string(it.Direction) + " " + string(it.Kind),
			Body:  firstNonEmpty(it.Subject, it.Summary), RequestID: it.RequestID,
			Kind: string(it.Kind),
		}
		if it.User != nil {
			e.ActorName = it.User.Name
		}
		out = append(out, e)
	}

	// Request activity for every request this contact owns.
	var requestIDs []string
	if err := s.db.WithContext(ctx).Model(&domain.Request{}).
		Where("contact_id = ?", contactID).
		Order("last_activity_at DESC").Limit(100).
		Pluck("id", &requestIDs).Error; err != nil {
		return nil, store.Translate(err)
	}

	if len(requestIDs) > 0 {
		refs := map[string]string{}
		type refRow struct{ ID, Reference string }
		var rows []refRow
		s.db.WithContext(ctx).Model(&domain.Request{}).
			Select("id, reference").Where("id IN ?", requestIDs).Scan(&rows)
		for _, r := range rows {
			refs[r.ID] = r.Reference
		}

		var events []domain.RequestEvent
		if err := s.db.WithContext(ctx).
			Where("request_id IN ?", requestIDs).
			Order("created_at DESC").Limit(limit).Find(&events).Error; err != nil {
			return nil, store.Translate(err)
		}
		for _, ev := range events {
			out = append(out, TimelineEntry{
				ID: ev.ID, Type: "request_event", At: ev.CreatedAt,
				Title: ev.Kind, Body: ev.Summary, ActorName: ev.ActorName,
				RequestID: ev.RequestID, Reference: refs[ev.RequestID],
				Kind: ev.Kind, Detail: ev.Detail,
			})
		}

		var comments []domain.RequestComment
		if err := s.db.WithContext(ctx).
			Where("request_id IN ? AND visibility = ?", requestIDs, domain.VisibilityCitizen).
			Order("created_at DESC").Limit(limit).Find(&comments).Error; err != nil {
			return nil, store.Translate(err)
		}
		for _, c := range comments {
			out = append(out, TimelineEntry{
				ID: c.ID, Type: "comment", At: c.CreatedAt,
				Title: "update sent to citizen", Body: c.Body,
				ActorName: c.AuthorName, RequestID: c.RequestID, Reference: refs[c.RequestID],
			})
		}
	}

	var notes []domain.NotificationOutbox
	if err := s.db.WithContext(ctx).
		Where("contact_id = ? AND state = ?", contactID, domain.OutboxSent).
		Order("sent_at DESC").Limit(50).Find(&notes).Error; err != nil {
		return nil, store.Translate(err)
	}
	for _, n := range notes {
		at := n.CreatedAt
		if n.SentAt != nil {
			at = *n.SentAt
		}
		out = append(out, TimelineEntry{
			ID: n.ID, Type: "notification", At: at,
			Title: "notification: " + n.Subject, Body: n.Body,
			RequestID: n.RequestID, Kind: n.Event,
			Detail: domain.JSONMap{"channels": n.Channels},
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
