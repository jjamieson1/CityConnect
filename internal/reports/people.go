package reports

import (
	"context"
	"strconv"
	"time"

	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// AgentRow is one agent's throughput.
type AgentRow struct {
	UserID        string  `json:"userId"`
	Name          string  `json:"name"`
	DepartmentID  string  `json:"departmentId,omitempty"`
	Assigned      int64   `json:"assigned"`
	Closed        int64   `json:"closed"`
	OpenNow       int64   `json:"openNow"`
	Breached      int64   `json:"breached"`
	AvgHours      float64 `json:"avgResolutionHours"`
	CSATAvg       float64 `json:"csatAverage,omitempty"`
	CSATResponses int64   `json:"csatResponses,omitempty"`
}

// AgentReport is the throughput report.
//
// It counts work, not people: these numbers describe how load is distributed
// across a team, which is a supervisor's staffing question. They are a poor
// individual performance measure — an agent handling the hardest cases closes
// fewer of them — and the console labels them accordingly.
type AgentReport struct {
	Range RangeOut   `json:"range"`
	Rows  []AgentRow `json:"rows"`
	Note  string     `json:"note"`
}

// Agents builds the throughput report.
func (s *Service) Agents(ctx context.Context, r Range) (*AgentReport, error) {
	r = r.Normalize()
	out := &AgentReport{
		Range: RangeOut{From: r.From.Format("2006-01-02"), To: r.To.Format("2006-01-02")},
		Note: "Counts describe how work is distributed, not individual performance: " +
			"an agent handling the most difficult cases will close fewer of them.",
	}

	type row struct {
		UserID       string
		Name         string
		DepartmentID string
		Assigned     int64
		Closed       int64
		Breached     int64
	}
	var rows []row

	q := s.db.WithContext(ctx).Model(&domain.Request{}).
		Joins("JOIN users u ON u.id = requests.assignee_user_id").
		Where("requests.assignee_user_id <> ''").
		Where("requests.opened_at BETWEEN ? AND ?", r.From, r.To)
	if r.DepartmentID != "" {
		q = q.Where("requests.department_id = ?", r.DepartmentID)
	}
	if r.QueueID != "" {
		q = q.Where("requests.queue_id = ?", r.QueueID)
	}

	err := q.Select(`requests.assignee_user_id AS user_id, u.name AS name,
			u.department_id AS department_id,
			COUNT(*) AS assigned,
			SUM(CASE WHEN requests.closed_at IS NOT NULL THEN 1 ELSE 0 END) AS closed,
			SUM(CASE WHEN requests.sla_breached THEN 1 ELSE 0 END) AS breached`).
		Group("requests.assignee_user_id, u.name, u.department_id").
		Order("assigned DESC").Limit(200).Scan(&rows).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	for _, row := range rows {
		ar := AgentRow{
			UserID: row.UserID, Name: row.Name, DepartmentID: row.DepartmentID,
			Assigned: row.Assigned, Closed: row.Closed, Breached: row.Breached,
		}

		// Current load is a live figure, deliberately not restricted to the
		// reporting window: "how much is on this person's desk right now" is
		// the question a supervisor is actually asking.
		s.db.WithContext(ctx).Model(&domain.Request{}).
			Where("assignee_user_id = ? AND status NOT IN ?", row.UserID,
				[]domain.RequestStatus{domain.StatusClosed, domain.StatusCancelled}).
			Count(&ar.OpenNow)

		ar.AvgHours = s.avgResolutionFor(ctx, row.UserID, r)

		type csatRow struct {
			N     int64
			Total float64
		}
		var c csatRow
		s.db.WithContext(ctx).Model(&domain.Request{}).
			Select("COUNT(*) AS n, SUM(csat_score) AS total").
			Where("assignee_user_id = ? AND csat_score IS NOT NULL", row.UserID).
			Where("closed_at BETWEEN ? AND ?", r.From, r.To).
			Scan(&c)
		if c.N > 0 {
			ar.CSATResponses = c.N
			ar.CSATAvg = round1(c.Total / float64(c.N))
		}

		out.Rows = append(out.Rows, ar)
	}
	return out, nil
}

func (s *Service) avgResolutionFor(ctx context.Context, userID string, r Range) float64 {
	rows, err := s.db.WithContext(ctx).Model(&domain.Request{}).
		Select("closed_at, opened_at").
		Where("assignee_user_id = ? AND closed_at IS NOT NULL", userID).
		Where("closed_at BETWEEN ? AND ?", r.From, r.To).Rows()
	if err != nil {
		return 0
	}
	defer rows.Close()

	var sum float64
	var n int
	for rows.Next() {
		var closedAt, openedAt time.Time
		if err := rows.Scan(&closedAt, &openedAt); err != nil {
			continue
		}
		if h := closedAt.Sub(openedAt).Hours(); h >= 0 {
			sum += h
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return round1(sum / float64(n))
}

// CSATReport summarises satisfaction.
type CSATReport struct {
	Range        RangeOut          `json:"range"`
	Responses    int64             `json:"responses"`
	Surveyed     int64             `json:"surveyed"`
	ResponseRate float64           `json:"responseRatePct"`
	Average      float64           `json:"average"`
	Distribution map[string]int64  `json:"distribution"`
	ByType       []VolumeBreakdown `json:"byServiceType"`
}

// CSAT builds the satisfaction report.
func (s *Service) CSAT(ctx context.Context, r Range) (*CSATReport, error) {
	r = r.Normalize()
	out := &CSATReport{
		Range:        RangeOut{From: r.From.Format("2006-01-02"), To: r.To.Format("2006-01-02")},
		Distribution: map[string]int64{},
	}

	err := s.scope(ctx, r).
		Where("closed_at BETWEEN ? AND ? AND csat_sent_at IS NOT NULL", r.From, r.To).
		Count(&out.Surveyed).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	type scoreRow struct {
		Score int64
		N     int64
	}
	var scores []scoreRow
	err = s.scope(ctx, r).
		Where("closed_at BETWEEN ? AND ? AND csat_score IS NOT NULL", r.From, r.To).
		Select("csat_score AS score, COUNT(*) AS n").
		Group("csat_score").Scan(&scores).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	var total int64
	for _, row := range scores {
		out.Responses += row.N
		total += row.Score * row.N
		out.Distribution[itoa(row.Score)] = row.N
	}
	if out.Responses > 0 {
		out.Average = round1(float64(total) / float64(out.Responses))
	}
	if out.Surveyed > 0 {
		out.ResponseRate = round1(float64(out.Responses) / float64(out.Surveyed) * 100)
	}

	var byType []VolumeBreakdown
	err = s.scope(ctx, r).
		Joins("LEFT JOIN service_types st ON st.id = requests.service_type_id").
		Where("requests.closed_at BETWEEN ? AND ? AND requests.csat_score IS NOT NULL", r.From, r.To).
		Select("COALESCE(st.name, 'Unknown') AS label, AVG(requests.csat_score) AS count").
		Group("label").Order("count DESC").Limit(20).Scan(&byType).Error
	if err != nil {
		return nil, store.Translate(err)
	}
	out.ByType = byType

	return out, nil
}

// GeoPoint is a geographic cluster of requests.
type GeoPoint struct {
	Ward       string  `json:"ward,omitempty"`
	PostalCode string  `json:"postalCode,omitempty"`
	Count      int64   `json:"count"`
	Latitude   float64 `json:"latitude,omitempty"`
	Longitude  float64 `json:"longitude,omitempty"`
	Breached   int64   `json:"breached"`
}

// GeoReport is the geographic distribution report.
type GeoReport struct {
	Range     RangeOut   `json:"range"`
	ByWard    []GeoPoint `json:"byWard"`
	ByPostal  []GeoPoint `json:"byPostalCode"`
	Unmapped  int64      `json:"unmapped"`
	Clustered []GeoPoint `json:"clusters"`
}

// Geo builds the geographic report. Clusters are rounded coordinates rather
// than a real spatial index: at municipal volumes a three-decimal grid (about
// 100 m) is enough to show where the potholes actually are, without adding a
// PostGIS dependency.
func (s *Service) Geo(ctx context.Context, r Range) (*GeoReport, error) {
	r = r.Normalize()
	out := &GeoReport{Range: RangeOut{
		From: r.From.Format("2006-01-02"), To: r.To.Format("2006-01-02"),
	}}

	err := s.scope(ctx, r).
		Where("opened_at BETWEEN ? AND ? AND ward <> ''", r.From, r.To).
		Select("ward, COUNT(*) AS count, SUM(CASE WHEN sla_breached THEN 1 ELSE 0 END) AS breached").
		Group("ward").Order("count DESC").Limit(50).Scan(&out.ByWard).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	err = s.scope(ctx, r).
		Where("opened_at BETWEEN ? AND ? AND postal_code <> ''", r.From, r.To).
		Select("postal_code, COUNT(*) AS count, SUM(CASE WHEN sla_breached THEN 1 ELSE 0 END) AS breached").
		Group("postal_code").Order("count DESC").Limit(50).Scan(&out.ByPostal).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	err = s.scope(ctx, r).
		Where("opened_at BETWEEN ? AND ? AND latitude <> 0 AND longitude <> 0", r.From, r.To).
		Select("ROUND(latitude, 3) AS latitude, ROUND(longitude, 3) AS longitude, " +
			"COUNT(*) AS count, SUM(CASE WHEN sla_breached THEN 1 ELSE 0 END) AS breached").
		Group("ROUND(latitude, 3), ROUND(longitude, 3)").
		Having("COUNT(*) > 1").
		Order("count DESC").Limit(200).Scan(&out.Clustered).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	err = s.scope(ctx, r).
		Where("opened_at BETWEEN ? AND ? AND ward = '' AND latitude = 0", r.From, r.To).
		Count(&out.Unmapped).Error
	return out, store.Translate(err)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
