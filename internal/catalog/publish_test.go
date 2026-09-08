package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
)

func saveType(t *testing.T, s *Service, st *domain.ServiceType) *domain.ServiceType {
	t.Helper()
	if st.DefaultPriority == "" {
		st.DefaultPriority = domain.PriorityNormal
	}
	out, err := s.SaveServiceType(context.Background(), audit.JobActor("test"), st)
	if err != nil {
		t.Fatalf("save %q: %v", st.Code, err)
	}
	return out
}

func has(types []domain.ServiceType, code string) bool {
	for _, st := range types {
		if st.Code == code {
			return true
		}
	}
	return false
}

// A service being written must not be reachable by residents while it is being
// written. This is the whole reason the draft state exists.
func TestADraftIsInvisibleUntilItIsPublished(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	draft := saveType(t, s, &domain.ServiceType{
		Code: "TREES", Name: "Tree pruning",
		PublishState: domain.PublishDraft, PublicVisible: true,
	})

	public, err := s.ListServiceTypes(ctx, ServiceTypeFilter{PublicOnly: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if has(public, "TREES") {
		t.Error("a draft service was offered to the public")
	}

	// The console still sees it, otherwise it could never be finished.
	all, err := s.ListServiceTypes(ctx, ServiceTypeFilter{IncludeHidden: true})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if !has(all, "TREES") {
		t.Fatalf("the configuration console cannot see its own draft: %s", codes(all))
	}

	draft.PublishState = domain.PublishPublished
	saveType(t, s, draft)

	public, _ = s.ListServiceTypes(ctx, ServiceTypeFilter{PublicOnly: true})
	if !has(public, "TREES") {
		t.Error("publishing did not make the service available")
	}
}

// Creating an entry with nothing chosen must not publish it. The column default
// is 'published' so that an existing catalogue survives the upgrade, and that
// default must not leak into new records.
func TestANewServiceDefaultsToDraftRatherThanLive(t *testing.T) {
	s, _ := newCategoryEnv(t)

	created := saveType(t, s, &domain.ServiceType{Code: "NEW", Name: "Something new"})
	if created.PublishState != domain.PublishDraft {
		t.Errorf("a new service was created %q; a half-written service reached the portal",
			created.PublishState)
	}
}

// "Live from April 1 for the spring cleanup" is how a municipality actually
// runs seasonal services.
func TestADatedServiceAppearsOnlyInsideItsWindow(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	april := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	october := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	saveType(t, s, &domain.ServiceType{
		Code: "YARDWASTE", Name: "Yard waste collection",
		PublishState: domain.PublishPublished, PublicVisible: true,
		EffectiveStart: &april, EffectiveEnd: &october,
	})

	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"the day before it starts", april.Add(-time.Hour), false},
		{"the moment it starts", april, true},
		{"mid-season", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), true},
		{"the moment it ends", october, false},
		{"after the season", october.Add(time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListServiceTypes(ctx, ServiceTypeFilter{PublicOnly: true, At: tc.at})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if has(got, "YARDWASTE") != tc.want {
				t.Errorf("offered = %v, want %v", !tc.want, tc.want)
			}
		})
	}
}

// An unbounded service is the ordinary case and must not be filtered by a date
// nobody set.
func TestAServiceWithNoDatesIsAlwaysInWindow(t *testing.T) {
	s, _ := newCategoryEnv(t)

	saveType(t, s, &domain.ServiceType{
		Code: "POTHOLE", Name: "Pothole repair",
		PublishState: domain.PublishPublished, PublicVisible: true,
	})

	for _, at := range []time.Time{
		time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Now(),
		time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		got, err := s.ListServiceTypes(context.Background(),
			ServiceTypeFilter{PublicOnly: true, At: at})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !has(got, "POTHOLE") {
			t.Errorf("an undated service disappeared at %s", at)
		}
	}
}

func TestAWindowThatEndsBeforeItStartsIsRefused(t *testing.T) {
	s, _ := newCategoryEnv(t)

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	_, err := s.SaveServiceType(context.Background(), audit.JobActor("test"), &domain.ServiceType{
		Code: "BACKWARDS", Name: "Backwards", DefaultPriority: domain.PriorityNormal,
		EffectiveStart: &start, EffectiveEnd: &end,
	})
	if err == nil {
		t.Error("a service was saved with a window that can never be open")
	}
}

// Archiving is not deletion. A request filed years ago must still be able to
// say what was asked for.
func TestAnArchivedServiceStillResolvesOnAnOldRequest(t *testing.T) {
	s, db := newCategoryEnv(t)
	ctx := context.Background()

	st := saveType(t, s, &domain.ServiceType{
		Code: "PAYPHONE", Name: "Payphone repair",
		PublishState: domain.PublishPublished, PublicVisible: true,
	})
	if err := db.Create(&domain.Request{
		Reference: "SR-ABCD-EFGH", ServiceTypeID: st.ID, Subject: "Broken payphone",
	}).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}

	if err := s.DeleteServiceType(ctx, audit.JobActor("test"), st.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := s.GetServiceType(ctx, st.ID)
	if err != nil {
		t.Fatalf("an archived service no longer resolves: %v", err)
	}
	if got.PublishState != domain.PublishArchived {
		t.Errorf("publish state = %q, want archived", got.PublishState)
	}
	if got.Name != "Payphone repair" {
		t.Errorf("the name a citizen would see was lost: %q", got.Name)
	}

	public, _ := s.ListServiceTypes(ctx, ServiceTypeFilter{PublicOnly: true})
	if has(public, "PAYPHONE") {
		t.Error("an archived service is still being offered")
	}
}

// Staff choose which services are promoted, and in what order.
func TestPromotedServicesKeepTheOrderStaffChose(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	live := func(code, name string) string {
		return saveType(t, s, &domain.ServiceType{
			Code: code, Name: name,
			PublishState: domain.PublishPublished, PublicVisible: true,
		}).ID
	}
	// Deliberately not alphabetical, and not creation order.
	bins := live("BINS", "Missed collection")
	pothole := live("POTHOLE", "Pothole repair")
	noise := live("NOISE", "Noise complaint")

	if err := s.SetPromoted(ctx, audit.JobActor("test"), []string{pothole, noise, bins}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	got, err := s.PromotedServices(ctx)
	if err != nil {
		t.Fatalf("promoted: %v", err)
	}
	if order := codes(got); order != "POTHOLE,NOISE,BINS" {
		t.Errorf("promoted = %s, want POTHOLE,NOISE,BINS", order)
	}

	// A reorder is one operation, and a service dropped from the list stops
	// being promoted rather than lingering at position zero.
	if err := s.SetPromoted(ctx, audit.JobActor("test"), []string{noise, pothole}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	got, _ = s.PromotedServices(ctx)
	if order := codes(got); order != "NOISE,POTHOLE" {
		t.Errorf("after reorder promoted = %s, want NOISE,POTHOLE", order)
	}
}

// The portal's front page is the most expensive place for a mistake to land.
func TestOnlyALiveServiceCanBePromoted(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	draft := saveType(t, s, &domain.ServiceType{
		Code: "DRAFT", Name: "Not ready",
		PublishState: domain.PublishDraft, PublicVisible: true,
	})
	hidden := saveType(t, s, &domain.ServiceType{
		Code: "INTERNAL", Name: "Staff only",
		PublishState: domain.PublishPublished, PublicVisible: false,
	})

	for name, id := range map[string]string{"a draft": draft.ID, "a staff-only service": hidden.ID} {
		if err := s.SetPromoted(ctx, audit.JobActor("test"), []string{id}); err == nil {
			t.Errorf("%s was promoted to the portal's landing view", name)
		}
	}
}

// Archiving a promoted service must take it off the landing view in the same
// breath. A shortcut to a withdrawn service is a dead end a resident finds by
// clicking it.
func TestArchivingRemovesAServiceFromTheLandingView(t *testing.T) {
	s, db := newCategoryEnv(t)
	ctx := context.Background()

	st := saveType(t, s, &domain.ServiceType{
		Code: "SKATING", Name: "Outdoor rink booking",
		PublishState: domain.PublishPublished, PublicVisible: true,
	})
	if err := s.SetPromoted(ctx, audit.JobActor("test"), []string{st.ID}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := db.Create(&domain.Request{
		Reference: "SR-WXYZ-1234", ServiceTypeID: st.ID, Subject: "Rink",
	}).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}

	if err := s.DeleteServiceType(ctx, audit.JobActor("test"), st.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	got, _ := s.PromotedServices(ctx)
	if len(got) != 0 {
		t.Errorf("an archived service is still promoted: %s", codes(got))
	}
}

// The promoted list has one writer. An ordinary edit must not be able to claim
// a position, or to skip the checks SetPromoted applies.
func TestAnOrdinarySaveCannotPromoteAService(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	sneaky := saveType(t, s, &domain.ServiceType{
		Code: "SNEAKY", Name: "Promoted by the back door",
		PublishState: domain.PublishPublished, PublicVisible: true,
		Promoted: true, PromotedOrder: 0,
	})
	if sneaky.Promoted {
		t.Error("a save set the promoted flag directly")
	}
	got, _ := s.PromotedServices(ctx)
	if len(got) != 0 {
		t.Errorf("promoted list = %s, want empty", codes(got))
	}

	// And an edit to a genuinely promoted service must not silently drop it.
	if err := s.SetPromoted(ctx, audit.JobActor("test"), []string{sneaky.ID}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	sneaky.Name = "Renamed"
	saveType(t, s, sneaky)

	got, _ = s.PromotedServices(ctx)
	if len(got) != 1 {
		t.Errorf("editing a promoted service dropped it from the landing view: %s", codes(got))
	}
}
