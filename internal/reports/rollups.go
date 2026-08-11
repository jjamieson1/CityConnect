package reports

import (
	"context"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// RollupResult summarises a rollup build.
type RollupResult struct {
	Days    int `json:"days"`
	Metrics int `json:"metrics"`
}

// BuildRollups recomputes daily aggregates from the given date forward.
//
// Recomputing rather than incrementing is deliberate: a request closed today
// changes yesterday's numbers, and an incremental counter would drift out of
// step with the source of truth within a week. The window is small enough that
// recomputation is cheap.
func (s *Service) BuildRollups(ctx context.Context, from time.Time) (*RollupResult, error) {
	from = truncateDay(from)
	today := truncateDay(time.Now().UTC())
	res := &RollupResult{}

	for day := from; !day.After(today); day = day.AddDate(0, 0, 1) {
		if err := s.buildDay(ctx, day); err != nil {
			return res, err
		}
		res.Days++
	}

	var metrics int64
	s.db.WithContext(ctx).Model(&domain.ReportRollup{}).Where("day >= ?", from).Count(&metrics)
	res.Metrics = int(metrics)
	return res, nil
}

func (s *Service) buildDay(ctx context.Context, day time.Time) error {
	next := day.AddDate(0, 0, 1)

	type group struct {
		DepartmentID  string
		QueueID       string
		ServiceTypeID string
		N             int64
	}

	// Opened.
	var opened []group
	err := s.db.WithContext(ctx).Model(&domain.Request{}).
		Select("department_id, queue_id, service_type_id, COUNT(*) AS n").
		Where("opened_at >= ? AND opened_at < ?", day, next).
		Group("department_id, queue_id, service_type_id").Scan(&opened).Error
	if err != nil {
		return store.Translate(err)
	}

	// Closed, split by whether the SLA held.
	type closedGroup struct {
		group
		Breached int64
	}
	var closed []closedGroup
	err = s.db.WithContext(ctx).Model(&domain.Request{}).
		Select("department_id, queue_id, service_type_id, COUNT(*) AS n, " +
			"SUM(CASE WHEN sla_breached THEN 1 ELSE 0 END) AS breached").
		Where("closed_at >= ? AND closed_at < ?", day, next).
		Group("department_id, queue_id, service_type_id").Scan(&closed).Error
	if err != nil {
		return store.Translate(err)
	}

	return store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Where("day = ?", day).Delete(&domain.ReportRollup{}).Error; err != nil {
			return err
		}

		rows := make([]domain.ReportRollup, 0, len(opened)+len(closed)*2)
		add := func(metric string, g group, count int64, value float64) {
			if count == 0 && value == 0 {
				return
			}
			rows = append(rows, domain.ReportRollup{
				Day: day, Metric: metric,
				DepartmentID: g.DepartmentID, QueueID: g.QueueID, ServiceTypeID: g.ServiceTypeID,
				Count: count, SumVal: value,
			})
		}

		for _, g := range opened {
			add(domain.MetricRequestsOpened, g, g.N, 0)
		}
		for _, c := range closed {
			add(domain.MetricRequestsClosed, c.group, c.N, 0)
			add(domain.MetricSLABreached, c.group, c.Breached, 0)
			add(domain.MetricSLAMet, c.group, c.N-c.Breached, 0)
		}

		// Resolution time, aggregated per department for the trend chart.
		resolution, err := s.resolutionStats(ctx, day, next)
		if err != nil {
			return err
		}
		for dept, stats := range resolution {
			rows = append(rows, domain.ReportRollup{
				Day: day, Metric: domain.MetricResolutionHours, DepartmentID: dept,
				Count: stats.n, SumVal: stats.sum, AvgVal: stats.avg(),
				MinVal: stats.min, MaxVal: stats.max, P90Val: stats.p90(),
			})
		}

		// Satisfaction scores recorded that day.
		type csatRow struct {
			DepartmentID string
			N            int64
			Total        float64
		}
		var csat []csatRow
		if err := tx.Model(&domain.Request{}).
			Select("department_id, COUNT(*) AS n, SUM(csat_score) AS total").
			Where("csat_score IS NOT NULL AND updated_at >= ? AND updated_at < ?", day, next).
			Group("department_id").Scan(&csat).Error; err != nil {
			return err
		}
		for _, c := range csat {
			avg := 0.0
			if c.N > 0 {
				avg = c.Total / float64(c.N)
			}
			rows = append(rows, domain.ReportRollup{
				Day: day, Metric: domain.MetricCSAT, DepartmentID: c.DepartmentID,
				Count: c.N, SumVal: c.Total, AvgVal: avg,
			})
		}

		// Notifications actually delivered.
		var notified int64
		if err := tx.Model(&domain.NotificationOutbox{}).
			Where("state = ? AND sent_at >= ? AND sent_at < ?", domain.OutboxSent, day, next).
			Count(&notified).Error; err != nil {
			return err
		}
		if notified > 0 {
			rows = append(rows, domain.ReportRollup{
				Day: day, Metric: domain.MetricNotifications, Count: notified,
			})
		}

		if len(rows) == 0 {
			return nil
		}
		return tx.CreateInBatches(&rows, 200).Error
	})
}

type stats struct {
	n         int64
	sum       float64
	min       float64
	max       float64
	durations []float64
}

func (s *stats) avg() float64 {
	if s.n == 0 {
		return 0
	}
	return round1(s.sum / float64(s.n))
}

func (s *stats) p90() float64 {
	if len(s.durations) == 0 {
		return 0
	}
	sort.Float64s(s.durations)
	return round1(percentile(s.durations, 90))
}

func (s *Service) resolutionStats(ctx context.Context, from, to time.Time) (map[string]*stats, error) {
	rows, err := s.db.WithContext(ctx).Model(&domain.Request{}).
		Select("department_id, closed_at, opened_at").
		Where("closed_at >= ? AND closed_at < ?", from, to).
		Rows()
	if err != nil {
		return nil, store.Translate(err)
	}
	defer rows.Close()

	out := map[string]*stats{}
	for rows.Next() {
		var dept string
		var closedAt, openedAt time.Time
		if err := rows.Scan(&dept, &closedAt, &openedAt); err != nil {
			continue
		}
		hours := closedAt.Sub(openedAt).Hours()
		if hours < 0 {
			continue
		}
		st, ok := out[dept]
		if !ok {
			st = &stats{min: hours}
			out[dept] = st
		}
		st.n++
		st.sum += hours
		st.durations = append(st.durations, hours)
		if hours < st.min {
			st.min = hours
		}
		if hours > st.max {
			st.max = hours
		}
	}
	return out, nil
}

// TrendPoint is one day of a rolled-up metric.
type TrendPoint struct {
	Day   string  `json:"day"`
	Count int64   `json:"count"`
	Avg   float64 `json:"avg,omitempty"`
	P90   float64 `json:"p90,omitempty"`
}

// Trend reads a metric's daily series from the rollups.
func (s *Service) Trend(ctx context.Context, metric string, r Range) ([]TrendPoint, error) {
	r = r.Normalize()

	q := s.db.WithContext(ctx).Model(&domain.ReportRollup{}).
		Where("metric = ? AND day BETWEEN ? AND ?", metric, truncateDay(r.From), truncateDay(r.To))
	if r.DepartmentID != "" {
		q = q.Where("department_id = ?", r.DepartmentID)
	}
	if r.QueueID != "" {
		q = q.Where("queue_id = ?", r.QueueID)
	}

	type row struct {
		Day   time.Time
		Count int64
		Avg   float64
		P90   float64
	}
	var rows []row
	err := q.Select("day, SUM(count) AS count, AVG(avg_val) AS avg, MAX(p90_val) AS p90").
		Group("day").Order("day ASC").Scan(&rows).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	out := make([]TrendPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, TrendPoint{
			Day: r.Day.Format("2006-01-02"), Count: r.Count,
			Avg: round1(r.Avg), P90: round1(r.P90),
		})
	}
	return out, nil
}

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
