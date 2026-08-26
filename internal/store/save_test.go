package store

import (
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/storetest"
)

// gorm omits zero-valued fields on insert and lets the database apply the
// column default, so `false` on a `default:true` column is stored as `true`.
// The record comes back the opposite of what was asked for, with no error.
//
// The dangerous direction is "create this, but switched off" becoming "create
// this, switched on" — a catalogue entry staged as a draft, or a routing rule
// meant to be simulated before it goes live.
func TestSaveWritesFalseOnDefaultTrueColumns(t *testing.T) {
	db := storetest.New(t)

	st := &domain.ServiceType{
		Code: "DRAFT", Name: "Not launched yet", DefaultPriority: "normal",
		Active: false, PublicVisible: false, AllowsAttachments: false,
	}
	if err := Save(db, st, st.ID); err != nil {
		t.Fatalf("save: %v", err)
	}

	if st.ID == "" {
		t.Fatal("no primary key was assigned; the row cannot be found again")
	}
	// Reload by primary key and fail if it is missing. An earlier version of
	// this helper wrote rows with an empty id, and a test that tolerated a
	// missing row read false off a zero-valued struct and passed.
	var got domain.ServiceType
	if err := db.First(&got, "id = ?", st.ID).Error; err != nil {
		t.Fatalf("reload by id %q: %v", st.ID, err)
	}
	if got.Active || got.PublicVisible || got.AllowsAttachments {
		t.Errorf("stored active=%v public=%v attachments=%v, want all false",
			got.Active, got.PublicVisible, got.AllowsAttachments)
	}
	if got.ID == "" {
		t.Error("the primary key was not assigned")
	}
}

// True values and non-boolean fields must survive the same path.
func TestSaveKeepsOrdinaryValues(t *testing.T) {
	db := storetest.New(t)

	st := &domain.ServiceType{
		Code: "POTHOLE", Name: "Pothole repair", Category: "Roads",
		Description: "A hole in the road.", DefaultPriority: "high",
		Active: true, PublicVisible: true, RequiresLocation: true,
	}
	if err := Save(db, st, st.ID); err != nil {
		t.Fatalf("save: %v", err)
	}

	var got domain.ServiceType
	if err := db.First(&got, "id = ?", st.ID).Error; err != nil {
		t.Fatalf("reload by id %q: %v", st.ID, err)
	}
	if !got.Active || !got.PublicVisible || !got.RequiresLocation {
		t.Error("a true flag was lost")
	}
	if got.Name != "Pothole repair" || got.Category != "Roads" || got.DefaultPriority != "high" {
		t.Errorf("field values were lost: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set")
	}
}

// With a primary key present this is an update, where gorm already writes zero
// values — but it must still round-trip a flag being switched off.
func TestSaveUpdatesExistingRecords(t *testing.T) {
	db := storetest.New(t)

	st := &domain.ServiceType{Code: "NOISE", Name: "Noise complaint",
		DefaultPriority: "normal", Active: true, PublicVisible: true}
	if err := Save(db, st, st.ID); err != nil {
		t.Fatalf("create: %v", err)
	}

	st.Active = false
	st.Name = "Noise complaint (retired)"
	if err := Save(db, st, st.ID); err != nil {
		t.Fatalf("update: %v", err)
	}

	var got domain.ServiceType
	if err := db.First(&got, "id = ?", st.ID).Error; err != nil {
		t.Fatalf("reload by id %q: %v", st.ID, err)
	}
	if got.Active {
		t.Error("deactivating an existing service type did not persist")
	}
	if got.Name != "Noise complaint (retired)" {
		t.Errorf("Name = %q", got.Name)
	}
}

// Only the false flags are repaired — a record with nothing to fix must not
// trigger a second statement.
func TestFalseDefaultedColumns(t *testing.T) {
	db := storetest.New(t)

	all := falseDefaultedColumns(db, &domain.ServiceType{
		Active: false, PublicVisible: false, AllowsAttachments: false,
	})
	for _, want := range []string{"active", "public_visible", "allows_attachments"} {
		if _, ok := all[want]; !ok {
			t.Errorf("%s not flagged for repair; its default would stand", want)
		}
	}
	// RequiresLocation defaults to false, so an insert stores it correctly and
	// it needs no repair.
	if _, ok := all["requires_location"]; ok {
		t.Error("requires_location does not default to true; repairing it is noise")
	}

	none := falseDefaultedColumns(db, &domain.ServiceType{
		Active: true, PublicVisible: true, AllowsAttachments: true,
	})
	if len(none) != 0 {
		t.Errorf("nothing needed repair, got %v", none)
	}
}
