// Package jobs runs the background work: the SLA clock, the outbox and
// webhook dispatchers, reporting rollups, retention sweeps and the CSAT
// survey.
package jobs

import (
	"context"
	"time"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// SLAResult summarises one SLA pass.
type SLAResult struct {
	Warned    int `json:"warned"`
	Breached  int `json:"breached"`
	Escalated int `json:"escalated"`
	Responded int `json:"responseBreached"`
}

// RunSLA checks deadlines and raises warnings, breaches and escalations.
//
// Warnings fire before the deadline rather than after, because an alert that
// only tells you something is already late is a report, not an alert. The
// pause status is respected: a request waiting on a citizen has a stopped
// clock and is skipped entirely.
func (r *Runner) RunSLA(ctx context.Context) (*SLAResult, error) {
	now := time.Now().UTC()
	res := &SLAResult{}

	openStatuses := []domain.RequestStatus{
		domain.StatusNew, domain.StatusTriaged, domain.StatusAssigned, domain.StatusInProgress,
	}

	// First response overdue.
	var responseLate []domain.Request
	err := r.db.WithContext(ctx).
		Where("first_response_at IS NULL AND response_due_at IS NOT NULL AND response_due_at < ?", now).
		Where("response_breached = ?", false).
		Where("status IN ?", openStatuses).
		Limit(500).Find(&responseLate).Error
	if err != nil {
		return nil, store.Translate(err)
	}
	for i := range responseLate {
		req := &responseLate[i]
		r.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", req.ID).
			UpdateColumn("response_breached", true)
		r.event(ctx, req.ID, domain.EvtSLAWarning, "first-response target missed")
		res.Responded++
	}

	// Approaching the resolution deadline.
	var warning []domain.Request
	err = r.db.WithContext(ctx).Preload("ServiceType").
		Where("due_at IS NOT NULL AND sla_warned = ? AND sla_breached = ?", false, false).
		Where("status IN ?", openStatuses).
		Where("paused_at IS NULL").
		Limit(500).Find(&warning).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	for i := range warning {
		req := &warning[i]
		if req.DueAt == nil {
			continue
		}
		threshold, err := r.warnThreshold(ctx, req)
		if err != nil || threshold.IsZero() || now.Before(threshold) {
			continue
		}

		r.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", req.ID).
			UpdateColumn("sla_warned", true)
		r.event(ctx, req.ID, domain.EvtSLAWarning,
			"approaching the resolution deadline of "+req.DueAt.Format("2 Jan 15:04"))
		res.Warned++
	}

	// Breached.
	var breached []domain.Request
	err = r.db.WithContext(ctx).
		Where("due_at IS NOT NULL AND due_at < ? AND sla_breached = ?", now, false).
		Where("status IN ?", openStatuses).
		Where("paused_at IS NULL").
		Limit(500).Find(&breached).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	for i := range breached {
		req := &breached[i]
		r.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", req.ID).
			UpdateColumn("sla_breached", true)
		r.event(ctx, req.ID, domain.EvtSLABreached,
			"resolution deadline passed ("+req.DueAt.Format("2 Jan 15:04")+")")
		res.Breached++

		if r.escalate(ctx, req) {
			res.Escalated++
		}
	}

	if res.Warned+res.Breached+res.Responded > 0 {
		r.log.InfoContext(ctx, "SLA pass complete",
			"warned", res.Warned, "breached", res.Breached,
			"escalated", res.Escalated, "response_breached", res.Responded)
	}
	return res, nil
}

// warnThreshold computes when a request should raise its pre-breach warning.
func (r *Runner) warnThreshold(ctx context.Context, req *domain.Request) (time.Time, error) {
	if req.SLAPolicyID == "" || req.DueAt == nil {
		return time.Time{}, nil
	}
	policy, err := r.catalog.GetSLAPolicy(ctx, req.SLAPolicyID)
	if err != nil {
		return time.Time{}, err
	}
	cal, err := r.catalog.CalendarFor(ctx, policy.CalendarID)
	if err != nil {
		return time.Time{}, err
	}
	_, resolutionMin := policy.TargetsFor(req.Priority)
	return cal.Add(req.OpenedAt, resolutionMin*policy.WarnAtPercent/100), nil
}

// escalate moves a breached request to its queue's escalation queue.
func (r *Runner) escalate(ctx context.Context, req *domain.Request) bool {
	if req.QueueID == "" {
		return false
	}
	queue, err := r.routing.GetQueue(ctx, req.QueueID)
	if err != nil || queue.EscalationQueueID == "" {
		return false
	}

	_, err = r.requests.Transfer(ctx, audit.JobActor("sla-escalation"), req.ID, requests.TransferInput{
		QueueID: queue.EscalationQueueID,
		Note:    "escalated automatically after missing its resolution deadline",
	})
	if err != nil {
		r.log.WarnContext(ctx, "SLA escalation failed",
			"request", req.Reference, "error", err)
		return false
	}
	return true
}

// AutoCloseResult summarises the auto-close pass.
type AutoCloseResult struct {
	Closed int `json:"closed"`
}

// RunAutoClose closes resolved requests that have sat untouched.
//
// A resolved request is not finished — the citizen may still reply and reopen
// it. Closing after a grace period is what keeps the backlog honest without
// cutting that window short.
func (r *Runner) RunAutoClose(ctx context.Context) (*AutoCloseResult, error) {
	cutoff := time.Now().UTC().Add(-r.cfg.Job.AutoCloseAfter)

	var stale []domain.Request
	err := r.db.WithContext(ctx).
		Where("status = ? AND resolved_at IS NOT NULL AND resolved_at < ?",
			domain.StatusResolved, cutoff).
		Limit(200).Find(&stale).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	res := &AutoCloseResult{}
	actor := audit.JobActor("auto-close")

	for i := range stale {
		_, err := r.requests.Transition(ctx, actor, stale[i].ID, requests.TransitionInput{
			To:   domain.StatusClosed,
			Note: "Closed automatically after no further activity.",
		})
		if err != nil {
			r.log.WarnContext(ctx, "auto-close failed",
				"request", stale[i].Reference, "error", err)
			continue
		}
		res.Closed++
	}
	return res, nil
}

// CSATResult summarises the survey pass.
type CSATResult struct {
	Sent int `json:"sent"`
}

// RunCSAT sends satisfaction surveys for recently resolved requests.
//
// The survey goes out a little after resolution rather than at the moment of
// it, so the citizen has had a chance to see whether the pothole was actually
// filled before being asked how it went.
func (r *Runner) RunCSAT(ctx context.Context) (*CSATResult, error) {
	now := time.Now().UTC()
	from := now.Add(-72 * time.Hour)
	until := now.Add(-24 * time.Hour)

	var candidates []domain.Request
	err := r.db.WithContext(ctx).
		Preload("ServiceType").Preload("Department").Preload("Contact").
		Where("status IN ?", []domain.RequestStatus{domain.StatusResolved, domain.StatusClosed}).
		Where("resolved_at IS NOT NULL AND resolved_at BETWEEN ? AND ?", from, until).
		Where("csat_sent_at IS NULL AND csat_score IS NULL").
		Limit(100).Find(&candidates).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	res := &CSATResult{}
	for i := range candidates {
		req := &candidates[i]

		if err := r.notify.QueueForRequest(ctx, domain.EventCSATSurvey, req, nil); err != nil {
			r.log.WarnContext(ctx, "could not queue CSAT survey",
				"request", req.Reference, "error", err)
			continue
		}
		// Stamp regardless of whether the notification was suppressed: the
		// survey has been attempted, and retrying it daily would pester
		// anyone the consent gate refuses.
		r.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", req.ID).
			UpdateColumn("csat_sent_at", now)
		res.Sent++
	}
	return res, nil
}

func (r *Runner) event(ctx context.Context, requestID, kind, summary string) {
	ev := domain.RequestEvent{
		RequestID: requestID, Kind: kind,
		ActorType: audit.ActorJob, ActorName: "SLA monitor",
		Summary: summary,
	}
	if err := r.db.WithContext(ctx).Create(&ev).Error; err != nil {
		r.log.WarnContext(ctx, "could not write SLA timeline entry", "error", err)
	}
}
