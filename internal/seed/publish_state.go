package seed

import (
	"context"
	"log/slog"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// adoptPublishState converts the catalogue from the Active boolean it used to
// carry onto the three-state publish field that replaced it.
//
// AutoMigrate adds publish_state with a default of 'published', which is right
// for the overwhelming majority of an existing catalogue but wrong for the
// entries an administrator had switched off: those must come back archived,
// not live. Getting this backwards would republish every retired service in a
// municipality's catalogue on the deploy that upgraded them, which is the exact
// failure mode the state field exists to prevent.
//
// The legacy column is then neutralised rather than read again. GORM does not
// drop columns, so active lingers in the table; leaving it holding false would
// make this conversion fire a second time and re-archive a service an operator
// had since republished. Setting it true everywhere makes the pass idempotent
// without a migration table.
func adoptPublishState(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	// A fresh deployment never had the column.
	//
	// Asked through the migrator rather than by running `SELECT active` and
	// catching the failure. Both answer correctly, but the failing query is
	// logged as an ERROR by GORM before this function ever sees it — so every
	// boot of a clean install printed "Unknown column \'active\'" at error
	// level for a condition that is entirely expected, which is exactly the
	// sort of thing that teaches an operator to ignore the log.
	if !db.WithContext(ctx).Migrator().HasColumn(&domain.ServiceType{}, "active") {
		return nil
	}

	retired := db.WithContext(ctx).Model(&domain.ServiceType{}).
		Where("active = ? AND publish_state = ?", false, domain.PublishPublished).
		UpdateColumn("publish_state", domain.PublishArchived)
	if retired.Error != nil {
		return retired.Error
	}

	if err := db.WithContext(ctx).Exec(
		"UPDATE service_types SET active = ? WHERE active = ?", true, false).Error; err != nil {
		return err
	}

	if retired.RowsAffected > 0 {
		log.InfoContext(ctx, "adopted publish state from the legacy active flag",
			"archived", retired.RowsAffected)
	}
	return nil
}
