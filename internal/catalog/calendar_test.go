package catalog

import (
	"testing"
	"time"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

func testCalendar(t *testing.T) *Calendar {
	t.Helper()
	cal, err := LoadCalendar(&domain.BusinessCalendar{
		Name:     "City Hall",
		TimeZone: "America/Toronto",
		Hours:    DefaultHours(),
		Holidays: domain.StringList{"2026-08-03"}, // Civic Holiday, a Monday
	})
	if err != nil {
		t.Fatalf("load calendar: %v", err)
	}
	return cal
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	loc, _ := time.LoadLocation("America/Toronto")
	ts, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestIsOpen(t *testing.T) {
	cal := testCalendar(t)

	cases := []struct {
		when string
		open bool
		why  string
	}{
		{"2026-08-06 10:00", true, "Thursday mid-morning"},
		{"2026-08-06 08:00", false, "before opening"},
		{"2026-08-06 16:30", false, "at closing, exclusive"},
		{"2026-08-06 20:00", false, "evening"},
		{"2026-08-08 10:00", false, "Saturday"},
		{"2026-08-03 10:00", false, "civic holiday"},
	}
	for _, tc := range cases {
		if got := cal.IsOpen(at(t, tc.when)); got != tc.open {
			t.Errorf("IsOpen(%s) = %v, want %v (%s)", tc.when, got, tc.open, tc.why)
		}
	}
}

// TestAddSkipsOvernight is the case that makes the whole exercise worthwhile:
// a request logged just before closing must not be judged late the next
// morning simply because wall-clock hours passed overnight.
func TestAddSkipsOvernight(t *testing.T) {
	cal := testCalendar(t)

	// 4 business hours from 3:30pm Thursday: 1 hour today, 3 tomorrow.
	got := cal.Add(at(t, "2026-08-06 15:30"), 4*60)
	want := at(t, "2026-08-07 11:30")

	if !got.Equal(want.UTC()) {
		t.Errorf("Add = %s, want %s", got.In(want.Location()), want)
	}
}

func TestAddSkipsWeekendAndHoliday(t *testing.T) {
	cal := testCalendar(t)

	// Friday 3:30pm + 4 business hours. Friday gives 1 hour; Saturday and
	// Sunday are closed; Monday 3 August is the civic holiday; so it lands
	// Tuesday morning.
	got := cal.Add(at(t, "2026-07-31 15:30"), 4*60)
	want := at(t, "2026-08-04 11:30")

	if !got.Equal(want.UTC()) {
		t.Errorf("Add = %s, want %s", got.In(want.Location()), want)
	}
}

func TestAddFromClosedTimeStartsAtNextOpening(t *testing.T) {
	cal := testCalendar(t)

	// Reported Sunday afternoon; the clock starts Monday at 08:30.
	got := cal.Add(at(t, "2026-08-09 14:00"), 60)
	want := at(t, "2026-08-10 09:30")

	if !got.Equal(want.UTC()) {
		t.Errorf("Add = %s, want %s", got.In(want.Location()), want)
	}
}

func TestBetweenCountsOnlyOpenHours(t *testing.T) {
	cal := testCalendar(t)

	// Thursday 15:30 to Friday 11:30 is 20 wall hours but 4 business hours.
	got := cal.Between(at(t, "2026-08-06 15:30"), at(t, "2026-08-07 11:30"))
	if got != 240 {
		t.Errorf("Between = %d minutes, want 240", got)
	}
}

func TestBetweenAcrossClosedWeekendIsZero(t *testing.T) {
	cal := testCalendar(t)

	got := cal.Between(at(t, "2026-08-08 09:00"), at(t, "2026-08-09 17:00"))
	if got != 0 {
		t.Errorf("Between over a closed weekend = %d, want 0", got)
	}
}

func TestAlwaysOpenCalendarUsesWallTime(t *testing.T) {
	cal, err := LoadCalendar(&domain.BusinessCalendar{
		Name: "24/7", TimeZone: "America/Toronto", AlwaysOpen: true,
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	start := at(t, "2026-08-08 22:00") // Saturday night
	got := cal.Add(start, 4*60)
	if want := start.Add(4 * time.Hour); !got.Equal(want.UTC()) {
		t.Errorf("Add = %s, want %s", got, want.UTC())
	}
	if !cal.IsOpen(start) {
		t.Error("an always-open calendar must be open on a Saturday night")
	}
}

func TestNilCalendarIsAlwaysOpen(t *testing.T) {
	cal, err := LoadCalendar(nil)
	if err != nil {
		t.Fatalf("load nil: %v", err)
	}
	if !cal.AlwaysOpen() {
		t.Error("a nil calendar must behave as always open")
	}
}
