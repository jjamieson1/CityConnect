package seed

import (
	"context"
	"log/slog"
	"strings"
	"time"

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

// seedCollectionNotice installs a default collection notice if a deployment has
// none.
//
// Wording a municipality has not approved is not something to invent lightly,
// and this text is deliberately generic and replaceable — it says only what is
// true of the system's actual behaviour. But a portal that collects an email
// address with *no* notice at all is a PIPEDA problem from the first
// submission, and shipping that as the default is worse than shipping a
// starting point a City will edit. The admin surface publishes a new version;
// this one is never overwritten once anything exists.
func seedCollectionNotice(ctx context.Context, db *gorm.DB) error {
	var existing int64
	if err := db.WithContext(ctx).Model(&domain.CollectionNotice{}).
		Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	return db.WithContext(ctx).Create(&domain.CollectionNotice{
		Version: 1,
		Body: "We collect your name and contact details only to handle this report, " +
			"to confirm we have it, and to let you check on its progress. They are " +
			"kept under the City's records retention schedule and are not used for " +
			"anything else or shared outside the City except where the law requires " +
			"it. You can ask us what we hold about you, and ask for it to be " +
			"corrected. Reporting without contact details is also possible, and no " +
			"personal information is collected when you do.",
		EffectiveAt: time.Now().UTC(),
	}).Error
}
