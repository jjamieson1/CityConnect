package requests

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Filter narrows a request listing. It is the shape a saved view persists, so
// every field must be expressible as JSON.
type Filter struct {
	Query          string     `json:"q,omitempty"`
	Status         []string   `json:"status,omitempty"`
	Priority       []string   `json:"priority,omitempty"`
	QueueID        string     `json:"queueId,omitempty"`
	DepartmentID   string     `json:"departmentId,omitempty"`
	ServiceTypeID  string     `json:"serviceTypeId,omitempty"`
	AssigneeUserID string     `json:"assigneeUserId,omitempty"`
	AssigneeSysID  string     `json:"assigneeSystemId,omitempty"`
	ContactID      string     `json:"contactId,omitempty"`
	Ward           string     `json:"ward,omitempty"`
	Tag            string     `json:"tag,omitempty"`
	Source         string     `json:"source,omitempty"`
	ExternalRef    string     `json:"externalRef,omitempty"`
	OpenOnly       bool       `json:"openOnly,omitempty"`
	Unassigned     bool       `json:"unassigned,omitempty"`
	Breached       bool       `json:"breached,omitempty"`
	DueBefore      *time.Time `json:"dueBefore,omitempty"`
	OpenedAfter    *time.Time `json:"openedAfter,omitempty"`
	OpenedBefore   *time.Time `json:"openedBefore,omitempty"`
	ExcludeMerged  bool       `json:"excludeMerged,omitempty"`
}

var requestSortColumns = map[string]string{
	"reference":  "reference",
	"createdAt":  "created_at",
	"openedAt":   "opened_at",
	"updatedAt":  "last_activity_at",
	"dueAt":      "due_at",
	"priority":   "priority",
	"status":     "status",
	"subject":    "subject",
}

// List returns a page of requests.
func (s *Service) List(ctx context.Context, f Filter, page store.Page) (store.Result[domain.Request], error) {
	q := s.query(ctx, f).
		Preload("Contact").Preload("ServiceType").Preload("Queue").
		Preload("AssigneeUser").Preload("Department")

	var rows []domain.Request
	return store.Paginate(q, page, requestSortColumns, "last_activity_at", &rows)
}

func (s *Service) query(ctx context.Context, f Filter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&domain.Request{})

	if f.Query != "" {
		like := "%" + store.LikeEscape(f.Query) + "%"
		q = q.Where("subject LIKE ? OR description LIKE ? OR reference LIKE ? OR external_ref LIKE ?",
			like, like, like, like)
	}
	if len(f.Status) > 0 {
		q = q.Where("status IN ?", f.Status)
	}
	if len(f.Priority) > 0 {
		q = q.Where("priority IN ?", f.Priority)
	}
	if f.QueueID != "" {
		q = q.Where("queue_id = ?", f.QueueID)
	}
	if f.DepartmentID != "" {
		q = q.Where("department_id = ?", f.DepartmentID)
	}
	if f.ServiceTypeID != "" {
		q = q.Where("service_type_id = ?", f.ServiceTypeID)
	}
	if f.AssigneeUserID != "" {
		q = q.Where("assignee_user_id = ?", f.AssigneeUserID)
	}
	if f.AssigneeSysID != "" {
		q = q.Where("assignee_system_id = ?", f.AssigneeSysID)
	}
	if f.ContactID != "" {
		q = q.Where("contact_id = ?", f.ContactID)
	}
	if f.Ward != "" {
		q = q.Where("ward = ?", f.Ward)
	}
	if f.Source != "" {
		q = q.Where("source = ?", f.Source)
	}
	if f.ExternalRef != "" {
		q = q.Where("external_ref = ?", f.ExternalRef)
	}
	if f.Tag != "" {
		q = q.Where("tags LIKE ?", "%\""+store.LikeEscape(strings.ToLower(f.Tag))+"\"%")
	}
	if f.OpenOnly {
		q = q.Where("status NOT IN ?", []domain.RequestStatus{domain.StatusClosed, domain.StatusCancelled})
	}
	if f.Unassigned {
		q = q.Where("(assignee_user_id = '' OR assignee_user_id IS NULL) AND (assignee_system_id = '' OR assignee_system_id IS NULL)")
	}
	if f.Breached {
		q = q.Where("sla_breached = ?", true)
	}
	if f.DueBefore != nil {
		q = q.Where("due_at IS NOT NULL AND due_at <= ?", *f.DueBefore)
	}
	if f.OpenedAfter != nil {
		q = q.Where("opened_at >= ?", *f.OpenedAfter)
	}
	if f.OpenedBefore != nil {
		q = q.Where("opened_at <= ?", *f.OpenedBefore)
	}
	if f.ExcludeMerged {
		q = q.Where("merged_into_id = '' OR merged_into_id IS NULL")
	}
	return q
}

// Count returns how many requests match a filter, for badge counts.
func (s *Service) Count(ctx context.Context, f Filter) (int64, error) {
	var n int64
	err := s.query(ctx, f).Count(&n).Error
	return n, store.Translate(err)
}

// OpenForContact returns a contact's open requests, newest activity first.
// This is what the Service Card callout renders.
func (s *Service) OpenForContact(ctx context.Context, contactID string, limit int) ([]domain.Request, error) {
	if limit <= 0 {
		limit = 25
	}
	var out []domain.Request
	err := s.db.WithContext(ctx).
		Preload("ServiceType").Preload("Department").
		Where("contact_id = ? AND status NOT IN ?", contactID,
			[]domain.RequestStatus{domain.StatusClosed, domain.StatusCancelled}).
		Where("merged_into_id = '' OR merged_into_id IS NULL").
		Order("last_activity_at DESC").Limit(limit).Find(&out).Error
	return out, store.Translate(err)
}

// UpdateInput carries an edit to a request's descriptive fields. Nil fields
// are left untouched.
type UpdateInput struct {
	Subject         *string
	Description     *string
	Priority        *string
	Address1        *string
	Address2        *string
	City            *string
	State           *string
	PostalCode      *string
	Ward            *string
	ParcelID        *string
	Latitude        *float64
	Longitude       *float64
	Tags            *[]string
	FormData        domain.JSONMap
	ExpectedVersion uint
}

// Update edits a request's fields under optimistic concurrency.
func (s *Service) Update(ctx context.Context, actor audit.Actor, id string, in UpdateInput) (*domain.Request, error) {
	req, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.ExpectedVersion != 0 && req.Version != in.ExpectedVersion {
		return nil, fmt.Errorf("%w: expected version %d, found %d", ErrStale, in.ExpectedVersion, req.Version)
	}

	updates := map[string]any{"last_activity_at": time.Now().UTC(), "version": req.Version + 1}
	changes := domain.JSONMap{}

	setStr := func(col string, ptr *string, old string) {
		if ptr != nil && *ptr != old {
			updates[col] = *ptr
			changes[col] = []string{old, *ptr}
		}
	}
	setStr("subject", in.Subject, req.Subject)
	setStr("description", in.Description, req.Description)
	setStr("address1", in.Address1, req.Address1)
	setStr("address2", in.Address2, req.Address2)
	setStr("city", in.City, req.City)
	setStr("state", in.State, req.State)
	setStr("postal_code", in.PostalCode, req.PostalCode)
	setStr("ward", in.Ward, req.Ward)
	setStr("parcel_id", in.ParcelID, req.ParcelID)

	if in.Latitude != nil {
		updates["latitude"] = *in.Latitude
	}
	if in.Longitude != nil {
		updates["longitude"] = *in.Longitude
	}
	if in.Tags != nil {
		updates["tags"] = domain.StringList(*in.Tags).Normalized()
	}

	// Changing priority moves the deadline, since the target is priority-
	// dependent. Leaving the old deadline would silently misreport the SLA.
	if in.Priority != nil && *in.Priority != req.Priority {
		if domain.PriorityRank(*in.Priority) == 0 {
			return nil, fmt.Errorf("%w: unknown priority %q", ErrInvalidInput, *in.Priority)
		}
		updates["priority"] = *in.Priority
		changes["priority"] = []string{req.Priority, *in.Priority}

		if req.SLAPolicyID != "" {
			targets, err := s.catalog.ComputeTargets(ctx, req.SLAPolicyID, *in.Priority, req.OpenedAt)
			if err == nil && targets != nil {
				updates["due_at"] = targets.DueAt
				updates["response_due_at"] = targets.ResponseDueAt
			}
		}
	}

	if in.FormData != nil {
		if req.ServiceType != nil {
			cleaned, err := validateAgainstType(req, in.FormData)
			if err != nil {
				return nil, err
			}
			updates["form_data"] = cleaned
		} else {
			updates["form_data"] = in.FormData
		}
	}

	if len(changes) == 0 && len(updates) == 2 {
		return req, nil
	}

	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		res := tx.Model(&domain.Request{}).
			Where("id = ? AND version = ?", id, req.Version).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrStale
		}
		if len(changes) == 0 {
			return nil
		}
		return s.addEvent(ctx, tx, id, domain.EvtFieldsUpdated, actor,
			"request details updated", "", "", changes, false)
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	if len(changes) > 0 {
		s.audit.Record(ctx, actor, audit.Entry{
			Action: "request.updated", TargetType: "request", TargetID: id,
			Summary: req.Reference, Changes: changes,
		})
	}
	return s.Get(ctx, id)
}

// BulkAction applies one operation across many requests.
//
// Bulk work is what makes a queue console usable: after a storm, an agent has
// two hundred flooding reports to move at once, and doing that one at a time
// is not a workflow anybody follows.
type BulkAction struct {
	RequestIDs []string
	Operation  string // assign | transition | tag | priority | queue
	UserID     string
	SystemID   string
	Status     domain.RequestStatus
	Priority   string
	QueueID    string
	Tags       []string
	Note       string
}

// BulkResult reports per-request outcomes so a partial failure is visible
// rather than swallowed.
type BulkResult struct {
	Succeeded []string          `json:"succeeded"`
	Failed    map[string]string `json:"failed"`
}

// Bulk applies an operation to many requests, continuing past individual
// failures.
func (s *Service) Bulk(ctx context.Context, actor audit.Actor, in BulkAction) (*BulkResult, error) {
	if len(in.RequestIDs) == 0 {
		return nil, fmt.Errorf("%w: no requests selected", ErrInvalidInput)
	}
	if len(in.RequestIDs) > 500 {
		return nil, fmt.Errorf("%w: bulk actions are limited to 500 requests at a time", ErrInvalidInput)
	}

	res := &BulkResult{Failed: map[string]string{}}

	for _, id := range in.RequestIDs {
		var err error
		switch in.Operation {
		case "assign":
			_, err = s.Assign(ctx, actor, id, AssignInput{UserID: in.UserID, SystemID: in.SystemID})
		case "transition":
			_, err = s.Transition(ctx, actor, id, TransitionInput{To: in.Status, Note: in.Note})
		case "priority":
			p := in.Priority
			_, err = s.Update(ctx, actor, id, UpdateInput{Priority: &p})
		case "queue":
			_, err = s.Transfer(ctx, actor, id, TransferInput{QueueID: in.QueueID, Note: in.Note})
		case "tag":
			err = s.addTags(ctx, id, in.Tags)
		default:
			return nil, fmt.Errorf("%w: unknown bulk operation %q", ErrInvalidInput, in.Operation)
		}

		if err != nil {
			res.Failed[id] = err.Error()
			continue
		}
		res.Succeeded = append(res.Succeeded, id)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "request.bulk_" + in.Operation, TargetType: "request",
		Summary: fmt.Sprintf("%d succeeded, %d failed", len(res.Succeeded), len(res.Failed)),
	})
	return res, nil
}

func (s *Service) addTags(ctx context.Context, id string, tags []string) error {
	req, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	merged := req.Tags
	for _, t := range domain.StringList(tags).Normalized() {
		if !merged.Contains(t) {
			merged = append(merged, t)
		}
	}
	return store.Translate(s.db.WithContext(ctx).Model(&domain.Request{}).
		Where("id = ?", id).UpdateColumn("tags", merged).Error)
}

func validateAgainstType(req *domain.Request, data domain.JSONMap) (domain.JSONMap, error) {
	if req.ServiceType == nil {
		return data, nil
	}
	return catalog.ValidateFormData(req.ServiceType, data)
}
