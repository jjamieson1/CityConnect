package callout

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/storetest"
)

// The two origins used throughout. They share no prefix, so a link built from
// the wrong one cannot accidentally pass.
const (
	consoleURL = "https://staff.example.gov"
	portalURL  = "https://services.example.gov"
)

func testService(t *testing.T) *Service {
	t.Helper()
	db := storetest.New(t)
	cfg := &config.Config{
		PublicURL:       consoleURL,
		BasePath:        "/cityconnect",
		PortalPublicURL: portalURL,
	}
	cfg.C2.CalloutMaxTasks = 10
	cfg.C2.CalloutQuickLinks = []string{"GENERAL", "MISSED-COLLECTION"}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Service{
		db:      db,
		cfg:     cfg,
		catalog: catalog.NewService(db, audit.NewService(db, log), log),
		log:     log,
	}
}

// seedServiceType puts one bookable service in the catalogue.
func seedServiceType(t *testing.T, s *Service, code, name string, active, public bool) {
	t.Helper()
	st := &domain.ServiceType{
		Code: code, Name: name, Description: name + " description.",
		Active: active, PublicVisible: public, DefaultPriority: "normal",
	}
	if err := s.db.Create(st).Error; err != nil {
		t.Fatalf("seed service type %s: %v", code, err)
	}
	// Active and PublicVisible are `default:true` columns, so GORM omits them
	// on insert when they are false and the database fills in true. Writing
	// them explicitly is the only way to seed a retired or hidden service.
	if err := s.db.Model(st).Updates(map[string]any{
		"active": active, "public_visible": public,
	}).Error; err != nil {
		t.Fatalf("set flags on %s: %v", code, err)
	}
}

// byName indexes a bundle's tasks so assertions read by intent.
func byName(tasks []Task) map[string]Task {
	out := make(map[string]Task, len(tasks))
	for _, t := range tasks {
		out[t.Name] = t
	}
	return out
}

// Every link in a status bundle is opened by a citizen from their C2 dashboard.
// A citizen has no staff console account, so a console link ends at a sign-in
// page that refuses them — a dead end they cannot diagnose, on a surface the
// City does not control.
//
// This has now regressed twice: once when the portal moved from a path on the
// console to its own host, and once when the CTA was corrected but the
// per-task links were missed. Hence a test that walks the whole bundle rather
// than asserting on one field.
func TestBundleLinksAreOnThePortalOrigin(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	now := time.Now()
	open := []domain.Request{
		{Reference: "SR-2026-000001", Subject: "Pothole on Elm Street",
			Status: domain.StatusInProgress, LastActivityA: now},
		{Reference: "SR-2026-000002", Subject: "Streetlight out",
			Status: domain.StatusNew, LastActivityA: now.Add(-48 * time.Hour)},
	}

	b := s.render(ctx, &domain.Contact{Base: domain.Base{ID: "c1"}}, open)

	links := map[string]string{"CTA": b.CTA}
	for _, task := range b.Tasks {
		links["task "+task.Name] = task.URL
	}
	// Deliberately not a fixed count: the point is that *every* link is on the
	// portal, whatever rows the card happens to carry.
	if len(links) < 3 {
		t.Fatalf("expected a CTA and at least two task links, got %d: %v", len(links), links)
	}

	for name, got := range links {
		switch {
		case got == "":
			t.Errorf("%s: empty link; C2 renders the card without it", name)
		case !strings.HasPrefix(got, portalURL):
			t.Errorf("%s = %q, want a link on the citizen portal %s", name, got, portalURL)
		case strings.Contains(got, "/cityconnect"):
			// The portal is its own host at the root of that host. The console's
			// base path has no meaning there.
			t.Errorf("%s = %q, carries the staff console's base path", name, got)
		}
	}

	// The reference is what the citizen sees quoted everywhere else, and what
	// the portal routes on — so the deep link must be keyed on it.
	if want := portalURL + "/requests/SR-2026-000001"; b.Tasks[0].URL != want {
		t.Errorf("task URL = %q, want %q", b.Tasks[0].URL, want)
	}
}

// The empty bundle still carries a CTA: a citizen with no open requests is
// exactly who should be invited to open one.
func TestEmptyBundleStillLinksToThePortal(t *testing.T) {
	s := testService(t)

	b := s.render(context.Background(), &domain.Contact{Base: domain.Base{ID: "c1"}}, nil)

	if b.CTA != portalURL {
		t.Errorf("CTA = %q, want %q", b.CTA, portalURL)
	}
	// No open requests is precisely when the ways to start one matter most, so
	// the card is not empty — it offers the catalogue.
	if len(b.Tasks) == 0 {
		t.Error("a citizen with nothing open was offered no way to report anything")
	}
	for _, task := range b.Tasks {
		if strings.Contains(task.Name, "SR-") {
			t.Errorf("unexpected request row %q on an empty bundle", task.Name)
		}
	}
	if b.Description == "" {
		t.Error("expected a description saying there are no open requests")
	}
}

// The card offers ways to start something new alongside anything already open:
// two named services, then the whole catalogue.
func TestQuickLinksOfferNamedServicesAndTheCatalogue(t *testing.T) {
	s := testService(t)
	seedServiceType(t, s, "GENERAL", "General enquiry", true, true)
	seedServiceType(t, s, "MISSED-COLLECTION", "Missed waste collection", true, true)

	b := s.render(context.Background(), &domain.Contact{Base: domain.Base{ID: "c1"}}, nil)
	tasks := byName(b.Tasks)

	for name, wantURL := range map[string]string{
		"General enquiry":          portalURL + "/new/GENERAL",
		"Missed waste collection":  portalURL + "/new/MISSED-COLLECTION",
		"Browse all city services": portalURL + "/",
	} {
		task, ok := tasks[name]
		if !ok {
			t.Errorf("no %q row on the card; got %v", name, b.Tasks)
			continue
		}
		if task.URL != wantURL {
			t.Errorf("%q URL = %q, want %q", name, task.URL, wantURL)
		}
		if task.Description == "" {
			t.Errorf("%q has no description; the row renders as a bare link", name)
		}
	}

	// Browsing everything is the fallback, so it belongs last.
	if last := b.Tasks[len(b.Tasks)-1]; last.Name != "Browse all city services" {
		t.Errorf("last row = %q, want the browse-everything row", last.Name)
	}
}

// The names come from the catalogue, not from the configuration, so renaming a
// service in the console follows through to every citizen's card.
func TestQuickLinkNamesFollowTheCatalogue(t *testing.T) {
	s := testService(t)
	seedServiceType(t, s, "GENERAL", "Ask the City a question", true, true)

	b := s.render(context.Background(), &domain.Contact{Base: domain.Base{ID: "c1"}}, nil)

	if _, ok := byName(b.Tasks)["Ask the City a question"]; !ok {
		t.Errorf("the card did not use the catalogue's name; got %v", b.Tasks)
	}
}

// A configured code that no longer resolves to something a citizen can submit
// must be dropped, not advertised.
//
// C2 renders the card silently and a citizen who follows a dead link rarely
// reports it, so a stale code would sit on every dashboard indefinitely. The
// test the portal applies before accepting a report is the one applied here.
func TestQuickLinksSkipWhatTheCitizenCannotSubmit(t *testing.T) {
	cases := []struct {
		name           string
		active, public bool
	}{
		{"retired", false, true},
		{"hidden from the public", true, false},
		{"both", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t)
			seedServiceType(t, s, "GENERAL", "General enquiry", tc.active, tc.public)
			// MISSED-COLLECTION is never seeded here: an unknown code must be
			// skipped just as firmly as an unusable one.

			b := s.render(context.Background(), &domain.Contact{Base: domain.Base{ID: "c1"}}, nil)

			for _, task := range b.Tasks {
				if strings.Contains(task.URL, "/new/") {
					t.Errorf("offered %q at %q, which the portal would refuse", task.Name, task.URL)
				}
			}
			// The catalogue link survives — it needs no lookup and cannot go stale.
			if len(b.Tasks) != 1 || b.Tasks[0].Name != "Browse all city services" {
				t.Errorf("expected only the browse row, got %v", b.Tasks)
			}
		})
	}
}

// A citizen with a full card is there to see their own requests. Quick links
// take the remainder and never more than half.
func TestOpenRequestsKeepAtLeastHalfTheCard(t *testing.T) {
	s := testService(t)
	seedServiceType(t, s, "GENERAL", "General enquiry", true, true)
	seedServiceType(t, s, "MISSED-COLLECTION", "Missed waste collection", true, true)

	now := time.Now()
	var open []domain.Request
	for i := 0; i < 20; i++ {
		open = append(open, domain.Request{
			Reference: fmt.Sprintf("SR-2026-%06d", i+1), Subject: "Something",
			Status: domain.StatusNew, LastActivityA: now,
		})
	}

	for _, max := range []int{10, 4, 2, 1} {
		t.Run(fmt.Sprintf("max=%d", max), func(t *testing.T) {
			s.cfg.C2.CalloutMaxTasks = max

			b := s.render(context.Background(), &domain.Contact{Base: domain.Base{ID: "c1"}}, open)

			if len(b.Tasks) > max {
				t.Errorf("card carries %d rows, over the %d budget", len(b.Tasks), max)
			}
			var requests int
			for _, task := range b.Tasks {
				if strings.Contains(task.URL, "/requests/") {
					requests++
				}
			}
			if want := (max + 1) / 2; requests < want {
				t.Errorf("only %d of %d rows are the citizen's own requests, want at least %d",
					requests, len(b.Tasks), want)
			}
		})
	}
}
