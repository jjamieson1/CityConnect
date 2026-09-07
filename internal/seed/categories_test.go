package seed

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/storetest"
)

// A municipality already has a catalogue with categories they chose. Adopting
// the tree must not silently recategorise it on a deploy.
func TestAdoptionPreservesAnExistingCatalogue(t *testing.T) {
	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// Services as they were before the tree existed: a category name and no id.
	for _, st := range []domain.ServiceType{
		{Code: "POTHOLE", Name: "Pothole repair", Category: "Roads"},
		{Code: "STREETLIGHT", Name: "Streetlight outage", Category: "Roads"},
		{Code: "BINS", Name: "Missed collection", Category: "Waste"},
		{Code: "GENERAL", Name: "General enquiry"}, // never categorised
	} {
		if err := db.Create(&st).Error; err != nil {
			t.Fatalf("seed %s: %v", st.Code, err)
		}
	}

	if err := adoptFlatCategories(ctx, db, log); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	// Two distinct names became two categories — not three, and not one per
	// service.
	var categories []domain.ServiceCategory
	if err := db.Order("name ASC").Find(&categories).Error; err != nil {
		t.Fatalf("categories: %v", err)
	}
	if len(categories) != 2 {
		t.Fatalf("created %d categories, want 2 (%v)", len(categories), names(categories))
	}
	if categories[0].Name != "Roads" || categories[1].Name != "Waste" {
		t.Errorf("categories = %v, want the names the City already had", names(categories))
	}

	// Both Roads services point at the same category.
	var pothole, streetlight, general domain.ServiceType
	db.First(&pothole, "code = ?", "POTHOLE")
	db.First(&streetlight, "code = ?", "STREETLIGHT")
	db.First(&general, "code = ?", "GENERAL")

	if pothole.CategoryID == "" {
		t.Error("an existing service was left uncategorised")
	}
	if pothole.CategoryID != streetlight.CategoryID {
		t.Error("two services sharing a category name were split across two categories")
	}
	// A service that never had a category still has none, rather than being
	// filed somewhere arbitrary.
	if general.CategoryID != "" {
		t.Errorf("an uncategorised service was filed under %q", general.CategoryID)
	}
	// And the label survives, because routing rules match on it.
	if pothole.Category != "Roads" {
		t.Errorf("category label = %q, want it preserved", pothole.Category)
	}
}

// It runs on every boot, so a second pass must do nothing rather than
// duplicating what the first one built.
func TestAdoptionIsIdempotent(t *testing.T) {
	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	if err := db.Create(&domain.ServiceType{
		Code: "POTHOLE", Name: "Pothole repair", Category: "Roads",
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := adoptFlatCategories(ctx, db, log); err != nil {
			t.Fatalf("adopt pass %d: %v", i, err)
		}
	}

	var count int64
	if err := db.Model(&domain.ServiceCategory{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("three passes produced %d categories, want 1", count)
	}
}

// A category a municipality created deliberately must be reused rather than
// duplicated under a different case.
func TestAdoptionReusesAnExistingCategory(t *testing.T) {
	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	existing := domain.ServiceCategory{Name: "Roads", Active: true}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if err := db.Create(&domain.ServiceType{
		Code: "POTHOLE", Name: "Pothole repair", Category: "roads",
	}).Error; err != nil {
		t.Fatalf("seed service: %v", err)
	}

	if err := adoptFlatCategories(ctx, db, log); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	var count int64
	db.Model(&domain.ServiceCategory{}).Count(&count)
	if count != 1 {
		t.Errorf("a differently-cased name created a second category (%d total)", count)
	}

	var st domain.ServiceType
	db.First(&st, "code = ?", "POTHOLE")
	if st.CategoryID != existing.ID {
		t.Error("the service was not pointed at the category that already existed")
	}
}

func names(cs []domain.ServiceCategory) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}
