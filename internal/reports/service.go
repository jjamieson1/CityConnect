// Package reports turns request activity into the numbers a supervisor and a
// council report need: volume, SLA compliance, cycle times, agent throughput,
// satisfaction and geography.
//
// Dashboards read pre-aggregated daily rollups rather than scanning the
// request table. Municipal volumes are modest, but a dashboard that gets
// slower every year is one that stops being opened.
package reports

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var ErrInvalidInput = errors.New("reports: invalid input")

// Service implements reporting.
type Service struct {
	db  *gorm.DB
	log *slog.Logger
}

// NewService builds the reports service.
func NewService(db *gorm.DB, log *slog.Logger) *Service {
	return &Service{db: db, log: log.With("component", "reports")}
}

// Range is a reporting window.
type Range struct {
	From         time.Time
	To           time.Time
	DepartmentID string
	QueueID      string
}

// Normalize fills a sensible default window and clamps it.
func (r Range) Normalize() Range {
	if r.To.IsZero() {
		r.To = time.Now().UTC()
	}
	if r.From.IsZero() {
		r.From = r.To.AddDate(0, 0, -30)
	}
	if r.From.After(r.To) {
		r.From, r.To = r.To, r.From
	}
	return r
}

func (s *Service) scope(ctx context.Context, r Range) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&domain.Request{})
	if r.DepartmentID != "" {
		q = q.Where("department_id = ?", r.DepartmentID)
	}
	if r.QueueID != "" {
		q = q.Where("queue_id = ?", r.QueueID)
	}
	return q
}

// ---------------------------------------------------------------------------
// Volume
// ---------------------------------------------------------------------------

// VolumePoint is one day of intake and completion.
type VolumePoint struct {
	Day    string `json:"day"`
	Opened int64  `json:"opened"`
	Closed int64  `json:"closed"`
}

// VolumeBreakdown is a categorical slice of the same window.
type VolumeBreakdown struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// VolumeReport answers "how much work came in, and where from".
type VolumeReport struct {
	Range      RangeOut          `json:"range"`
	Series     []VolumePoint     `json:"series"`
	TotalOpen  int64             `json:"totalOpen"`
	TotalNew   int64             `json:"totalNew"`
	TotalDone  int64             `json:"totalClosed"`
	ByType     []VolumeBreakdown `json:"byServiceType"`
	ByStatus   []VolumeBreakdown `json:"byStatus"`
	BySource   []VolumeBreakdown `json:"bySource"`
	ByPriority []VolumeBreakdown `json:"byPriority"`
	ByQueue    []VolumeBreakdown `json:"byQueue"`
}

// RangeOut echoes the window a report covers.
type RangeOut struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Volume builds the intake report.
func (s *Service) Volume(ctx context.Context, r Range) (*VolumeReport, error) {
	r = r.Normalize()
	out := &VolumeReport{Range: RangeOut{
		From: r.From.Format("2006-01-02"), To: r.To.Format("2006-01-02"),
	}}

	type dayRow struct {
		Day string
		N   int64
	}

	var opened []dayRow
	err := s.scope(ctx, r).
		Select(s.dayExpr("opened_at") + " AS day, COUNT(*) AS n").
		Where("opened_at BETWEEN ? AND ?", r.From, r.To).
		Group("day").Order("day ASC").Scan(&opened).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	var closed []dayRow
	err = s.scope(ctx, r).
		Select(s.dayExpr("closed_at") + " AS day, COUNT(*) AS n").
		Where("closed_at BETWEEN ? AND ?", r.From, r.To).
		Group("day").Order("day ASC").Scan(&closed).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	byDay := map[string]*VolumePoint{}
	for _, row := range opened {
		byDay[row.Day] = &VolumePoint{Day: row.Day, Opened: row.N}
		out.TotalNew += row.N
	}
	for _, row := range closed {
		if p, ok := byDay[row.Day]; ok {
			p.Closed = row.N
		} else {
			byDay[row.Day] = &VolumePoint{Day: row.Day, Closed: row.N}
		}
		out.TotalDone += row.N
	}
	for _, p := range byDay {
		out.Series = append(out.Series, *p)
	}
	sort.Slice(out.Series, func(i, j int) bool { return out.Series[i].Day < out.Series[j].Day })

	if err := s.scope(ctx, r).
		Where("status NOT IN ?", []domain.RequestStatus{domain.StatusClosed, domain.StatusCancelled}).
		Count(&out.TotalOpen).Error; err != nil {
		return nil, store.Translate(err)
	}

	breakdowns := []struct {
		join   string
		column string
		dest   *[]VolumeBreakdown
	}{
		{"LEFT JOIN service_types st ON st.id = requests.service_type_id", "COALESCE(st.name, 'Unknown')", &out.ByType},
		{"", "requests.status", &out.ByStatus},
		{"", "requests.source", &out.BySource},
		{"", "requests.priority", &out.ByPriority},
		{"LEFT JOIN queues q ON q.id = requests.queue_id", "COALESCE(q.name, 'Unassigned')", &out.ByQueue},
	}
	for _, b := range breakdowns {
		q := s.scope(ctx, r).Where("opened_at BETWEEN ? AND ?", r.From, r.To)
		if b.join != "" {
			q = q.Joins(b.join)
		}
		var rows []VolumeBreakdown
		if err := q.Select(b.column + " AS label, COUNT(*) AS count").
			Group("label").Order("count DESC").Limit(20).Scan(&rows).Error; err != nil {
			return nil, store.Translate(err)
		}
		*b.dest = rows
	}

	return out, nil
}

// ---------------------------------------------------------------------------
// SLA
// ---------------------------------------------------------------------------

// SLAReport answers "are we meeting our commitments".
type SLAReport struct {
	Range           RangeOut       `json:"range"`
	Total           int64          `json:"total"`
	Met             int64          `json:"met"`
	Breached        int64          `json:"breached"`
	CompliancePct   float64        `json:"compliancePct"`
	ResponseBreach  int64          `json:"responseBreached"`
	AvgResolutionHr float64        `json:"avgResolutionHours"`
	P90ResolutionHr float64        `json:"p90ResolutionHours"`
	AvgResponseHr   float64        `json:"avgFirstResponseHours"`
	ByType          []SLABreakdown `json:"byServiceType"`
	OpenBreached    int64          `json:"openBreached"`
	AtRisk          int64          `json:"atRisk"`
}

// SLABreakdown is compliance for one service type.
type SLABreakdown struct {
	Label         string  `json:"label"`
	Total         int64   `json:"total"`
	Breached      int64   `json:"breached"`
	CompliancePct float64 `json:"compliancePct"`
}

// SLA builds the compliance report.
func (s *Service) SLA(ctx context.Context, r Range) (*SLAReport, error) {
	r = r.Normalize()
	out := &SLAReport{Range: RangeOut{
		From: r.From.Format("2006-01-02"), To: r.To.Format("2006-01-02"),
	}}

	completed := func() *gorm.DB {
		return s.scope(ctx, r).Where("closed_at BETWEEN ? AND ?", r.From, r.To)
	}

	if err := completed().Count(&out.Total).Error; err != nil {
		return nil, store.Translate(err)
	}
	if err := completed().Where("sla_breached = ?", true).Count(&out.Breached).Error; err != nil {
		return nil, store.Translate(err)
	}
	if err := completed().Where("response_breached = ?", true).Count(&out.ResponseBreach).Error; err != nil {
		return nil, store.Translate(err)
	}
	out.Met = out.Total - out.Breached
	if out.Total > 0 {
		out.CompliancePct = round1(float64(out.Met) / float64(out.Total) * 100)
	}

	// Cycle times come from the raw timestamps rather than a stored duration,
	// so a corrected close date is reflected without a backfill.
	var durations []float64
	rows, err := completed().
		Select("closed_at, opened_at").
		Where("closed_at IS NOT NULL").
		Rows()
	if err != nil {
		return nil, store.Translate(err)
	}
	defer rows.Close()

	var sum float64
	for rows.Next() {
		var closedAt, openedAt time.Time
		if err := rows.Scan(&closedAt, &openedAt); err != nil {
			continue
		}
		hours := closedAt.Sub(openedAt).Hours()
		if hours < 0 {
			continue
		}
		durations = append(durations, hours)
		sum += hours
	}
	if n := len(durations); n > 0 {
		out.AvgResolutionHr = round1(sum / float64(n))
		sort.Float64s(durations)
		out.P90ResolutionHr = round1(percentile(durations, 90))
	}

	var responseSum float64
	var responseN int64
	respRows, err := completed().
		Select("first_response_at, opened_at").
		Where("first_response_at IS NOT NULL").Rows()
	if err == nil {
		defer respRows.Close()
		for respRows.Next() {
			var respondedAt, openedAt time.Time
			if err := respRows.Scan(&respondedAt, &openedAt); err != nil {
				continue
			}
			if h := respondedAt.Sub(openedAt).Hours(); h >= 0 {
				responseSum += h
				responseN++
			}
		}
	}
	if responseN > 0 {
		out.AvgResponseHr = round1(responseSum / float64(responseN))
	}

	// Live pressure, not history: what is late right now, and what is close.
	openStatuses := []domain.RequestStatus{domain.StatusClosed, domain.StatusCancelled}
	if err := s.scope(ctx, r).
		Where("status NOT IN ? AND sla_breached = ?", openStatuses, true).
		Count(&out.OpenBreached).Error; err != nil {
		return nil, store.Translate(err)
	}
	if err := s.scope(ctx, r).
		Where("status NOT IN ? AND sla_breached = ? AND sla_warned = ?", openStatuses, false, true).
		Count(&out.AtRisk).Error; err != nil {
		return nil, store.Translate(err)
	}

	type typeRow struct {
		Label    string
		Total    int64
		Breached int64
	}
	var typeRows []typeRow
	err = completed().
		Joins("LEFT JOIN service_types st ON st.id = requests.service_type_id").
		Select("COALESCE(st.name, 'Unknown') AS label, COUNT(*) AS total, " +
			"SUM(CASE WHEN requests.sla_breached THEN 1 ELSE 0 END) AS breached").
		Group("label").Order("total DESC").Limit(20).Scan(&typeRows).Error
	if err != nil {
		return nil, store.Translate(err)
	}
	for _, row := range typeRows {
		b := SLABreakdown{Label: row.Label, Total: row.Total, Breached: row.Breached}
		if row.Total > 0 {
			b.CompliancePct = round1(float64(row.Total-row.Breached) / float64(row.Total) * 100)
		}
		out.ByType = append(out.ByType, b)
	}

	return out, nil
}

// dayExpr renders a date-truncation expression for the active dialect.
func (s *Service) dayExpr(column string) string {
	if s.db.Dialector.Name() == "mysql" {
		return fmt.Sprintf("DATE_FORMAT(%s, '%%Y-%%m-%%d')", column)
	}
	return fmt.Sprintf("strftime('%%Y-%%m-%%d', %s)", column)
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

// percentile returns the nearest-rank percentile of an already-sorted slice.
// A p90 that reports the worst case for small samples is more useful to a
// supervisor than one that interpolates toward the median.
func percentile(sorted []float64, p int) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := (p*n + 99) / 100 // ceil(p/100 * n)
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}
