package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/notifications"
	"github.com/jjamieson1/CityConnect/internal/reports"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/routing"
	"github.com/jjamieson1/CityConnect/internal/webhooks"
)

// Runner owns the background schedule.
type Runner struct {
	db       *gorm.DB
	cfg      *config.Config
	log      *slog.Logger
	catalog  *catalog.Service
	routing  *routing.Service
	requests *requests.Service
	notify   *notifications.Service
	webhooks *webhooks.Service
	reports  *reports.Service
	agents   *agents.Service

	mu      sync.Mutex
	lastRun map[string]JobStatus
}

// JobStatus records the outcome of a job's last run, for the admin dashboard
// and the readiness probe.
type JobStatus struct {
	Name     string    `json:"name"`
	LastRun  time.Time `json:"lastRun"`
	Duration string    `json:"duration"`
	Error    string    `json:"error,omitempty"`
	Detail   any       `json:"detail,omitempty"`
}

// NewRunner builds the job runner.
func NewRunner(
	db *gorm.DB, cfg *config.Config, log *slog.Logger,
	cat *catalog.Service, route *routing.Service, reqs *requests.Service,
	notify *notifications.Service, hooks *webhooks.Service,
	rep *reports.Service, ag *agents.Service,
) *Runner {
	return &Runner{
		db: db, cfg: cfg, log: log.With("component", "jobs"),
		catalog: cat, routing: route, requests: reqs,
		notify: notify, webhooks: hooks, reports: rep, agents: ag,
		lastRun: map[string]JobStatus{},
	}
}

// Start launches every scheduled job and blocks until the context is
// cancelled.
//
// Each job runs on its own ticker so a slow rollup does not delay the outbox,
// and each is wrapped so a panic takes down that job's tick rather than the
// server.
func (r *Runner) Start(ctx context.Context) {
	if !r.cfg.Job.Enabled {
		r.log.Info("background jobs are disabled")
		return
	}

	schedule := []struct {
		name     string
		interval time.Duration
		fn       func(context.Context) (any, error)
	}{
		{"outbox", r.cfg.Job.OutboxInterval, func(c context.Context) (any, error) {
			return r.notify.Drain(c, 25)
		}},
		{"webhooks", r.cfg.Job.WebhookInterval, func(c context.Context) (any, error) {
			return r.webhooks.Drain(c, 50)
		}},
		{"sla", r.cfg.Job.SLAInterval, func(c context.Context) (any, error) {
			return r.RunSLA(c)
		}},
		{"rollups", r.cfg.Job.RollupInterval, func(c context.Context) (any, error) {
			return r.reports.BuildRollups(c, time.Now().UTC().AddDate(0, 0, -2))
		}},
		{"auto-close", time.Hour, func(c context.Context) (any, error) {
			return r.RunAutoClose(c)
		}},
		{"csat", 6 * time.Hour, func(c context.Context) (any, error) {
			return r.RunCSAT(c)
		}},
		{"sessions", time.Hour, func(c context.Context) (any, error) {
			n, err := r.agents.PurgeExpired(c)
			return map[string]int64{"purged": n}, err
		}},
		{"retention", r.cfg.Job.RetentionInterval, func(c context.Context) (any, error) {
			return r.RunRetention(c)
		}},
	}

	var wg sync.WaitGroup
	for _, job := range schedule {
		if job.interval <= 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.loop(ctx, job.name, job.interval, job.fn)
		}()
	}

	r.log.Info("background jobs started", "count", len(schedule))
	wg.Wait()
	r.log.Info("background jobs stopped")
}

func (r *Runner) loop(ctx context.Context, name string, interval time.Duration, fn func(context.Context) (any, error)) {
	// Stagger the first tick so every job does not fire simultaneously at boot.
	initial := time.Duration(int64(interval) / 4)
	if initial < time.Second {
		initial = time.Second
	}
	timer := time.NewTimer(initial)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			r.runOnce(ctx, name, fn)
			timer.Reset(interval)
		}
	}
}

func (r *Runner) runOnce(ctx context.Context, name string, fn func(context.Context) (any, error)) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("job panicked", "job", name, "panic", p)
			r.record(name, JobStatus{Name: name, LastRun: time.Now().UTC(), Error: "panicked"})
		}
	}()

	// A job that hangs must not hold its ticker forever.
	jobCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	start := time.Now()
	detail, err := fn(jobCtx)
	status := JobStatus{
		Name: name, LastRun: start.UTC(),
		Duration: time.Since(start).Round(time.Millisecond).String(),
		Detail:   detail,
	}
	if err != nil {
		status.Error = err.Error()
		r.log.ErrorContext(ctx, "job failed", "job", name, "error", err)
	}
	r.record(name, status)
}

func (r *Runner) record(name string, status JobStatus) {
	r.mu.Lock()
	r.lastRun[name] = status
	r.mu.Unlock()
}

// Status returns the last outcome of every job.
func (r *Runner) Status() []JobStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]JobStatus, 0, len(r.lastRun))
	for _, s := range r.lastRun {
		out = append(out, s)
	}
	return out
}

// RunNow triggers a job by name, for the admin console and for tests.
func (r *Runner) RunNow(ctx context.Context, name string) (any, error) {
	switch name {
	case "outbox":
		return r.notify.Drain(ctx, 100)
	case "webhooks":
		return r.webhooks.Drain(ctx, 100)
	case "sla":
		return r.RunSLA(ctx)
	case "rollups":
		return r.reports.BuildRollups(ctx, time.Now().UTC().AddDate(0, 0, -30))
	case "auto-close":
		return r.RunAutoClose(ctx)
	case "csat":
		return r.RunCSAT(ctx)
	case "retention":
		return r.RunRetention(ctx)
	}
	return nil, nil
}
