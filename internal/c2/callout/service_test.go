package callout

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

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
	return &Service{
		db:  db,
		cfg: cfg,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
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
	if len(links) != 3 {
		t.Fatalf("expected a CTA and two task links, got %d links: %v", len(links), links)
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
	if len(b.Tasks) != 0 {
		t.Errorf("expected no tasks, got %d", len(b.Tasks))
	}
	if b.Description == "" {
		t.Error("expected a description saying there are no open requests")
	}
}
