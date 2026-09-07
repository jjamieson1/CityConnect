package seed

import (
	"context"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// adoptFlatCategories moves the catalogue from a category *string* onto the
// category tree, without losing anything.
//
// Before the tree existed, ServiceType.Category held a free-text name. Those
// names are the categories a municipality already chose and already sees in the
// portal — throwing them away and starting from a fixed list would silently
// recategorise somebody's live catalogue on a deploy. So each distinct existing
// name becomes a top-level category and the services filed under it are
// pointed at the new row.
//
// Idempotent, and runs on every boot. A service that already has a CategoryID
// is left alone, so this converges once and then does nothing.
func adoptFlatCategories(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	var orphans []domain.ServiceType
	if err := db.WithContext(ctx).
		Where("(category_id IS NULL OR category_id = '') AND category <> ''").
		Find(&orphans).Error; err != nil {
		return err
	}
	if len(orphans) == 0 {
		return nil
	}

	// Existing categories first, so a second run — or a name a municipality has
	// since created deliberately — is reused rather than duplicated.
	var existing []domain.ServiceCategory
	if err := db.WithContext(ctx).Find(&existing).Error; err != nil {
		return err
	}
	byName := make(map[string]string, len(existing))
	for _, c := range existing {
		byName[strings.ToLower(c.Name)] = c.ID
	}

	order := len(existing)
	adopted := 0
	for i := range orphans {
		st := &orphans[i]
		name := strings.TrimSpace(st.Category)
		key := strings.ToLower(name)

		id, ok := byName[key]
		if !ok {
			cat := domain.ServiceCategory{
				Name: name, DisplayOrder: order, Active: true,
			}
			if err := db.WithContext(ctx).Create(&cat).Error; err != nil {
				return err
			}
			id, byName[key] = cat.ID, cat.ID
			order++
		}

		if err := db.WithContext(ctx).Model(&domain.ServiceType{}).
			Where("id = ?", st.ID).
			UpdateColumn("category_id", id).Error; err != nil {
			return err
		}
		adopted++
	}

	log.Info("adopted flat service categories into the category tree",
		"services", adopted, "categories", len(byName))
	return nil
}

// nestSeededCategories arranges the baseline catalogue into two levels.
//
// Adoption produces a flat list, which is correct for a municipality's own
// existing categories — we do not get to decide how somebody else's catalogue
// is shaped. The seeded sample is ours, so it demonstrates the hierarchy the
// requirement asks for rather than leaving a flat list that makes the feature
// look unimplemented.
//
// Only ever moves a category that is still top-level and still in its seeded
// position, so a City that has since rearranged the tree is left alone.
func nestSeededCategories(ctx context.Context, db *gorm.DB) error {
	// child -> parent, both by the names the baseline seed creates.
	nesting := map[string]string{
		"Roads": "Streets & transport",
		"Water": "Streets & transport",
		"Waste": "Bins & recycling",
		"Parks": "Parks & public space",
		"Bylaw": "Community & bylaw",
	}

	for childName, parentName := range nesting {
		var child domain.ServiceCategory
		err := db.WithContext(ctx).
			Where("name = ? AND (parent_id IS NULL OR parent_id = '')", childName).
			First(&child).Error
		if err != nil {
			// Absent, or already nested by somebody. Either way, not ours to move.
			continue
		}

		var parent domain.ServiceCategory
		err = db.WithContext(ctx).Where("name = ?", parentName).First(&parent).Error
		if err != nil {
			parent = domain.ServiceCategory{Name: parentName, Active: true}
			if err := db.WithContext(ctx).Create(&parent).Error; err != nil {
				return err
			}
		}

		if err := db.WithContext(ctx).Model(&domain.ServiceCategory{}).
			Where("id = ?", child.ID).
			UpdateColumn("parent_id", parent.ID).Error; err != nil {
			return err
		}
	}
	return nil
}
