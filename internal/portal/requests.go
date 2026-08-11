package portal

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// CatalogEntry is a service a citizen may request. It is a deliberately narrow
// projection of ServiceType: the internal queue, SLA policy and routing
// configuration are none of the public's business.
type CatalogEntry struct {
	ID          string             `json:"id"`
	Code        string             `json:"code"`
	Name        string             `json:"name"`
	Category    string             `json:"category,omitempty"`
	Description string             `json:"description,omitempty"`
	Department  string             `json:"department,omitempty"`
	NeedsPlace  bool               `json:"requiresLocation"`
	Fields      []domain.FormField `json:"fields"`
}

// Catalog returns the services a citizen can report, grouped by category in
// the order the console configured.
func (s *Service) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	types, err := s.catalog.ListServiceTypes(ctx, catalog.ServiceTypeFilter{PublicOnly: true})
	if err != nil {
		return nil, err
	}

	out := make([]CatalogEntry, 0, len(types))
	for i := range types {
		st := &types[i]
		fields, err := catalog.ParseForm(st.IntakeForm)
		if err != nil {
			// A malformed form must not remove the service from the catalogue;
			// it degrades to subject and description.
			s.log.WarnContext(ctx, "service type has a malformed intake form",
				"code", st.Code, "error", err)
			fields = nil
		}
		entry := CatalogEntry{
			ID: st.ID, Code: st.Code, Name: st.Name, Category: st.Category,
			Description: st.Description, NeedsPlace: st.RequiresLocation,
			Fields: fields,
		}
		if entry.Fields == nil {
			entry.Fields = []domain.FormField{}
		}
		if st.Department != nil {
			entry.Department = firstNonEmpty(st.Department.PublicName, st.Department.Name)
		}
		out = append(out, entry)
	}
	return out, nil
}

// MyRequest is a citizen's view of their own request.
//
// It is a projection rather than the domain object: a citizen sees status,
// dates and what was done, never the queue, the assignee, internal tags or
// the SLA bookkeeping.
type MyRequest struct {
	Reference   string     `json:"reference"`
	Subject     string     `json:"subject"`
	Description string     `json:"description,omitempty"`
	ServiceType string     `json:"serviceType,omitempty"`
	Department  string     `json:"department,omitempty"`
	Status      string     `json:"status"`
	StatusLabel string     `json:"statusLabel"`
	Open        bool       `json:"open"`
	Address     string     `json:"address,omitempty"`
	OpenedAt    time.Time  `json:"openedAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	ExpectedBy  *time.Time `json:"expectedBy,omitempty"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`

	Resolution string `json:"resolution,omitempty"`
	CanCancel  bool   `json:"canCancel"`
	CanComment bool   `json:"canComment"`
	CanRate    bool   `json:"canRate"`
	CSATScore  *int   `json:"csatScore,omitempty"`

	Updates []MyUpdate `json:"updates,omitempty"`
}

// MyUpdate is one entry in the citizen-visible history.
type MyUpdate struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Body   string    `json:"body"`
	Author string    `json:"author,omitempty"`
	Mine   bool      `json:"mine"`
}

// MyRequests lists everything this contact has reported.
func (s *Service) MyRequests(ctx context.Context, contactID string, openOnly bool) ([]MyRequest, error) {
	filter := requests.Filter{ContactID: contactID, OpenOnly: openOnly, ExcludeMerged: false}
	res, err := s.requests.List(ctx, filter, store.Page{Limit: 100, SortBy: "updatedAt", Desc: true})
	if err != nil {
		return nil, err
	}

	out := make([]MyRequest, 0, len(res.Items))
	for i := range res.Items {
		out = append(out, s.project(&res.Items[i]))
	}
	return out, nil
}

// Request loads one of the caller's requests by reference.
//
// The contact id comes from the session and is compared here, so quoting
// somebody else's reference returns not-found rather than their data. It is
// deliberately not distinguishable from a reference that does not exist —
// otherwise the endpoint would confirm which references are real.
func (s *Service) Request(ctx context.Context, contactID, reference string) (*MyRequest, error) {
	req, err := s.requests.GetByReference(ctx, reference)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.owns(ctx, contactID, req) {
		return nil, ErrNotFound
	}

	view := s.project(req)
	updates, err := s.updatesFor(ctx, req)
	if err != nil {
		return nil, err
	}
	view.Updates = updates
	return &view, nil
}

// owns reports whether a request belongs to this contact, following a merge so
// history survives one.
func (s *Service) owns(ctx context.Context, contactID string, req *domain.Request) bool {
	if req.ContactID == contactID {
		return true
	}
	contact, err := s.contacts.Get(ctx, req.ContactID)
	if err != nil {
		return false
	}
	return contact.MergedIntoID == contactID
}

func (s *Service) project(r *domain.Request) MyRequest {
	view := MyRequest{
		Reference: r.Reference, Subject: r.Subject, Description: r.Description,
		Status: string(r.Status), StatusLabel: catalog.StatusLabel(r.Status),
		Open:      r.Status.Open(),
		OpenedAt:  r.OpenedAt, UpdatedAt: r.LastActivityA,
		ResolvedAt: r.ResolvedAt, Resolution: r.ResolutionNote,
		CSATScore: r.CSATScore,
	}
	if r.ServiceType != nil {
		view.ServiceType = r.ServiceType.Name
	}
	if r.Department != nil {
		view.Department = firstNonEmpty(r.Department.PublicName, r.Department.Name)
	}
	if addr := strings.TrimSpace(strings.Join(nonEmpty(r.Address1, r.City, r.PostalCode), ", ")); addr != "" {
		view.Address = addr
	}
	// The deadline is shown only while it still means something.
	if r.Status.Open() {
		view.ExpectedBy = r.DueAt
	}

	view.CanComment = r.Status.Open()
	// Cancelling is for a report the citizen no longer needs. Once a crew has
	// started, withdrawing it is a conversation, not a button.
	view.CanCancel = r.Status == domain.StatusNew || r.Status == domain.StatusTriaged
	view.CanRate = (r.Status == domain.StatusResolved || r.Status == domain.StatusClosed) && r.CSATScore == nil
	return view
}

// updatesFor assembles the citizen-visible history.
//
// Internal comments and internal-only timeline events are excluded at the
// query, not filtered afterwards: a projection that has already loaded private
// notes is one refactor away from leaking them.
func (s *Service) updatesFor(ctx context.Context, r *domain.Request) ([]MyUpdate, error) {
	var comments []domain.RequestComment
	err := s.db.WithContext(ctx).
		Where("request_id = ? AND visibility = ?", r.ID, domain.VisibilityCitizen).
		Order("created_at ASC").Find(&comments).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	out := make([]MyUpdate, 0, len(comments)+4)
	for _, c := range comments {
		mine := c.AuthorType == "citizen"
		author := c.AuthorName
		if mine {
			author = "You"
		} else if author == "" {
			author = "The City"
		}
		out = append(out, MyUpdate{
			At: c.CreatedAt, Kind: "note", Body: c.Body, Author: author, Mine: mine,
		})
	}

	var events []domain.RequestEvent
	err = s.db.WithContext(ctx).
		Where("request_id = ? AND citizen_visible = ? AND kind IN ?", r.ID, true,
			[]string{domain.EvtCreated, domain.EvtStatusChanged}).
		Order("created_at ASC").Find(&events).Error
	if err != nil {
		return nil, store.Translate(err)
	}
	for _, ev := range events {
		body := ev.Summary
		if ev.Kind == domain.EvtStatusChanged && ev.ToValue != "" {
			body = "Status changed to " + strings.ToLower(catalog.StatusLabel(domain.RequestStatus(ev.ToValue)))
		}
		if ev.Kind == domain.EvtCreated {
			body = "Report received"
		}
		out = append(out, MyUpdate{At: ev.CreatedAt, Kind: "status", Body: body, Author: "The City"})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

// CreateInput is a citizen's report.
type CreateInput struct {
	ServiceTypeID string
	Subject       string
	Description   string
	Address1      string
	City          string
	PostalCode    string
	Ward          string
	FormData      domain.JSONMap
}

// Create files a request on the citizen's own behalf.
//
// The contact id comes from the session, and the source is fixed here, so a
// portal submission can never be attributed to another resident or dressed up
// as having arrived through a trusted channel.
func (s *Service) Create(ctx context.Context, contact *domain.Contact, in CreateInput) (*MyRequest, error) {
	if strings.TrimSpace(in.ServiceTypeID) == "" {
		return nil, fmt.Errorf("%w: choose a service", ErrInvalidInput)
	}

	st, err := s.catalog.GetServiceType(ctx, in.ServiceTypeID)
	if err != nil {
		return nil, fmt.Errorf("%w: unknown service", ErrInvalidInput)
	}
	// Only the public catalogue is reportable. An internal-only service type
	// must not be filed against just because its id was guessed.
	if !st.Active || !st.PublicVisible {
		return nil, fmt.Errorf("%w: that service is not available online", ErrInvalidInput)
	}

	req, err := s.requests.Create(ctx, audit.C2Actor(contact.ID), requests.CreateInput{
		ContactID: contact.ID, ServiceTypeID: st.ID,
		Subject: in.Subject, Description: in.Description,
		Address1: in.Address1, City: in.City, PostalCode: in.PostalCode, Ward: in.Ward,
		FormData: in.FormData,
		Source:   domain.SourceC2Card,
	})
	if err != nil {
		return nil, err
	}

	view := s.project(req)
	return &view, nil
}

// Comment adds a citizen's reply to their own request.
func (s *Service) Comment(ctx context.Context, contact *domain.Contact, reference, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("%w: write something first", ErrInvalidInput)
	}
	if len(body) > 5000 {
		return fmt.Errorf("%w: that message is too long", ErrInvalidInput)
	}

	req, err := s.requests.GetByReference(ctx, reference)
	if err != nil || !s.owns(ctx, contact.ID, req) {
		return ErrNotFound
	}
	if !req.Status.Open() {
		return fmt.Errorf("%w: this request is closed", ErrNotPermitted)
	}

	comment := &domain.RequestComment{
		RequestID: req.ID, AuthorType: "citizen", AuthorID: contact.ID,
		AuthorName: contact.DisplayName,
		// A citizen's own words are visible to them by definition.
		Visibility: domain.VisibilityCitizen, Body: body,
	}

	return store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Create(comment).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Model(&domain.Request{}).Where("id = ?", req.ID).
			Updates(map[string]any{"last_activity_at": now}).Error; err != nil {
			return err
		}
		// A reply is a signal that the citizen is no longer the blocker, so a
		// request parked on them returns to the queue rather than sitting in
		// a waiting state nobody revisits.
		if req.Status == domain.StatusWaitingCitizen {
			if err := tx.Model(&domain.Request{}).Where("id = ?", req.ID).
				UpdateColumn("status", domain.StatusInProgress).Error; err != nil {
				return err
			}
		}
		return tx.Create(&domain.RequestEvent{
			RequestID: req.ID, Kind: domain.EvtCommented,
			ActorType: "citizen", ActorID: contact.ID, ActorName: contact.DisplayName,
			Summary: "the citizen replied", CitizenVis: true,
		}).Error
	})
}

// Cancel withdraws a report the citizen no longer needs.
func (s *Service) Cancel(ctx context.Context, contact *domain.Contact, reference, reason string) error {
	req, err := s.requests.GetByReference(ctx, reference)
	if err != nil || !s.owns(ctx, contact.ID, req) {
		return ErrNotFound
	}
	if req.Status != domain.StatusNew && req.Status != domain.StatusTriaged {
		return fmt.Errorf("%w: work has already started on this request; reply instead and we will follow up", ErrNotPermitted)
	}

	note := strings.TrimSpace(reason)
	if note == "" {
		note = "Withdrawn by the person who reported it."
	}
	_, err = s.requests.Transition(ctx, audit.C2Actor(contact.ID), req.ID, requests.TransitionInput{
		To: domain.StatusCancelled, Note: note, ResolutionCode: "withdrawn",
	})
	return err
}

// Rate records a satisfaction score.
//
// This is the other half of the CSAT survey: the scheduler sends it, and until
// now nothing could record an answer, so the satisfaction report could only
// ever read zero.
func (s *Service) Rate(ctx context.Context, contact *domain.Contact, reference string, score int, comment string) error {
	if score < 1 || score > 5 {
		return fmt.Errorf("%w: a rating is 1 to 5", ErrInvalidInput)
	}

	req, err := s.requests.GetByReference(ctx, reference)
	if err != nil || !s.owns(ctx, contact.ID, req) {
		return ErrNotFound
	}
	if req.Status != domain.StatusResolved && req.Status != domain.StatusClosed {
		return fmt.Errorf("%w: this request is not finished yet", ErrNotPermitted)
	}
	if req.CSATScore != nil {
		return fmt.Errorf("%w: you have already rated this request", ErrNotPermitted)
	}

	err = s.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", req.ID).
		Updates(map[string]any{
			"csat_score":   score,
			"csat_comment": truncate(strings.TrimSpace(comment), 2000),
		}).Error
	if err != nil {
		return store.Translate(err)
	}

	s.db.WithContext(ctx).Create(&domain.RequestEvent{
		RequestID: req.ID, Kind: "csat_received",
		ActorType: "citizen", ActorID: contact.ID, ActorName: contact.DisplayName,
		Summary: fmt.Sprintf("rated %d out of 5", score),
	})
	return nil
}

// Profile is what the portal shows about the signed-in citizen.
type Profile struct {
	Name         string `json:"name"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	Address      string `json:"address,omitempty"`
	OpenRequests int    `json:"openRequests"`
}

// Profile summarises the caller.
func (s *Service) Profile(ctx context.Context, contact *domain.Contact) (*Profile, error) {
	open, err := s.requests.Count(ctx, requests.Filter{ContactID: contact.ID, OpenOnly: true})
	if err != nil {
		return nil, err
	}
	return &Profile{
		Name:  contact.DisplayName,
		Email: contact.PrimaryEmail, Phone: contact.PrimaryPhone,
		Address:      strings.Join(nonEmpty(contact.Address1, contact.City, contact.PostalCode), ", "),
		OpenRequests: int(open),
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(vals ...string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
