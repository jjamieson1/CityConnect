package seed

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/storetest"
)

// legacyCatalogue rebuilds what the table looked like before publish_state
// existed: an active flag alongside it, which is what an upgraded deployment
// actually has, since AutoMigrate adds columns and never drops them.
func legacyCatalogue(t *testing.T) (*gorm.DB, *slog.Logger) {
	t.Helper()
	db := storetest.New(t)
	if err := db.Exec("ALTER TABLE service_types ADD COLUMN active numeric DEFAULT 1").Error; err != nil {
		t.Fatalf("add legacy column: %v", err)
	}
	return db, slog.New(slog.NewTextHandler(io.Discard, nil))
}

func legacyService(t *testing.T, db *gorm.DB, code string, active bool) *domain.ServiceType {
	t.Helper()
	st := &domain.ServiceType{
		Code: code, Name: code, DefaultPriority: domain.PriorityNormal,
		// AutoMigrate's default for the new column, which is what every
		// pre-existing row gets on the upgrade.
		PublishState: domain.PublishPublished, PublicVisible: true,
	}
	if err := db.Create(st).Error; err != nil {
		t.Fatalf("create %s: %v", code, err)
	}
	if err := db.Exec("UPDATE service_types SET active = ? WHERE id = ?", active, st.ID).Error; err != nil {
		t.Fatalf("set legacy active on %s: %v", code, err)
	}
	return st
}

func stateOf(t *testing.T, db *gorm.DB, id string) domain.PublishState {
	t.Helper()
	var got domain.ServiceType
	if err := db.First(&got, "id = ?", id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	return got.PublishState
}

// The upgrade must not republish a catalogue a municipality had retired. The
// new column defaults to 'published', which is right for a live service and
// exactly wrong for one that was switched off.
func TestAdoptionArchivesWhatWasSwitchedOff(t *testing.T) {
	db, log := legacyCatalogue(t)

	live := legacyService(t, db, "POTHOLE", true)
	retired := legacyService(t, db, "PAYPHONE", false)

	if err := adoptPublishState(context.Background(), db, log); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	if got := stateOf(t, db, live.ID); got != domain.PublishPublished {
		t.Errorf("a live service became %q", got)
	}
	if got := stateOf(t, db, retired.ID); got != domain.PublishArchived {
		t.Errorf("a retired service came back as %q; the upgrade republished it", got)
	}
}

// It runs on every boot, so it must converge. The failure this guards against
// is subtle: the legacy column keeps its false, and an operator who republishes
// the service finds it archived again after the next restart.
func TestAdoptionDoesNotUndoALaterRepublish(t *testing.T) {
	db, log := legacyCatalogue(t)
	ctx := context.Background()

	retired := legacyService(t, db, "PAYPHONE", false)
	if err := adoptPublishState(ctx, db, log); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	// An operator brings it back, months later.
	if err := db.Model(&domain.ServiceType{}).Where("id = ?", retired.ID).
		UpdateColumn("publish_state", domain.PublishPublished).Error; err != nil {
		t.Fatalf("republish: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := adoptPublishState(ctx, db, log); err != nil {
			t.Fatalf("adopt pass %d: %v", i, err)
		}
	}

	if got := stateOf(t, db, retired.ID); got != domain.PublishPublished {
		t.Errorf("a republished service was archived again by boot %q", got)
	}
}

// A fresh deployment has no legacy column at all, and must not fail on its
// absence.
func TestAdoptionIsAQuietNoOpOnAFreshDatabase(t *testing.T) {
	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	st := &domain.ServiceType{
		Code: "POTHOLE", Name: "Pothole repair", DefaultPriority: domain.PriorityNormal,
		PublishState: domain.PublishPublished, PublicVisible: true,
	}
	if err := db.Create(st).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := adoptPublishState(context.Background(), db, log); err != nil {
		t.Fatalf("adopt on a fresh database: %v", err)
	}
	if got := stateOf(t, db, st.ID); got != domain.PublishPublished {
		t.Errorf("publish state = %q on a fresh database", got)
	}
}
