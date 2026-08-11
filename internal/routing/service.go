package routing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var (
	ErrNotFound     = errors.New("routing: not found")
	ErrInvalidInput = errors.New("routing: invalid input")
	ErrConflict     = errors.New("routing: conflict")
	ErrNoCandidate  = errors.New("routing: no eligible assignee in queue")
)

// Service implements queue and rule management plus assignment.
type Service struct {
	db    *gorm.DB
	audit *audit.Service
	log   *slog.Logger
}

// NewService builds the routing service.
func NewService(db *gorm.DB, aud *audit.Service, log *slog.Logger) *Service {
	return &Service{db: db, audit: aud, log: log.With("component", "routing")}
}

// ---------------------------------------------------------------------------
// Queues
// ---------------------------------------------------------------------------

// ListQueues returns queues with their open workload.
func (s *Service) ListQueues(ctx context.Context, departmentID string, includeInactive bool) ([]domain.Queue, error) {
	q := s.db.WithContext(ctx).Model(&domain.Queue{}).Preload("Department")
	if !includeInactive {
		q = q.Where("active = ?", true)
	}
	if departmentID != "" {
		q = q.Where("department_id = ?", departmentID)
	}

	var queues []domain.Queue
	if err := q.Order("sort_order ASC, name ASC").Find(&queues).Error; err != nil {
		return nil, store.Translate(err)
	}
	if len(queues) == 0 {
		return queues, nil
	}

	ids := make([]string, len(queues))
	for i, item := range queues {
		ids[i] = item.ID
	}
	type row struct {
		QueueID string
		N       int
	}
	var counts []row
	err := s.db.WithContext(ctx).Model(&domain.Request{}).
		Select("queue_id, COUNT(*) AS n").
		Where("queue_id IN ? AND status NOT IN ?", ids,
			[]domain.RequestStatus{domain.StatusClosed, domain.StatusCancelled}).
		Group("queue_id").Scan(&counts).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	byID := make(map[string]int, len(counts))
	for _, c := range counts {
		byID[c.QueueID] = c.N
	}
	for i := range queues {
		queues[i].OpenCount = byID[queues[i].ID]
	}
	return queues, nil
}

// GetQueue loads one queue with its members.
func (s *Service) GetQueue(ctx context.Context, id string) (*domain.Queue, error) {
	var q domain.Queue
	err := s.db.WithContext(ctx).Preload("Department").Preload("Members").Preload("Systems").
		First(&q, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &q, store.Translate(err)
}

// SaveQueue creates or updates a queue.
func (s *Service) SaveQueue(ctx context.Context, actor audit.Actor, q *domain.Queue) (*domain.Queue, error) {
	q.Code = strings.ToUpper(strings.TrimSpace(q.Code))
	q.Name = strings.TrimSpace(q.Name)
	if q.Code == "" || q.Name == "" {
		return nil, fmt.Errorf("%w: queue code and name are required", ErrInvalidInput)
	}
	if q.Kind == "" {
		q.Kind = domain.QueueKindHuman
	}
	if q.AssignmentStrategy == "" {
		q.AssignmentStrategy = domain.AssignManual
	}
	switch q.AssignmentStrategy {
	case domain.AssignManual, domain.AssignRoundRobin, domain.AssignLeastLoaded:
	default:
		return nil, fmt.Errorf("%w: unknown assignment strategy %q", ErrInvalidInput, q.AssignmentStrategy)
	}
	if q.EscalationQueueID == q.ID && q.ID != "" {
		return nil, fmt.Errorf("%w: a queue cannot escalate to itself", ErrInvalidInput)
	}

	action := "queue.created"
	if q.ID != "" {
		action = "queue.updated"
	}
	if err := s.db.WithContext(ctx).Omit("Members", "Systems", "Department").Save(q).Error; err != nil {
		return nil, store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "queue", TargetID: q.ID, Summary: q.Name,
	})
	return q, nil
}

// SetQueueMembers replaces a queue's staff membership.
func (s *Service) SetQueueMembers(ctx context.Context, actor audit.Actor, queueID string, userIDs []string) error {
	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Where("queue_id = ?", queueID).Delete(&domain.QueueMember{}).Error; err != nil {
			return err
		}
		if len(userIDs) == 0 {
			return nil
		}
		now := time.Now().UTC()
		rows := make([]domain.QueueMember, 0, len(userIDs))
		for _, uid := range userIDs {
			if uid != "" {
				rows = append(rows, domain.QueueMember{QueueID: queueID, UserID: uid, JoinedAt: now})
			}
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "queue.members_set", TargetType: "queue", TargetID: queueID,
		Summary: fmt.Sprintf("%d member(s)", len(userIDs)),
	})
	return nil
}

// DeleteQueue removes a queue that holds no open work.
func (s *Service) DeleteQueue(ctx context.Context, actor audit.Actor, id string) error {
	var open int64
	s.db.WithContext(ctx).Model(&domain.Request{}).
		Where("queue_id = ? AND status NOT IN ?", id,
			[]domain.RequestStatus{domain.StatusClosed, domain.StatusCancelled}).
		Count(&open)
	if open > 0 {
		return fmt.Errorf("%w: queue still holds %d open request(s)", ErrConflict, open)
	}

	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Where("queue_id = ?", id).Delete(&domain.QueueMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("queue_id = ?", id).Delete(&domain.QueueSystem{}).Error; err != nil {
			return err
		}
		return tx.Delete(&domain.Queue{}, "id = ?", id).Error
	})
	if err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "queue.deleted", TargetType: "queue", TargetID: id,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Assignment
// ---------------------------------------------------------------------------

// Assignment names who a request should go to.
type Assignment struct {
	UserID   string
	SystemID string
}

// PickAssignee chooses an owner from a queue according to its strategy.
//
// Manual queues return nothing: work sits in the queue until an agent takes
// it. Automatic strategies only consider active members, so an agent on leave
// does not silently accumulate a backlog.
func (s *Service) PickAssignee(ctx context.Context, queueID string) (*Assignment, error) {
	q, err := s.GetQueue(ctx, queueID)
	if err != nil {
		return nil, err
	}
	if q.AssignmentStrategy == domain.AssignManual {
		return nil, nil
	}

	// A system queue routes to its connected system.
	if q.Kind == domain.QueueKindSystem {
		for _, sys := range q.Systems {
			if sys.Active {
				return &Assignment{SystemID: sys.ID}, nil
			}
		}
		return nil, ErrNoCandidate
	}

	eligible := make([]domain.User, 0, len(q.Members))
	for _, m := range q.Members {
		if m.Status == domain.UserActive {
			eligible = append(eligible, m)
		}
	}
	if len(eligible) == 0 {
		return nil, ErrNoCandidate
	}

	switch q.AssignmentStrategy {
	case domain.AssignRoundRobin:
		// The cursor is persisted so a restart does not reset the rotation to
		// the first member, which would quietly overload whoever sorts first.
		idx := q.RoundRobinCursor % len(eligible)
		chosen := eligible[idx]
		s.db.WithContext(ctx).Model(&domain.Queue{}).Where("id = ?", q.ID).
			UpdateColumn("round_robin_cursor", (q.RoundRobinCursor+1)%1_000_000)
		return &Assignment{UserID: chosen.ID}, nil

	case domain.AssignLeastLoaded:
		ids := make([]string, len(eligible))
		for i, u := range eligible {
			ids[i] = u.ID
		}
		type row struct {
			AssigneeUserID string
			N              int
		}
		var counts []row
		err := s.db.WithContext(ctx).Model(&domain.Request{}).
			Select("assignee_user_id, COUNT(*) AS n").
			Where("assignee_user_id IN ? AND status NOT IN ?", ids,
				[]domain.RequestStatus{domain.StatusClosed, domain.StatusCancelled, domain.StatusResolved}).
			Group("assignee_user_id").Scan(&counts).Error
		if err != nil {
			return nil, store.Translate(err)
		}

		load := make(map[string]int, len(ids))
		for _, c := range counts {
			load[c.AssigneeUserID] = c.N
		}
		best, bestLoad := eligible[0], load[eligible[0].ID]
		for _, u := range eligible[1:] {
			if load[u.ID] < bestLoad {
				best, bestLoad = u, load[u.ID]
			}
		}
		return &Assignment{UserID: best.ID}, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

// ListRules returns the rule set in evaluation order.
func (s *Service) ListRules(ctx context.Context, includeInactive bool) ([]domain.RoutingRule, error) {
	q := s.db.WithContext(ctx).Model(&domain.RoutingRule{})
	if !includeInactive {
		q = q.Where("active = ?", true)
	}
	var out []domain.RoutingRule
	err := q.Order("priority ASC, created_at ASC").Find(&out).Error
	return out, store.Translate(err)
}

// GetRule loads one rule.
func (s *Service) GetRule(ctx context.Context, id string) (*domain.RoutingRule, error) {
	var r domain.RoutingRule
	err := s.db.WithContext(ctx).First(&r, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &r, store.Translate(err)
}

// SaveRule creates or updates a routing rule, validating it first.
func (s *Service) SaveRule(ctx context.Context, actor audit.Actor, r *domain.RoutingRule) (*domain.RoutingRule, error) {
	if err := ValidateRule(r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	action := "rule.created"
	if r.ID != "" {
		action = "rule.updated"
	}
	if err := s.db.WithContext(ctx).Save(r).Error; err != nil {
		return nil, store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "routing_rule", TargetID: r.ID, Summary: r.Name,
		Changes: domain.JSONMap{"conditions": r.Conditions, "actions": r.Actions, "active": r.Active},
	})
	return r, nil
}

// DeleteRule removes a rule.
func (s *Service) DeleteRule(ctx context.Context, actor audit.Actor, id string) error {
	if err := s.db.WithContext(ctx).Delete(&domain.RoutingRule{}, "id = ?", id).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "rule.deleted", TargetType: "routing_rule", TargetID: id,
	})
	return nil
}

// Route evaluates the active rule set for a request.
func (s *Service) Route(ctx context.Context, f Facts) (Decision, error) {
	rules, err := s.ListRules(ctx, false)
	if err != nil {
		return Decision{}, err
	}
	d := Evaluate(rules, f)

	// Record which rules fired so the console can show a rule's real hit rate
	// rather than an admin's assumption about it.
	if len(d.MatchedRules) > 0 {
		now := time.Now().UTC()
		ids := make([]string, len(d.MatchedRules))
		for i, m := range d.MatchedRules {
			ids[i] = m.ID
		}
		s.db.WithContext(ctx).Model(&domain.RoutingRule{}).Where("id IN ?", ids).
			Updates(map[string]any{
				"match_count":  gorm.Expr("match_count + 1"),
				"last_matched": now,
			})
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// Simulation
// ---------------------------------------------------------------------------

// SimulationCase is one request replayed through a candidate rule set.
type SimulationCase struct {
	RequestID  string   `json:"requestId"`
	Reference  string   `json:"reference"`
	Subject    string   `json:"subject"`
	CurrentQ   string   `json:"currentQueueId,omitempty"`
	ProposedQ  string   `json:"proposedQueueId,omitempty"`
	Changed    bool     `json:"changed"`
	Unrouted   bool     `json:"unrouted"`
	RuleNames  []string `json:"matchedRules,omitempty"`
	NewPriorty string   `json:"proposedPriority,omitempty"`
}

// SimulationResult summarises a dry run.
type SimulationResult struct {
	Sampled   int              `json:"sampled"`
	Changed   int              `json:"changed"`
	Unrouted  int              `json:"unrouted"`
	Cases     []SimulationCase `json:"cases"`
	RuleHits  map[string]int   `json:"ruleHits"`
	Truncated bool             `json:"truncated"`
}

// Simulate replays recent requests through a candidate rule set without
// changing anything.
//
// Activating an untested rule is how a queue silently stops receiving work:
// the rule matches more than its author expected, everything lands somewhere
// else, and nobody notices until a citizen chases a request three weeks later.
// A dry run against real history is cheap and makes that visible first.
func (s *Service) Simulate(ctx context.Context, rules []domain.RoutingRule, sample int) (*SimulationResult, error) {
	if sample <= 0 || sample > 500 {
		sample = 100
	}

	var requests []domain.Request
	err := s.db.WithContext(ctx).
		Preload("ServiceType").
		Order("created_at DESC").Limit(sample).Find(&requests).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	res := &SimulationResult{RuleHits: map[string]int{}, Sampled: len(requests)}

	for i := range requests {
		r := &requests[i]
		code, category := "", ""
		if r.ServiceType != nil {
			code, category = r.ServiceType.Code, r.ServiceType.Category
		}

		d := Evaluate(rules, FactsFromRequest(r, code, category))

		c := SimulationCase{
			RequestID: r.ID, Reference: r.Reference, Subject: r.Subject,
			CurrentQ: r.QueueID, ProposedQ: d.QueueID, NewPriorty: d.Priority,
		}
		for _, m := range d.MatchedRules {
			c.RuleNames = append(c.RuleNames, m.Name)
			res.RuleHits[m.Name]++
		}

		switch {
		case d.QueueID == "":
			c.Unrouted = true
			res.Unrouted++
		case d.QueueID != r.QueueID:
			c.Changed = true
			res.Changed++
		}

		if len(res.Cases) < 100 {
			res.Cases = append(res.Cases, c)
		} else {
			res.Truncated = true
		}
	}
	return res, nil
}
