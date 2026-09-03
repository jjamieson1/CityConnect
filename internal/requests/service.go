// Package requests owns the service request: its lifecycle, timeline,
// comments, attachments, links and SLA accounting. It is the centre of the
// application — everything else either feeds it or reports on it.
package requests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/routing"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var (
	ErrNotFound        = errors.New("requests: not found")
	ErrInvalidInput    = errors.New("requests: invalid input")
	ErrConflict        = errors.New("requests: conflict")
	ErrStale           = errors.New("requests: request changed since it was read")
	ErrBadTransition   = errors.New("requests: illegal status transition")
	ErrForbidden       = errors.New("requests: not permitted for this department")
	ErrAlreadyMerged   = errors.New("requests: request has already been merged")
	ErrSelfLink        = errors.New("requests: a request cannot link to itself")
)

// Notifier is the outbound notification hook. It is an interface so the
// requests service does not depend on the notifications package, which
// depends on catalog and contacts — the cycle would otherwise be unavoidable.
type Notifier interface {
	QueueForRequest(ctx context.Context, event string, req *domain.Request, extra map[string]string) error
}

// WebhookPublisher fans request events out to connected systems.
type WebhookPublisher interface {
	Publish(ctx context.Context, event string, req *domain.Request) error
}

// Service implements request management.
type Service struct {
	db        *gorm.DB
	audit     *audit.Service
	catalog   *catalog.Service
	routing   *routing.Service
	notify    Notifier
	hooks     WebhookPublisher
	refPrefix string
	log       *slog.Logger
}

// NewService builds the requests service.
func NewService(
	db *gorm.DB,
	aud *audit.Service,
	cat *catalog.Service,
	route *routing.Service,
	log *slog.Logger,
) *Service {
	return &Service{
		db: db, audit: aud, catalog: cat, routing: route,
		refPrefix: DefaultReferencePrefix,
		log:       log.With("component", "requests"),
	}
}

// SetReferencePrefix sets the prefix new request references carry, so a
// deployment can quote BBY-… rather than the default.
//
// It is a setter rather than a constructor argument because it is optional
// presentation: every deployment gets a working default, and a municipality
// that wants its own initials should not force a signature change on the one
// caller that does not care.
func (s *Service) SetReferencePrefix(prefix string) {
	s.refPrefix = NormalizeReferencePrefix(prefix)
}

// SetNotifier wires the notification hook after construction, breaking the
// dependency cycle between requests and notifications.
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

// SetWebhooks wires the outbound webhook publisher.
func (s *Service) SetWebhooks(w WebhookPublisher) { s.hooks = w }

// Audit exposes the audit service so background jobs can record actions
// against the same chain without holding a second reference to it.
func (s *Service) Audit() *audit.Service { return s.audit }

// DB exposes the handle for the read-only aggregate queries reporting needs.
func (s *Service) DB() *gorm.DB { return s.db }

// CreateInput describes a new service request.
type CreateInput struct {
	ContactID string
	// Channel is one of domain.Channel*. Empty defaults to authenticated,
	// which keeps every existing caller — staff, API, importers — meaning what
	// it meant before: a request filed on behalf of a known contact.
	Channel         string
	ServiceTypeID   string
	ServiceTypeCode string
	Subject         string
	Description     string
	Priority        string
	Source          string
	OriginSystem    string
	ExternalRef     string

	Address1   string
	Address2   string
	City       string
	State      string
	PostalCode string
	Ward       string
	ParcelID   string
	Latitude   float64
	Longitude  float64

	FormData domain.JSONMap
	Tags     []string

	// SkipRouting leaves the request unrouted, for an importer that already
	// knows where its records belong.
	SkipRouting bool
	QueueID     string
}

// Create opens a service request: it validates the intake form, routes the
// request, computes SLA targets, writes the opening timeline entry and queues
// the acknowledgement notification.
func (s *Service) Create(ctx context.Context, actor audit.Actor, in CreateInput) (*domain.Request, error) {
	channel := in.Channel
	if channel == "" {
		channel = domain.ChannelAuthenticated
	}
	if !domain.ValidChannel(channel) {
		return nil, fmt.Errorf("%w: unknown submission channel %q", ErrInvalidInput, channel)
	}

	// The contact and the channel have to agree, and this is the only place
	// that is enforced. An anonymous request carrying a contact id would leak
	// somebody's identity onto a report they were promised was anonymous; an
	// identified one without a contact would be silently unreachable.
	switch {
	case channel == domain.ChannelAnonymous && in.ContactID != "":
		return nil, fmt.Errorf("%w: an anonymous request cannot carry a contact", ErrInvalidInput)
	case channel != domain.ChannelAnonymous && in.ContactID == "":
		return nil, fmt.Errorf("%w: contactId is required", ErrInvalidInput)
	}

	st, err := s.resolveServiceType(ctx, in.ServiceTypeID, in.ServiceTypeCode)
	if err != nil {
		return nil, err
	}
	if !st.Active {
		return nil, fmt.Errorf("%w: service type %q is no longer active", ErrInvalidInput, st.Code)
	}

	formData, err := catalog.ValidateFormData(st, in.FormData)
	if err != nil {
		return nil, err
	}

	subject := strings.TrimSpace(in.Subject)
	if subject == "" {
		subject = st.Name
	}
	priority := in.Priority
	if priority == "" {
		priority = st.DefaultPriority
	}
	if domain.PriorityRank(priority) == 0 {
		return nil, fmt.Errorf("%w: unknown priority %q", ErrInvalidInput, priority)
	}
	source := in.Source
	if source == "" {
		source = domain.SourceAgent
	}
	if st.RequiresLocation && strings.TrimSpace(in.Address1) == "" && in.Latitude == 0 {
		return nil, fmt.Errorf("%w: %s requires a location", ErrInvalidInput, st.Name)
	}

	now := time.Now().UTC()
	req := &domain.Request{
		ContactID:     in.ContactID,
		Channel:       channel,
		ServiceTypeID: st.ID,
		DepartmentID:  st.DepartmentID,
		QueueID:       firstNonEmpty(in.QueueID, st.DefaultQueueID),
		Source:        source,
		OriginSystem:  in.OriginSystem,
		ExternalRef:   in.ExternalRef,
		Status:        domain.StatusNew,
		Priority:      priority,
		Subject:       subject,
		Description:   strings.TrimSpace(in.Description),
		Address1:      in.Address1, Address2: in.Address2,
		City: in.City, State: in.State, PostalCode: in.PostalCode,
		Ward: in.Ward, ParcelID: in.ParcelID,
		Latitude: in.Latitude, Longitude: in.Longitude,
		FormData:      formData,
		Tags:          domain.StringList(in.Tags).Normalized(),
		OpenedAt:      now,
		LastActivityA: now,
		SLAPolicyID:   st.SLAPolicyID,
		Version:       1,
	}

	var decision routing.Decision
	if !in.SkipRouting {
		decision, err = s.routing.Route(ctx, routing.FactsFromRequest(req, st.Code, st.Category))
		if err != nil {
			// Routing failure must not lose the request. It lands unrouted in
			// the default queue, where a supervisor can see and triage it.
			s.log.ErrorContext(ctx, "routing failed; request left for manual triage",
				"service_type", st.Code, "error", err)
		} else {
			applyDecision(req, decision)
		}
	}

	// Auto-assign from the queue's strategy when routing did not name an owner.
	if req.QueueID != "" && !req.Assigned() {
		if pick, err := s.routing.PickAssignee(ctx, req.QueueID); err == nil && pick != nil {
			req.AssigneeUserID, req.AssigneeSystemID = pick.UserID, pick.SystemID
		}
	}
	if req.Assigned() {
		req.Status = domain.StatusAssigned
	}

	if err := s.applySLA(ctx, req, now); err != nil {
		s.log.WarnContext(ctx, "could not compute SLA targets", "error", err)
	}

	summary := "request opened"
	if len(decision.MatchedRules) > 0 {
		names := make([]string, len(decision.MatchedRules))
		for i, m := range decision.MatchedRules {
			names[i] = m.Name
		}
		summary = "request opened; routed by " + strings.Join(names, ", ")
	}

	// The reference is drawn at random rather than allocated in sequence, so a
	// collision is possible in principle and must never reach the resident as a
	// failed submission. The unique index on reference is the arbiter, and
	// another draw is the whole remedy.
	for attempt := 1; ; attempt++ {
		req.Reference, err = NewReference(s.refPrefix)
		if err != nil {
			return nil, err
		}

		err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
			if err := tx.Create(req).Error; err != nil {
				return err
			}
			return s.addEvent(ctx, tx, req.ID, domain.EvtCreated, actor, summary, "", string(req.Status),
				domain.JSONMap{"source": source, "matchedRules": decision.MatchedRules}, true)
		})
		if err == nil {
			break
		}
		if attempt < referenceAttempts && errors.Is(store.Translate(err), store.ErrDuplicate) {
			s.log.WarnContext(ctx, "request reference collided; drawing another",
				"attempt", attempt)
			continue
		}
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "request.created", TargetType: "request", TargetID: req.ID,
		Summary: req.Reference + ": " + req.Subject,
	})

	s.emit(ctx, domain.EventRequestCreated, req, nil)
	return s.Get(ctx, req.ID)
}

func applyDecision(req *domain.Request, d routing.Decision) {
	if d.QueueID != "" {
		req.QueueID = d.QueueID
	}
	if d.DepartmentID != "" {
		req.DepartmentID = d.DepartmentID
	}
	if d.AssigneeID != "" {
		req.AssigneeUserID = d.AssigneeID
	}
	if d.SystemID != "" {
		req.AssigneeSystemID = d.SystemID
	}
	if d.Priority != "" {
		req.Priority = d.Priority
	}
	if d.SLAPolicyID != "" {
		req.SLAPolicyID = d.SLAPolicyID
	}
	for _, tag := range d.AddTags {
		if !req.Tags.Contains(tag) {
			req.Tags = append(req.Tags, tag)
		}
	}
}

func (s *Service) applySLA(ctx context.Context, req *domain.Request, from time.Time) error {
	if req.SLAPolicyID == "" {
		return nil
	}
	targets, err := s.catalog.ComputeTargets(ctx, req.SLAPolicyID, req.Priority, from)
	if err != nil || targets == nil {
		return err
	}
	req.ResponseDueAt = &targets.ResponseDueAt
	req.DueAt = &targets.DueAt
	return nil
}

func (s *Service) resolveServiceType(ctx context.Context, id, code string) (*domain.ServiceType, error) {
	switch {
	case id != "":
		st, err := s.catalog.GetServiceType(ctx, id)
		if errors.Is(err, catalog.ErrNotFound) {
			return nil, fmt.Errorf("%w: unknown service type", ErrInvalidInput)
		}
		return st, err
	case code != "":
		st, err := s.catalog.GetServiceTypeByCode(ctx, code)
		if errors.Is(err, catalog.ErrNotFound) {
			return nil, fmt.Errorf("%w: unknown service type code %q", ErrInvalidInput, code)
		}
		return st, err
	}
	return nil, fmt.Errorf("%w: a service type id or code is required", ErrInvalidInput)
}

// Get loads a request with its associations.
func (s *Service) Get(ctx context.Context, id string) (*domain.Request, error) {
	var r domain.Request
	err := s.db.WithContext(ctx).
		Preload("Contact").Preload("ServiceType").Preload("Queue").
		Preload("Department").Preload("AssigneeUser").Preload("AssigneeSystem").
		First(&r, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &r, store.Translate(err)
}

// GetByReference resolves the identifier a citizen quotes.
func (s *Service) GetByReference(ctx context.Context, reference string) (*domain.Request, error) {
	var r domain.Request
	err := s.db.WithContext(ctx).
		Preload("Contact").Preload("ServiceType").Preload("Queue").Preload("AssigneeUser").
		First(&r, "reference = ?", NormalizeReference(reference)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &r, store.Translate(err)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// emit queues a notification and a webhook for an event, logging rather than
// failing: a request that was successfully written must not be rolled back
// because a downstream fan-out was unavailable.
func (s *Service) emit(ctx context.Context, event string, req *domain.Request, extra map[string]string) {
	if s.notify != nil {
		if err := s.notify.QueueForRequest(ctx, event, req, extra); err != nil {
			s.log.WarnContext(ctx, "could not queue notification",
				"event", event, "request", req.Reference, "error", err)
		}
	}
	if s.hooks != nil {
		if err := s.hooks.Publish(ctx, event, req); err != nil {
			s.log.WarnContext(ctx, "could not publish webhook",
				"event", event, "request", req.Reference, "error", err)
		}
	}
}
