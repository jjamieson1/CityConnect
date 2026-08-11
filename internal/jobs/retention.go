package jobs

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// RetentionResult summarises a retention pass.
type RetentionResult struct {
	Entities  map[string]int `json:"entities"`
	Anonymize int            `json:"anonymized"`
	Purged    int            `json:"purged"`
}

// RunRetention applies the configured records schedules.
//
// Municipal records retention is a statutory obligation, not a tidiness
// exercise, and it is far cheaper to build in from the start than to retrofit
// once ten years of personal data has accumulated. Policies are disabled by
// default: destroying records is not something to start doing by accident, so
// an administrator must switch each one on deliberately.
func (r *Runner) RunRetention(ctx context.Context) (*RetentionResult, error) {
	var policies []domain.RetentionPolicy
	err := r.db.WithContext(ctx).Where("enabled = ?", true).Find(&policies).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	res := &RetentionResult{Entities: map[string]int{}}
	if len(policies) == 0 {
		return res, nil
	}

	now := time.Now().UTC()
	for i := range policies {
		p := &policies[i]
		if p.RetainMonths <= 0 {
			continue
		}
		cutoff := now.AddDate(0, -p.RetainMonths, 0)

		var affected int
		var err error

		switch p.Entity {
		case "request":
			affected, err = r.retainRequests(ctx, p, cutoff)
		case "contact":
			affected, err = r.retainContacts(ctx, p, cutoff)
		case "interaction":
			affected, err = r.retainInteractions(ctx, cutoff)
		case "notification":
			affected, err = r.purge(ctx, &domain.NotificationOutbox{}, cutoff)
		case "webhook_delivery":
			affected, err = r.purge(ctx, &domain.WebhookDelivery{}, cutoff)
		case "callout_log":
			affected, err = r.purge(ctx, &domain.CalloutLog{}, cutoff)
		default:
			continue
		}

		if err != nil {
			r.log.ErrorContext(ctx, "retention policy failed",
				"entity", p.Entity, "error", err)
			continue
		}

		res.Entities[p.Entity] = affected
		if p.Action == "purge" {
			res.Purged += affected
		} else {
			res.Anonymize += affected
		}

		r.db.WithContext(ctx).Model(p).Updates(map[string]any{
			"last_run_at":   now,
			"last_affected": affected,
		})
	}

	if res.Anonymize+res.Purged > 0 {
		r.log.InfoContext(ctx, "retention pass complete",
			"anonymized", res.Anonymize, "purged", res.Purged)
	}
	return res, nil
}

// retainRequests applies a schedule to closed requests. Only closed work is
// eligible — an open request is a live record regardless of its age.
func (r *Runner) retainRequests(ctx context.Context, p *domain.RetentionPolicy, cutoff time.Time) (int, error) {
	var batch []domain.Request
	err := r.db.WithContext(ctx).
		Where("closed_at IS NOT NULL AND closed_at < ?", cutoff).
		Limit(500).Find(&batch).Error
	if err != nil {
		return 0, store.Translate(err)
	}

	count := 0
	for i := range batch {
		req := &batch[i]

		if p.Action == "purge" {
			err = store.Tx(ctx, r.db, func(tx *gorm.DB) error {
				for _, model := range []any{
					&domain.RequestComment{}, &domain.RequestEvent{},
					&domain.RequestLink{}, &domain.Attachment{},
				} {
					if err := tx.Unscoped().Where("request_id = ?", req.ID).Delete(model).Error; err != nil {
						return err
					}
				}
				return tx.Unscoped().Delete(&domain.Request{}, "id = ?", req.ID).Error
			})
		} else {
			// Anonymise: keep the operational record — counts, cycle times,
			// service mix all stay reportable — and remove what identifies a
			// person or a private address.
			err = r.db.WithContext(ctx).Model(&domain.Request{}).Where("id = ?", req.ID).
				Updates(map[string]any{
					"description": "[redacted under records retention]",
					"address1":    "", "address2": "", "parcel_id": "",
					"latitude": 0, "longitude": 0,
					"form_data":       domain.JSONMap{},
					"resolution_note": "",
					"csat_comment":    "",
				}).Error
			if err == nil {
				err = r.db.WithContext(ctx).Model(&domain.RequestComment{}).
					Where("request_id = ?", req.ID).
					UpdateColumn("body", "[redacted under records retention]").Error
			}
		}

		if err != nil {
			return count, store.Translate(err)
		}
		count++
	}

	if count > 0 {
		r.auditRetention(ctx, "request", p.Action, count)
	}
	return count, nil
}

// retainContacts anonymises contacts with no recent activity. Contacts are
// never purged outright while any request references them: a request whose
// reporter has vanished cannot be explained afterwards.
func (r *Runner) retainContacts(ctx context.Context, p *domain.RetentionPolicy, cutoff time.Time) (int, error) {
	var batch []domain.Contact
	err := r.db.WithContext(ctx).
		Where("updated_at < ? AND status <> ?", cutoff, domain.ContactMerged).
		Where("id NOT IN (SELECT contact_id FROM requests WHERE closed_at IS NULL OR closed_at > ?)", cutoff).
		Limit(500).Find(&batch).Error
	if err != nil {
		return 0, store.Translate(err)
	}

	count := 0
	for i := range batch {
		c := &batch[i]
		err := store.Tx(ctx, r.db, func(tx *gorm.DB) error {
			if err := tx.Model(&domain.Contact{}).Where("id = ?", c.ID).Updates(map[string]any{
				"display_name":  fmt.Sprintf("Redacted contact %s", c.ID[:8]),
				"given_name":    "", "family_name": "", "organization": "",
				"primary_email": "", "primary_phone": "",
				"address1": "", "address2": "", "postal_code": "",
				"latitude": 0, "longitude": 0,
				"notes":         "", "custom_fields": domain.JSONMap{},
				"status":        domain.ContactInactive,
			}).Error; err != nil {
				return err
			}
			if err := tx.Where("contact_id = ?", c.ID).Delete(&domain.ContactChannel{}).Error; err != nil {
				return err
			}
			// Identity links go too: keeping a C2 subject against a redacted
			// record would let the identity be reconstructed.
			return tx.Where("contact_id = ?", c.ID).Delete(&domain.ContactIdentity{}).Error
		})
		if err != nil {
			return count, store.Translate(err)
		}
		count++
	}

	if count > 0 {
		r.auditRetention(ctx, "contact", "anonymize", count)
	}
	return count, nil
}

func (r *Runner) retainInteractions(ctx context.Context, cutoff time.Time) (int, error) {
	res := r.db.WithContext(ctx).Unscoped().
		Where("occurred_at < ?", cutoff).
		Limit(1000).Delete(&domain.Interaction{})
	if res.Error != nil {
		return 0, store.Translate(res.Error)
	}
	if res.RowsAffected > 0 {
		r.auditRetention(ctx, "interaction", "purge", int(res.RowsAffected))
	}
	return int(res.RowsAffected), nil
}

func (r *Runner) purge(ctx context.Context, model any, cutoff time.Time) (int, error) {
	res := r.db.WithContext(ctx).Unscoped().
		Where("created_at < ?", cutoff).Limit(2000).Delete(model)
	if res.Error != nil {
		return 0, store.Translate(res.Error)
	}
	return int(res.RowsAffected), nil
}

// auditRetention records the destruction itself. The audit chain is the only
// place that will still show a redaction happened once the data is gone.
func (r *Runner) auditRetention(ctx context.Context, entity, action string, count int) {
	r.requests.Audit().Record(ctx, audit.JobActor("retention"), audit.Entry{
		Action:     "retention." + action,
		TargetType: entity,
		Summary:    fmt.Sprintf("%s %d %s record(s) under the records schedule", action, count, entity),
		Changes:    domain.JSONMap{"count": count},
	})
}
