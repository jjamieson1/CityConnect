package catalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/storetest"
)

func newCategoryEnv(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(db, audit.NewService(db, log), log), db
}

func save(t *testing.T, s *Service, c *domain.ServiceCategory) *domain.ServiceCategory {
	t.Helper()
	out, err := s.SaveCategory(context.Background(), audit.JobActor("test"), c)
	if err != nil {
		t.Fatalf("save %q: %v", c.Name, err)
	}
	return out
}

// The requirement: browse "Roads & transport → Potholes", in the order staff
// chose rather than alphabetically.
func TestCategoryTreeNestsAndOrders(t *testing.T) {
	s, _ := newCategoryEnv(t)

	roads := save(t, s, &domain.ServiceCategory{Name: "Roads & transport", DisplayOrder: 1, Active: true})
	waste := save(t, s, &domain.ServiceCategory{Name: "Waste", DisplayOrder: 2, Active: true})
	save(t, s, &domain.ServiceCategory{Name: "Potholes", ParentID: roads.ID, DisplayOrder: 2, Active: true})
	save(t, s, &domain.ServiceCategory{Name: "Street lighting", ParentID: roads.ID, DisplayOrder: 1, Active: true})

	tree, err := s.CategoryTree(context.Background(), false)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("got %d top-level categories, want 2", len(tree))
	}
	// Display order, not alphabetical — "Roads" before "Waste" is both here,
	// so check the children where the two disagree.
	if tree[0].Name != roads.Name || tree[1].Name != waste.Name {
		t.Errorf("top level = %q, %q; want display order", tree[0].Name, tree[1].Name)
	}
	if len(tree[0].Children) != 2 {
		t.Fatalf("Roads has %d children, want 2", len(tree[0].Children))
	}
	// Street lighting is order 1 and Potholes order 2, so alphabetical would
	// put Potholes first and the configured order must not.
	if tree[0].Children[0].Name != "Street lighting" {
		t.Errorf("children in %q order; want the configured one",
			tree[0].Children[0].Name)
	}
}

// A cycle makes a tree walk that never ends. It has to be refused where a
// business user sees a sentence, not where they see a hung page.
func TestCategoryCannotBecomeItsOwnAncestor(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	parent := save(t, s, &domain.ServiceCategory{Name: "Roads", Active: true})
	child := save(t, s, &domain.ServiceCategory{Name: "Potholes", ParentID: parent.ID, Active: true})

	// Reparent the top-level category under its own child.
	parent.ParentID = child.ID
	if _, err := s.SaveCategory(ctx, audit.JobActor("test"), parent); err == nil {
		t.Error("a cycle was accepted")
	} else if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want an input error a business user can read", err)
	}

	// And the direct case.
	solo := save(t, s, &domain.ServiceCategory{Name: "Waste", Active: true})
	solo.ParentID = solo.ID
	if _, err := s.SaveCategory(ctx, audit.JobActor("test"), solo); err == nil {
		t.Error("a category was accepted as its own parent")
	}
}

// A rename must reach the services filed under it: the name is denormalised
// onto ServiceType, routing rules match on it, and both frontends read it.
func TestRenamingACategoryUpdatesItsServices(t *testing.T) {
	s, db := newCategoryEnv(t)
	ctx := context.Background()

	cat := save(t, s, &domain.ServiceCategory{Name: "Roads", Active: true})
	st, err := s.SaveServiceType(ctx, audit.JobActor("test"), &domain.ServiceType{
		Code: "POTHOLE", Name: "Pothole repair", CategoryID: cat.ID,
		Active: true, PublicVisible: true,
	})
	if err != nil {
		t.Fatalf("save service: %v", err)
	}
	if st.Category != "Roads" {
		t.Fatalf("service category = %q, want it taken from the category", st.Category)
	}

	cat.Name = "Roads & transport"
	save(t, s, cat)

	var after domain.ServiceType
	if err := db.First(&after, "id = ?", st.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Category != "Roads & transport" {
		t.Errorf("service still labelled %q after the rename; routing rules and "+
			"both frontends read this field", after.Category)
	}
}

// A service must not be labelled with a category it is not filed under, or a
// routing rule matching on the name routes it somewhere wrong.
func TestServiceCategoryNameComesFromTheCategoryNotTheCaller(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	cat := save(t, s, &domain.ServiceCategory{Name: "Waste", Active: true})

	st, err := s.SaveServiceType(ctx, audit.JobActor("test"), &domain.ServiceType{
		Code: "BINS", Name: "Missed collection", CategoryID: cat.ID,
		Category: "Something else entirely",
		Active:   true, PublicVisible: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if st.Category != "Waste" {
		t.Errorf("category = %q; the caller was allowed to choose the label", st.Category)
	}

	// And clearing the category clears the label rather than leaving it stale.
	st.CategoryID = ""
	cleared, err := s.SaveServiceType(ctx, audit.JobActor("test"), st)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if cleared.Category != "" {
		t.Errorf("category = %q after being unfiled, want empty", cleared.Category)
	}
}

// Deleting a category out from under live services would leave them
// uncategorised without anyone deciding that. Refuse it, in words.
func TestDeletingACategoryInUseIsRefused(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	cat := save(t, s, &domain.ServiceCategory{Name: "Roads", Active: true})
	if _, err := s.SaveServiceType(ctx, audit.JobActor("test"), &domain.ServiceType{
		Code: "POTHOLE", Name: "Pothole repair", CategoryID: cat.ID,
		Active: true, PublicVisible: true,
	}); err != nil {
		t.Fatalf("save service: %v", err)
	}

	err := s.DeleteCategory(ctx, audit.JobActor("test"), cat.ID)
	if err == nil {
		t.Fatal("a category with services under it was deleted")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want an input error", err)
	}
}

func TestDeletingACategoryWithChildrenIsRefused(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	parent := save(t, s, &domain.ServiceCategory{Name: "Roads", Active: true})
	save(t, s, &domain.ServiceCategory{Name: "Potholes", ParentID: parent.ID, Active: true})

	if err := s.DeleteCategory(ctx, audit.JobActor("test"), parent.ID); err == nil {
		t.Error("a category with children was deleted")
	}
}

// An empty category is a tidy-up a business user should be able to do.
func TestUnusedCategoryCanBeDeleted(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	cat := save(t, s, &domain.ServiceCategory{Name: "Obsolete", Active: true})
	if err := s.DeleteCategory(ctx, audit.JobActor("test"), cat.ID); err != nil {
		t.Errorf("deleting an unused category: %v", err)
	}
}

// The breadcrumb reads outermost first. Collected leaf-first, so the reversal
// is easy to get wrong and worth pinning.
func TestCategoryPathReadsOutermostFirst(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	roads := save(t, s, &domain.ServiceCategory{Name: "Roads & transport", Active: true})
	potholes := save(t, s, &domain.ServiceCategory{Name: "Potholes", ParentID: roads.ID, Active: true})

	path, err := s.CategoryPath(ctx, potholes.ID)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if len(path) != 2 {
		t.Fatalf("path has %d entries, want 2", len(path))
	}
	if path[0].Name != "Roads & transport" || path[1].Name != "Potholes" {
		t.Errorf("path = %q → %q, want outermost first", path[0].Name, path[1].Name)
	}
}

// A service that has not been filed anywhere should still be reportable rather
// than invisible.
func TestUncategorisedServiceIsAllowed(t *testing.T) {
	s, _ := newCategoryEnv(t)

	st, err := s.SaveServiceType(context.Background(), audit.JobActor("test"), &domain.ServiceType{
		Code: "GENERAL", Name: "General enquiry", Active: true, PublicVisible: true,
	})
	if err != nil {
		t.Fatalf("an uncategorised service was refused: %v", err)
	}
	if st.CategoryID != "" || st.Category != "" {
		t.Errorf("got category %q/%q, want none", st.CategoryID, st.Category)
	}
}
