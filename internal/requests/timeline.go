package requests

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// addEvent appends to a request's timeline inside an existing transaction.
func (s *Service) addEvent(
	ctx context.Context, tx *gorm.DB, requestID, kind string,
	actor audit.Actor, summary, from, to string, detail domain.JSONMap, citizenVisible bool,
) error {
	ev := domain.RequestEvent{
		RequestID: requestID, Kind: kind,
		ActorID: actor.ID, ActorType: actor.Type, ActorName: actor.Label,
		Summary: summary, FromValue: from, ToValue: to,
		Detail: detail, CitizenVis: citizenVisible,
	}
	return tx.WithContext(ctx).Create(&ev).Error
}

// Events returns a request's timeline, newest first.
func (s *Service) Events(ctx context.Context, requestID string, limit int) ([]domain.RequestEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out []domain.RequestEvent
	err := s.db.WithContext(ctx).Where("request_id = ?", requestID).
		Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, store.Translate(err)
}

// CommentInput adds a note to a request.
type CommentInput struct {
	Body          string
	Visibility    domain.CommentVisibility
	NotifyCitizen bool
	MacroID       string
}

// AddComment records a note.
//
// A citizen-visible comment is the material the Service Card callout renders
// as "what has been done", so its visibility is an explicit choice rather than
// a default.
func (s *Service) AddComment(ctx context.Context, actor audit.Actor, requestID string, in CommentInput) (*domain.RequestComment, error) {
	body := strings.TrimSpace(in.Body)
	if body == "" {
		return nil, fmt.Errorf("%w: a comment needs a body", ErrInvalidInput)
	}

	req, err := s.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if in.Visibility == "" {
		in.Visibility = domain.VisibilityInternal
	}

	comment := &domain.RequestComment{
		RequestID: requestID, AuthorID: actor.ID, AuthorType: actor.Type,
		AuthorName: actor.Label, Visibility: in.Visibility, Body: body,
		MacroID: in.MacroID,
	}

	now := time.Now().UTC()
	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Create(comment).Error; err != nil {
			return err
		}

		updates := map[string]any{"last_activity_at": now}
		// A comment counts as the first response only when the citizen can
		// see it; an internal note is not a reply to anyone.
		if req.FirstResponseAt == nil && in.Visibility == domain.VisibilityCitizen {
			updates["first_response_at"] = now
		}
		if err := tx.Model(&domain.Request{}).Where("id = ?", requestID).Updates(updates).Error; err != nil {
			return err
		}

		return s.addEvent(ctx, tx, requestID, domain.EvtCommented, actor,
			truncate(body, 200), "", string(in.Visibility), nil,
			in.Visibility == domain.VisibilityCitizen)
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	if in.MacroID != "" {
		s.catalog.RecordMacroUse(ctx, in.MacroID)
	}
	if in.NotifyCitizen && in.Visibility == domain.VisibilityCitizen {
		updated, err := s.Get(ctx, requestID)
		if err == nil {
			s.emit(ctx, domain.EventCitizenComment, updated, map[string]string{"comment": body})
		}
	}
	return comment, nil
}

// Comments returns a request's notes, optionally only those a citizen may see.
func (s *Service) Comments(ctx context.Context, requestID string, citizenOnly bool) ([]domain.RequestComment, error) {
	q := s.db.WithContext(ctx).Where("request_id = ?", requestID)
	if citizenOnly {
		q = q.Where("visibility = ?", domain.VisibilityCitizen)
	}
	var out []domain.RequestComment
	err := q.Order("created_at ASC").Find(&out).Error
	return out, store.Translate(err)
}

// ApplyMacro runs a canned response against a request: the comment plus any
// status, priority or tag side effects it declares.
func (s *Service) ApplyMacro(ctx context.Context, actor audit.Actor, requestID, macroID string) (*domain.Request, error) {
	macro, err := s.catalog.GetMacro(ctx, macroID)
	if err != nil {
		return nil, fmt.Errorf("%w: unknown macro", ErrInvalidInput)
	}
	req, err := s.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(macro.Body) != "" {
		if _, err := s.AddComment(ctx, actor, requestID, CommentInput{
			Body:          macro.Body,
			Visibility:    domain.CommentVisibility(macro.Visibility),
			NotifyCitizen: macro.NotifyCitzn,
			MacroID:       macro.ID,
		}); err != nil {
			return nil, err
		}
	}

	if len(macro.AddTags) > 0 {
		tags := req.Tags
		for _, t := range macro.AddTags {
			if !tags.Contains(t) {
				tags = append(tags, t)
			}
		}
		s.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", requestID).
			UpdateColumn("tags", tags)
	}
	if macro.SetPriority != "" && domain.PriorityRank(macro.SetPriority) > 0 {
		s.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", requestID).
			UpdateColumn("priority", macro.SetPriority)
	}
	if macro.SetStatus != "" {
		target := domain.RequestStatus(macro.SetStatus)
		if req.Status.CanTransitionTo(target) {
			if _, err := s.Transition(ctx, actor, requestID, TransitionInput{To: target}); err != nil {
				s.log.WarnContext(ctx, "macro status change refused",
					"macro", macro.Name, "from", req.Status, "to", target, "error", err)
			}
		}
	}
	return s.Get(ctx, requestID)
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

// LinkInput joins two requests.
type LinkInput struct {
	TargetID  string
	TargetRef string
	Kind      string
	Note      string
}

// Link relates two requests.
//
// Municipal workloads produce many reports of one problem — twenty calls about
// the same pothole. Linking them lets the crew work it once while every
// reporter stays on their own request and keeps receiving updates.
func (s *Service) Link(ctx context.Context, actor audit.Actor, requestID string, in LinkInput) (*domain.RequestLink, error) {
	target, err := s.resolveTarget(ctx, in.TargetID, in.TargetRef)
	if err != nil {
		return nil, err
	}
	if target.ID == requestID {
		return nil, ErrSelfLink
	}
	switch in.Kind {
	case domain.LinkDuplicateOf, domain.LinkRelatedTo, domain.LinkChildOf:
	default:
		return nil, fmt.Errorf("%w: unknown link kind %q", ErrInvalidInput, in.Kind)
	}

	link := &domain.RequestLink{
		RequestID: requestID, TargetID: target.ID, Kind: in.Kind,
		CreatedBy: actor.ID, Note: in.Note,
	}

	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Create(link).Error; err != nil {
			return err
		}
		// Related-to is symmetric; recording only one direction means the
		// other request's console never shows the connection.
		if in.Kind == domain.LinkRelatedTo {
			reverse := &domain.RequestLink{
				RequestID: target.ID, TargetID: requestID, Kind: domain.LinkRelatedTo,
				CreatedBy: actor.ID, Note: in.Note,
			}
			if err := tx.Create(reverse).Error; err != nil {
				return err
			}
		}
		return s.addEvent(ctx, tx, requestID, domain.EvtLinked, actor,
			fmt.Sprintf("linked as %s to %s", in.Kind, target.Reference),
			"", target.Reference, nil, false)
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	// Marking a duplicate closes the loser and points it at the survivor, so
	// the crew sees one piece of work rather than twenty.
	if in.Kind == domain.LinkDuplicateOf {
		s.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", requestID).
			UpdateColumn("merged_into_id", target.ID)
		if _, err := s.Transition(ctx, actor, requestID, TransitionInput{
			To:             domain.StatusClosed,
			ResolutionCode: "duplicate",
			Note:           "Closed as a duplicate of " + target.Reference,
		}); err != nil {
			s.log.WarnContext(ctx, "could not close duplicate", "request", requestID, "error", err)
		}
	}
	return link, nil
}

func (s *Service) resolveTarget(ctx context.Context, id, reference string) (*domain.Request, error) {
	if id != "" {
		return s.Get(ctx, id)
	}
	if reference != "" {
		return s.GetByReference(ctx, reference)
	}
	return nil, fmt.Errorf("%w: a link needs a target id or reference", ErrInvalidInput)
}

// Links returns a request's links with their targets resolved for display.
func (s *Service) Links(ctx context.Context, requestID string) ([]domain.RequestLink, error) {
	var links []domain.RequestLink
	if err := s.db.WithContext(ctx).Where("request_id = ?", requestID).Find(&links).Error; err != nil {
		return nil, store.Translate(err)
	}
	if len(links) == 0 {
		return links, nil
	}

	ids := make([]string, len(links))
	for i, l := range links {
		ids[i] = l.TargetID
	}
	type row struct{ ID, Reference, Subject string }
	var targets []row
	s.db.WithContext(ctx).Model(&domain.Request{}).
		Select("id, reference, subject").Where("id IN ?", ids).Scan(&targets)

	byID := make(map[string]row, len(targets))
	for _, t := range targets {
		byID[t.ID] = t
	}
	for i := range links {
		if t, ok := byID[links[i].TargetID]; ok {
			links[i].TargetRef, links[i].TargetSubj = t.Reference, t.Subject
		}
	}
	return links, nil
}

// Unlink removes a link.
func (s *Service) Unlink(ctx context.Context, actor audit.Actor, requestID, linkID string) error {
	res := s.db.WithContext(ctx).
		Where("id = ? AND request_id = ?", linkID, requestID).
		Delete(&domain.RequestLink{})
	if res.Error != nil {
		return store.Translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
