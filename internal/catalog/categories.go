package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// maxCategoryDepth bounds the tree. Two levels is what the requirement asks
// for and what a resident can hold in their head; the limit exists so a
// mis-parented category cannot produce a chain nobody can navigate.
const maxCategoryDepth = 3

// ListCategories returns every category, ordered as staff arranged them.
func (s *Service) ListCategories(ctx context.Context, includeInactive bool) ([]domain.ServiceCategory, error) {
	q := s.db.WithContext(ctx).Model(&domain.ServiceCategory{})
	if !includeInactive {
		q = q.Where("active = ?", true)
	}
	var out []domain.ServiceCategory
	err := q.Order("display_order ASC, name ASC").Find(&out).Error
	return out, store.Translate(err)
}

// CategoryTree returns the categories as a tree, each level in its configured
// order.
//
// Assembled in Go from one query rather than fetched recursively: a municipal
// catalogue has tens of categories, and a recursive CTE would be one more thing
// that behaves differently on SQLite in tests than on MySQL in production.
func (s *Service) CategoryTree(ctx context.Context, includeInactive bool) ([]domain.ServiceCategory, error) {
	flat, err := s.ListCategories(ctx, includeInactive)
	if err != nil {
		return nil, err
	}

	byParent := map[string][]domain.ServiceCategory{}
	for _, c := range flat {
		byParent[c.ParentID] = append(byParent[c.ParentID], c)
	}

	var build func(parentID string, depth int) []domain.ServiceCategory
	build = func(parentID string, depth int) []domain.ServiceCategory {
		if depth > maxCategoryDepth {
			return nil
		}
		nodes := byParent[parentID]
		out := make([]domain.ServiceCategory, 0, len(nodes))
		for _, n := range nodes {
			n.Children = build(n.ID, depth+1)
			out = append(out, n)
		}
		return out
	}
	return build("", 1), nil
}

// SaveCategory creates or updates a category.
//
// A rename propagates to every service filed under it, because the name is
// denormalised onto ServiceType — routing rules match on it and both frontends
// read it. Without the propagation a rename would leave services labelled with
// the old name and routing rules matching nothing, which is exactly the "rename
// that breaks every service pointing at it" this model exists to avoid.
func (s *Service) SaveCategory(ctx context.Context, actor audit.Actor, c *domain.ServiceCategory) (*domain.ServiceCategory, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return nil, fmt.Errorf("%w: a category needs a name", ErrInvalidInput)
	}

	if c.ParentID != "" {
		if err := s.validateParent(ctx, c); err != nil {
			return nil, err
		}
	}

	var previousName string
	if c.ID != "" {
		var existing domain.ServiceCategory
		if err := s.db.WithContext(ctx).First(&existing, "id = ?", c.ID).Error; err != nil {
			return nil, ErrNotFound
		}
		previousName = existing.Name
	}

	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := store.Save(tx, c, c.ID); err != nil {
			return err
		}
		if previousName != "" && previousName != c.Name {
			if err := tx.Model(&domain.ServiceType{}).
				Where("category_id = ?", c.ID).
				UpdateColumn("category", c.Name).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "catalog.category_saved", TargetType: "service_category", TargetID: c.ID,
		Summary: c.Name,
	})
	return c, nil
}

// validateParent refuses a parent that would make a cycle or an unreadable
// depth.
//
// A category that is its own ancestor produces a tree walk that never ends, and
// the place to refuse it is at save time where a business user sees a sentence
// — not at render time where they see a hung page.
func (s *Service) validateParent(ctx context.Context, c *domain.ServiceCategory) error {
	if c.ParentID == c.ID && c.ID != "" {
		return fmt.Errorf("%w: a category cannot be its own parent", ErrInvalidInput)
	}

	var parent domain.ServiceCategory
	if err := s.db.WithContext(ctx).First(&parent, "id = ?", c.ParentID).Error; err != nil {
		return fmt.Errorf("%w: unknown parent category", ErrInvalidInput)
	}

	// Walk up from the proposed parent. Meeting this category on the way means
	// the move would close a loop.
	seen := parent
	for depth := 1; seen.ParentID != ""; depth++ {
		if seen.ParentID == c.ID {
			return fmt.Errorf("%w: that would make %q a descendant of itself", ErrInvalidInput, c.Name)
		}
		if depth >= maxCategoryDepth {
			return fmt.Errorf("%w: categories may be nested %d deep at most",
				ErrInvalidInput, maxCategoryDepth)
		}
		var next domain.ServiceCategory
		if err := s.db.WithContext(ctx).First(&next, "id = ?", seen.ParentID).Error; err != nil {
			break
		}
		seen = next
	}
	return nil
}

// DeleteCategory removes a category that nothing is using.
//
// Refused while services or child categories still point at it, and refused
// with a sentence rather than a foreign-key error: the person doing this is a
// business user in an admin screen, and "constraint violation" tells them
// nothing about what to do next.
func (s *Service) DeleteCategory(ctx context.Context, actor audit.Actor, id string) error {
	var c domain.ServiceCategory
	if err := s.db.WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
		return ErrNotFound
	}

	var services int64
	if err := s.db.WithContext(ctx).Model(&domain.ServiceType{}).
		Where("category_id = ?", id).Count(&services).Error; err != nil {
		return store.Translate(err)
	}
	if services > 0 {
		return fmt.Errorf("%w: %d service(s) are still filed under %q — move them first",
			ErrInvalidInput, services, c.Name)
	}

	var children int64
	if err := s.db.WithContext(ctx).Model(&domain.ServiceCategory{}).
		Where("parent_id = ?", id).Count(&children).Error; err != nil {
		return store.Translate(err)
	}
	if children > 0 {
		return fmt.Errorf("%w: %q still has %d sub-categor%s — move or remove them first",
			ErrInvalidInput, c.Name, children, plural(children))
	}

	if err := s.db.WithContext(ctx).Delete(&c).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "catalog.category_deleted", TargetType: "service_category", TargetID: id,
		Summary: c.Name,
	})
	return nil
}

// CategoryPath returns a category and its ancestors, outermost first, for
// rendering "Roads & transport → Potholes".
func (s *Service) CategoryPath(ctx context.Context, id string) ([]domain.ServiceCategory, error) {
	if id == "" {
		return nil, nil
	}
	var path []domain.ServiceCategory
	for depth := 0; id != "" && depth <= maxCategoryDepth; depth++ {
		var c domain.ServiceCategory
		if err := s.db.WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				break
			}
			return nil, store.Translate(err)
		}
		path = append(path, c)
		id = c.ParentID
	}
	// Collected leaf-first; a breadcrumb reads the other way. Reversed
	// explicitly — a sort comparator that inspects indices rather than elements
	// is not a reverse, it is undefined behaviour that happens to look like one.
	slices.Reverse(path)
	return path, nil
}

func plural(n int64) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
