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

// TransitionInput describes a status change.
type TransitionInput struct {
	To              domain.RequestStatus
	Note            string
	ResolutionCode  string
	NotifyCitizen   bool
	ExpectedVersion uint
}

// Transition moves a request through the status machine.
//
// Illegal moves are rejected rather than written. An unconstrained status
// column is how a workflow stops meaning anything: once "closed → new" is
// possible, no report about cycle time or backlog can be trusted again.
func (s *Service) Transition(ctx context.Context, actor audit.Actor, id string, in TransitionInput) (*domain.Request, error) {
	req, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.MergedIntoID != "" {
		return nil, ErrAlreadyMerged
	}
	if in.ExpectedVersion != 0 && req.Version != in.ExpectedVersion {
		return nil, fmt.Errorf("%w: expected version %d, found %d", ErrStale, in.ExpectedVersion, req.Version)
	}
	if !in.To.Valid() {
		return nil, fmt.Errorf("%w: unknown status %q", ErrInvalidInput, in.To)
	}
	if !req.Status.CanTransitionTo(in.To) {
		return nil, fmt.Errorf("%w: %s cannot move to %s", ErrBadTransition, req.Status, in.To)
	}
	if in.To == domain.StatusResolved && strings.TrimSpace(in.ResolutionCode) == "" && strings.TrimSpace(in.Note) == "" {
		return nil, fmt.Errorf("%w: resolving a request needs a resolution code or note", ErrInvalidInput)
	}

	now := time.Now().UTC()
	from := req.Status
	reopening := from.Reopening(in.To)

	updates := map[string]any{
		"status":           in.To,
		"last_activity_at": now,
		"version":          req.Version + 1,
	}

	// The SLA clock pauses while the city is waiting on someone else and
	// resumes when work restarts, so a request held for a citizen's reply does
	// not breach through no fault of staff.
	paused, err := s.isPauseStatus(ctx, req, in.To)
	if err != nil {
		return nil, err
	}
	wasPaused, err := s.isPauseStatus(ctx, req, from)
	if err != nil {
		return nil, err
	}

	switch {
	case paused && !wasPaused:
		updates["paused_at"] = now
	case !paused && wasPaused && req.PausedAt != nil:
		elapsed, err := s.businessMinutes(ctx, req, *req.PausedAt, now)
		if err == nil {
			updates["paused_minutes"] = req.PausedMinutes + elapsed
			if req.DueAt != nil {
				if shifted, err := s.shiftDeadline(ctx, req, *req.DueAt, elapsed); err == nil {
					updates["due_at"] = shifted
				}
			}
		}
		updates["paused_at"] = nil
	}

	switch in.To {
	case domain.StatusResolved:
		updates["resolved_at"] = now
		if in.ResolutionCode != "" {
			updates["resolution_code"] = in.ResolutionCode
		}
		if in.Note != "" {
			updates["resolution_note"] = in.Note
		}
	case domain.StatusClosed:
		updates["closed_at"] = now
		if req.ResolvedAt == nil {
			updates["resolved_at"] = now
		}
	case domain.StatusCancelled:
		updates["closed_at"] = now
	}

	if reopening {
		updates["reopen_count"] = req.ReopenCount + 1
		updates["closed_at"] = nil
		updates["resolved_at"] = nil
		updates["sla_breached"] = false
		updates["sla_warned"] = false
		// A reopened request gets a fresh resolution clock; measuring it
		// against the original open date would report every reopen as a breach.
		if req.SLAPolicyID != "" {
			if targets, err := s.catalog.ComputeTargets(ctx, req.SLAPolicyID, req.Priority, now); err == nil && targets != nil {
				updates["due_at"] = targets.DueAt
			}
		}
	}

	// The first response is the moment staff actually engaged, which is what
	// the response target measures.
	if req.FirstResponseAt == nil && in.To != domain.StatusTriaged {
		updates["first_response_at"] = now
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

		kind := domain.EvtStatusChanged
		summary := fmt.Sprintf("status changed from %s to %s", from, in.To)
		if reopening {
			kind = domain.EvtReopened
			summary = "request reopened"
		}
		if err := s.addEvent(ctx, tx, id, kind, actor, summary,
			string(from), string(in.To), nil, true); err != nil {
			return err
		}

		if strings.TrimSpace(in.Note) == "" {
			return nil
		}
		visibility := domain.VisibilityInternal
		if in.NotifyCitizen {
			visibility = domain.VisibilityCitizen
		}
		return tx.Create(&domain.RequestComment{
			RequestID: id, AuthorID: actor.ID, AuthorType: actor.Type,
			AuthorName: actor.Label, Visibility: visibility, Body: in.Note,
		}).Error
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "request.status_changed", TargetType: "request", TargetID: id,
		Summary: fmt.Sprintf("%s: %s → %s", req.Reference, from, in.To),
		Changes: domain.JSONMap{"from": from, "to": in.To},
	})

	updated, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	event := domain.EventStatusChanged
	switch in.To {
	case domain.StatusResolved:
		event = domain.EventRequestResolved
	case domain.StatusClosed:
		event = domain.EventRequestClosed
	}
	if in.NotifyCitizen || in.To == domain.StatusResolved || in.To == domain.StatusClosed {
		s.emit(ctx, event, updated, map[string]string{"comment": in.Note})
	} else {
		s.emit(ctx, event, updated, nil)
	}
	return updated, nil
}

func (s *Service) isPauseStatus(ctx context.Context, req *domain.Request, status domain.RequestStatus) (bool, error) {
	if req.SLAPolicyID == "" {
		return false, nil
	}
	policy, err := s.catalog.GetSLAPolicy(ctx, req.SLAPolicyID)
	if err != nil {
		return false, nil
	}
	return policy.PauseStatuses.Contains(string(status)), nil
}

func (s *Service) businessMinutes(ctx context.Context, req *domain.Request, from, to time.Time) (int, error) {
	cal, err := s.calendarFor(ctx, req)
	if err != nil {
		return 0, err
	}
	return cal.Between(from, to), nil
}

func (s *Service) shiftDeadline(ctx context.Context, req *domain.Request, deadline time.Time, byMinutes int) (time.Time, error) {
	cal, err := s.calendarFor(ctx, req)
	if err != nil {
		return deadline, err
	}
	return cal.Add(deadline, byMinutes), nil
}

func (s *Service) calendarFor(ctx context.Context, req *domain.Request) (*catalog.Calendar, error) {
	calendarID := ""
	if req.SLAPolicyID != "" {
		if policy, err := s.catalog.GetSLAPolicy(ctx, req.SLAPolicyID); err == nil {
			calendarID = policy.CalendarID
		}
	}
	return s.catalog.CalendarFor(ctx, calendarID)
}

// AssignInput names a new owner.
type AssignInput struct {
	UserID          string
	SystemID        string
	Note            string
	ExpectedVersion uint
}

// Assign sets or clears a request's owner. Passing neither id unassigns.
func (s *Service) Assign(ctx context.Context, actor audit.Actor, id string, in AssignInput) (*domain.Request, error) {
	req, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.ExpectedVersion != 0 && req.Version != in.ExpectedVersion {
		return nil, ErrStale
	}
	if in.UserID != "" && in.SystemID != "" {
		return nil, fmt.Errorf("%w: a request has one owner, not both a user and a system", ErrInvalidInput)
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"assignee_user_id":   in.UserID,
		"assignee_system_id": in.SystemID,
		"last_activity_at":   now,
		"version":            req.Version + 1,
	}

	unassigning := in.UserID == "" && in.SystemID == ""
	if !unassigning && req.Status == domain.StatusNew {
		updates["status"] = domain.StatusAssigned
	}
	if req.FirstResponseAt == nil && !unassigning {
		updates["first_response_at"] = now
	}

	name := "unassigned"
	if in.UserID != "" {
		var u domain.User
		if err := s.db.WithContext(ctx).First(&u, "id = ?", in.UserID).Error; err != nil {
			return nil, fmt.Errorf("%w: unknown assignee", ErrInvalidInput)
		}
		name = u.Name
	}
	if in.SystemID != "" {
		var sys domain.ConnectedSystem
		if err := s.db.WithContext(ctx).First(&sys, "id = ?", in.SystemID).Error; err != nil {
			return nil, fmt.Errorf("%w: unknown assignee system", ErrInvalidInput)
		}
		name = sys.Name
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
		kind := domain.EvtAssigned
		if unassigning {
			kind = domain.EvtUnassigned
		}
		return s.addEvent(ctx, tx, id, kind, actor, "assigned to "+name, "", name, nil, false)
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "request.assigned", TargetType: "request", TargetID: id,
		Summary: req.Reference + " assigned to " + name,
	})

	updated, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !unassigning {
		s.emit(ctx, domain.EventRequestAssigned, updated, nil)
	}
	return updated, nil
}

// TransferInput moves a request to another department or queue.
type TransferInput struct {
	DepartmentID string
	QueueID      string
	Note         string
	Reassign     bool
}

// Transfer moves a request across the department boundary.
//
// This is the most common real-world routing correction — a call taken as a
// Bylaw matter turns out to be Public Works — so it is a first-class action
// with its own timeline entry rather than an untracked field edit.
func (s *Service) Transfer(ctx context.Context, actor audit.Actor, id string, in TransferInput) (*domain.Request, error) {
	req, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.DepartmentID == "" && in.QueueID == "" {
		return nil, fmt.Errorf("%w: a transfer needs a target department or queue", ErrInvalidInput)
	}

	fromDept, fromQueue := req.DepartmentID, req.QueueID
	now := time.Now().UTC()

	updates := map[string]any{"last_activity_at": now, "version": req.Version + 1}
	if in.DepartmentID != "" {
		updates["department_id"] = in.DepartmentID
	}
	if in.QueueID != "" {
		updates["queue_id"] = in.QueueID
	}

	// A transfer normally clears the old owner: the point is that the previous
	// department is no longer working it.
	if in.Reassign || in.DepartmentID != "" {
		updates["assignee_user_id"] = ""
		updates["assignee_system_id"] = ""
		if req.Status == domain.StatusAssigned {
			updates["status"] = domain.StatusTriaged
		}
	}

	targetQueue := in.QueueID
	if targetQueue == "" {
		targetQueue = req.QueueID
	}

	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Model(&domain.Request{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		summary := "transferred"
		if in.Note != "" {
			summary += ": " + in.Note
		}
		return s.addEvent(ctx, tx, id, domain.EvtTransferred, actor, summary,
			fromDept+"/"+fromQueue, in.DepartmentID+"/"+in.QueueID, nil, false)
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	// Give the receiving queue's strategy a chance to pick up the work.
	if targetQueue != "" {
		if pick, err := s.routing.PickAssignee(ctx, targetQueue); err == nil && pick != nil {
			s.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", id).Updates(map[string]any{
				"assignee_user_id":   pick.UserID,
				"assignee_system_id": pick.SystemID,
				"status":             domain.StatusAssigned,
			})
		}
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "request.transferred", TargetType: "request", TargetID: id,
		Summary: req.Reference + " transferred",
		Changes: domain.JSONMap{
			"fromDepartment": fromDept, "toDepartment": in.DepartmentID,
			"fromQueue": fromQueue, "toQueue": in.QueueID,
		},
	})
	return s.Get(ctx, id)
}
