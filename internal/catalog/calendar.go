package catalog

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// Calendar wraps a BusinessCalendar with the arithmetic SLA targets need.
//
// SLA clocks run in business time, not wall time. A pothole reported at
// 4:55pm on Friday with a four-hour target is not late at 9am on Monday, and
// a system that says otherwise trains staff to ignore its own alerts.
type Calendar struct {
	model    *domain.BusinessCalendar
	loc      *time.Location
	windows  map[time.Weekday][]window
	holidays map[string]bool
}

type window struct {
	startMin int // minutes from midnight
	endMin   int
}

// LoadCalendar prepares a calendar for arithmetic. A nil model yields an
// always-open calendar, which is the correct default for a 24/7 service such
// as a water main break.
func LoadCalendar(m *domain.BusinessCalendar) (*Calendar, error) {
	if m == nil {
		return &Calendar{model: &domain.BusinessCalendar{AlwaysOpen: true}, loc: time.UTC}, nil
	}

	loc := time.UTC
	if m.TimeZone != "" {
		l, err := time.LoadLocation(m.TimeZone)
		if err != nil {
			return nil, fmt.Errorf("catalog: unknown time zone %q: %w", m.TimeZone, err)
		}
		loc = l
	}

	c := &Calendar{
		model:    m,
		loc:      loc,
		windows:  map[time.Weekday][]window{},
		holidays: map[string]bool{},
	}
	for _, day := range m.Holidays {
		c.holidays[strings.TrimSpace(day)] = true
	}

	for key, raw := range m.Hours {
		dayNum, err := strconv.Atoi(key)
		if err != nil || dayNum < 0 || dayNum > 6 {
			continue
		}
		b, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var spans []domain.HoursWindow
		if err := json.Unmarshal(b, &spans); err != nil {
			continue
		}
		for _, s := range spans {
			start, err1 := parseClock(s.Start)
			end, err2 := parseClock(s.End)
			if err1 != nil || err2 != nil || end <= start {
				continue
			}
			c.windows[time.Weekday(dayNum)] = append(c.windows[time.Weekday(dayNum)], window{start, end})
		}
	}
	return c, nil
}

// AlwaysOpen reports whether the calendar imposes no restriction.
func (c *Calendar) AlwaysOpen() bool {
	return c.model.AlwaysOpen || len(c.windows) == 0
}

// IsOpen reports whether the city is open at t.
func (c *Calendar) IsOpen(t time.Time) bool {
	if c.AlwaysOpen() {
		return true
	}
	local := t.In(c.loc)
	if c.holidays[local.Format("2006-01-02")] {
		return false
	}
	minutes := local.Hour()*60 + local.Minute()
	for _, w := range c.windows[local.Weekday()] {
		if minutes >= w.startMin && minutes < w.endMin {
			return true
		}
	}
	return false
}

// Add returns the instant reached after adding the given number of business
// minutes to from, skipping closed hours and holidays.
func (c *Calendar) Add(from time.Time, businessMinutes int) time.Time {
	if c.AlwaysOpen() {
		return from.Add(time.Duration(businessMinutes) * time.Minute)
	}
	if businessMinutes <= 0 {
		return from
	}

	local := from.In(c.loc)
	remaining := businessMinutes

	// A year of days is a generous ceiling; it exists only so a pathological
	// calendar (every day a holiday) terminates rather than spinning.
	for range 400 {
		day := local
		dayKey := day.Format("2006-01-02")

		if !c.holidays[dayKey] {
			for _, w := range c.windows[day.Weekday()] {
				startOfDay := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, c.loc)
				winStart := startOfDay.Add(time.Duration(w.startMin) * time.Minute)
				winEnd := startOfDay.Add(time.Duration(w.endMin) * time.Minute)

				if local.After(winEnd) || local.Equal(winEnd) {
					continue
				}
				cursor := local
				if cursor.Before(winStart) {
					cursor = winStart
				}

				available := int(winEnd.Sub(cursor) / time.Minute)
				if available >= remaining {
					return cursor.Add(time.Duration(remaining) * time.Minute).UTC()
				}
				remaining -= available
				local = winEnd
			}
		}

		// Move to the start of the next day.
		next := local.AddDate(0, 0, 1)
		local = time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, c.loc)
	}

	// Fall back to wall time rather than returning a zero value, so a
	// misconfigured calendar produces a late-looking date instead of 1970.
	return from.Add(time.Duration(businessMinutes) * time.Minute)
}

// Between counts the business minutes elapsed between two instants.
func (c *Calendar) Between(from, to time.Time) int {
	if !to.After(from) {
		return 0
	}
	if c.AlwaysOpen() {
		return int(to.Sub(from) / time.Minute)
	}

	local := from.In(c.loc)
	end := to.In(c.loc)
	total := 0

	for range 400 {
		if !local.Before(end) {
			break
		}
		day := local
		if !c.holidays[day.Format("2006-01-02")] {
			for _, w := range c.windows[day.Weekday()] {
				startOfDay := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, c.loc)
				winStart := startOfDay.Add(time.Duration(w.startMin) * time.Minute)
				winEnd := startOfDay.Add(time.Duration(w.endMin) * time.Minute)

				lo, hi := maxTime(local, winStart), minTime(end, winEnd)
				if hi.After(lo) {
					total += int(hi.Sub(lo) / time.Minute)
				}
			}
		}
		next := local.AddDate(0, 0, 1)
		local = time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, c.loc)
	}
	return total
}

func parseClock(s string) (int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("catalog: bad clock time %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 24 {
		return 0, fmt.Errorf("catalog: bad hour in %q", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("catalog: bad minute in %q", s)
	}
	return h*60 + m, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// DefaultHours is a conventional Monday-to-Friday municipal schedule, used to
// seed the default calendar.
func DefaultHours() domain.JSONMap {
	weekday := []domain.HoursWindow{{Start: "08:30", End: "16:30"}}
	return domain.JSONMap{
		"1": weekday, "2": weekday, "3": weekday, "4": weekday, "5": weekday,
	}
}
