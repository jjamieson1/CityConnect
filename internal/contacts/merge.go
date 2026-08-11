package contacts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// DuplicateCandidate is a possible duplicate with the evidence for it.
type DuplicateCandidate struct {
	Contact domain.Contact `json:"contact"`
	Score   int            `json:"score"`
	Reasons []string       `json:"reasons"`
}

// FindDuplicates scores likely duplicates of a contact.
//
// Duplicates are inevitable: the same person turns up as a C2 subject, a
// phone call taken by an agent, and a permitting-system record, and nothing
// upstream reconciles them. Scoring is deliberately conservative — this
// surfaces candidates for a human to confirm, never merges on its own.
func (s *Service) FindDuplicates(ctx context.Context, id string, limit int) ([]DuplicateCandidate, error) {
	base, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	q := s.db.WithContext(ctx).Model(&domain.Contact{}).
		Where("id <> ?", id).
		Where("merged_into_id = '' OR merged_into_id IS NULL")

	conds := s.db.Session(&gorm.Session{NewDB: true})
	var any bool

	if base.PrimaryEmail != "" {
		conds = conds.Or("primary_email = ?", base.PrimaryEmail)
		any = true
	}
	if digits := digitsOnly(base.PrimaryPhone); len(digits) >= 7 {
		conds = conds.Or("primary_phone LIKE ?", "%"+digits[len(digits)-7:])
		any = true
	}
	if base.DisplayName != "" {
		conds = conds.Or("display_name = ?", base.DisplayName)
		any = true
	}
	if !any {
		return nil, nil
	}

	var rows []domain.Contact
	if err := q.Where(conds).Limit(limit * 3).Find(&rows).Error; err != nil {
		return nil, store.Translate(err)
	}

	out := make([]DuplicateCandidate, 0, len(rows))
	for i := range rows {
		c := rows[i]
		cand := DuplicateCandidate{Contact: c}

		if base.PrimaryEmail != "" && c.PrimaryEmail == base.PrimaryEmail {
			cand.Score += 50
			cand.Reasons = append(cand.Reasons, "same email address")
		}
		if a, b := digitsOnly(base.PrimaryPhone), digitsOnly(c.PrimaryPhone); len(a) >= 7 && len(b) >= 7 &&
			a[len(a)-7:] == b[len(b)-7:] {
			cand.Score += 35
			cand.Reasons = append(cand.Reasons, "same phone number")
		}
		if base.DisplayName != "" && strings.EqualFold(c.DisplayName, base.DisplayName) {
			cand.Score += 25
			cand.Reasons = append(cand.Reasons, "same name")
		}
		if base.PostalCode != "" && c.PostalCode == base.PostalCode {
			cand.Score += 10
			cand.Reasons = append(cand.Reasons, "same postal code")
		}
		if base.Address1 != "" && strings.EqualFold(c.Address1, base.Address1) {
			cand.Score += 15
			cand.Reasons = append(cand.Reasons, "same street address")
		}

		if cand.Score >= 35 {
			out = append(out, cand)
		}
	}

	// Strongest evidence first, then trim.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Score > out[j-1].Score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MergeInput describes a merge.
type MergeInput struct {
	SurvivorID string
	MergedID   string
	// FieldChoices overrides which record wins per field: "survivor" (default)
	// or "merged".
	FieldChoices map[string]string
	Note         string
}

// Merge folds one contact into another, moving every dependent record and
// keeping enough detail to undo it.
//
// A merge is the single most destructive routine action in a CRM. Making it
// reversible is what stops an over-eager cleanup from permanently losing a
// citizen's history.
func (s *Service) Merge(ctx context.Context, actor audit.Actor, in MergeInput) (*domain.Contact, error) {
	if in.SurvivorID == in.MergedID || in.SurvivorID == "" || in.MergedID == "" {
		return nil, fmt.Errorf("%w: merge needs two distinct contacts", ErrInvalidInput)
	}

	survivor, err := s.Get(ctx, in.SurvivorID)
	if err != nil {
		return nil, err
	}
	merged, err := s.Get(ctx, in.MergedID)
	if err != nil {
		return nil, err
	}
	if merged.MergedIntoID != "" {
		return nil, fmt.Errorf("%w: that contact has already been merged", ErrConflict)
	}

	snapshot, _ := json.Marshal(merged)
	var snapMap domain.JSONMap
	_ = json.Unmarshal(snapshot, &snapMap)

	moved := domain.JSONMap{}
	record := domain.MergeRecord{
		SurvivorID: survivor.ID,
		MergedID:   merged.ID,
		PerformedB: actor.ID,
		Snapshot:   snapMap,
		Note:       in.Note,
	}

	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		// Move dependent records. Requests move so the survivor carries the
		// full service history; interactions so the timeline is complete.
		for _, m := range []struct {
			name  string
			model any
		}{
			{"requests", &domain.Request{}},
			{"interactions", &domain.Interaction{}},
			{"contact_identities", &domain.ContactIdentity{}},
			{"contact_channels", &domain.ContactChannel{}},
			{"consent_preferences", &domain.ConsentPreference{}},
			{"notification_outboxes", &domain.NotificationOutbox{}},
		} {
			res := tx.Model(m.model).Where("contact_id = ?", merged.ID).
				UpdateColumn("contact_id", survivor.ID)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				moved[m.name] = res.RowsAffected
			}
		}

		// Group memberships use a composite key, so a blind update would
		// collide where both contacts are already in the same group.
		var memberships []domain.ContactGroupMember
		if err := tx.Where("contact_id = ?", merged.ID).Find(&memberships).Error; err != nil {
			return err
		}
		for _, m := range memberships {
			var n int64
			tx.Model(&domain.ContactGroupMember{}).
				Where("contact_id = ? AND contact_group_id = ?", survivor.ID, m.GroupID).Count(&n)
			if n == 0 {
				if err := tx.Model(&domain.ContactGroupMember{}).
					Where("contact_id = ? AND contact_group_id = ?", merged.ID, m.GroupID).
					UpdateColumn("contact_id", survivor.ID).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Where("contact_id = ? AND contact_group_id = ?", merged.ID, m.GroupID).
					Delete(&domain.ContactGroupMember{}).Error; err != nil {
					return err
				}
			}
		}

		// Fill gaps on the survivor from the merged record, and honour any
		// explicit per-field choice the agent made.
		fields := fillGaps(survivor, merged, in.FieldChoices)
		fields["version"] = survivor.Version + 1
		if err := tx.Model(&domain.Contact{}).Where("id = ?", survivor.ID).Updates(fields).Error; err != nil {
			return err
		}
		record.FieldsKept = toJSONMap(fields)

		// The loser is retained, not deleted, so its id keeps resolving.
		if err := tx.Model(&domain.Contact{}).Where("id = ?", merged.ID).Updates(map[string]any{
			"status":         domain.ContactMerged,
			"merged_into_id": survivor.ID,
		}).Error; err != nil {
			return err
		}

		record.Moved = moved
		if err := tx.Create(&record).Error; err != nil {
			return err
		}

		return s.audit.RecordTx(ctx, tx, actor, audit.Entry{
			Action: "contact.merged", TargetType: "contact", TargetID: survivor.ID,
			Summary: fmt.Sprintf("merged %s into %s", merged.DisplayName, survivor.DisplayName),
			Changes: domain.JSONMap{"mergedId": merged.ID, "moved": moved},
		})
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	return s.Get(ctx, survivor.ID)
}

// Unmerge reverses a merge, restoring the merged contact and moving back the
// records that came with it.
//
// It cannot restore records created *after* the merge — those legitimately
// belong to the survivor — so it moves back only what the merge itself moved.
func (s *Service) Unmerge(ctx context.Context, actor audit.Actor, mergeID string) (*domain.Contact, error) {
	var record domain.MergeRecord
	if err := s.db.WithContext(ctx).First(&record, "id = ?", mergeID).Error; err != nil {
		return nil, ErrNotFound
	}
	if record.UndoneAt != nil {
		return nil, fmt.Errorf("%w: that merge has already been undone", ErrConflict)
	}

	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		// Restore the merged contact from its snapshot.
		var snapshot domain.Contact
		raw, err := json.Marshal(record.Snapshot)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}

		if err := tx.Model(&domain.Contact{}).Where("id = ?", record.MergedID).Updates(map[string]any{
			"status":         domain.ContactActive,
			"merged_into_id": "",
			"display_name":   snapshot.DisplayName,
			"primary_email":  snapshot.PrimaryEmail,
			"primary_phone":  snapshot.PrimaryPhone,
		}).Error; err != nil {
			return err
		}

		// Identities are unambiguous: they carry the provider id that was
		// originally the merged contact's, so they move back cleanly.
		var identities []domain.ContactIdentity
		if raw, ok := record.Snapshot["identities"]; ok {
			b, _ := json.Marshal(raw)
			_ = json.Unmarshal(b, &identities)
		}
		for _, ident := range identities {
			if err := tx.Model(&domain.ContactIdentity{}).
				Where("provider = ? AND external_id = ?", ident.Provider, ident.ExternalID).
				UpdateColumn("contact_id", record.MergedID).Error; err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		if err := tx.Model(&record).UpdateColumn("undone_at", now).Error; err != nil {
			return err
		}

		return s.audit.RecordTx(ctx, tx, actor, audit.Entry{
			Action: "contact.unmerged", TargetType: "contact", TargetID: record.SurvivorID,
			Summary: "reversed a contact merge",
			Changes: domain.JSONMap{"mergeId": mergeID, "restoredId": record.MergedID},
		})
	})
	if err != nil {
		return nil, store.Translate(err)
	}
	return s.Get(ctx, record.MergedID)
}

// MergeHistory lists merges involving a contact.
func (s *Service) MergeHistory(ctx context.Context, contactID string) ([]domain.MergeRecord, error) {
	var out []domain.MergeRecord
	err := s.db.WithContext(ctx).
		Where("survivor_id = ? OR merged_id = ?", contactID, contactID).
		Order("created_at DESC").Find(&out).Error
	return out, store.Translate(err)
}

// fillGaps returns the survivor fields to update: empty survivor fields are
// filled from the merged record, and explicit choices always win.
func fillGaps(survivor, merged *domain.Contact, choices map[string]string) map[string]any {
	out := map[string]any{}

	take := func(field, survivorVal, mergedVal string, column string) {
		if choices[field] == "merged" && mergedVal != "" {
			out[column] = mergedVal
			return
		}
		if survivorVal == "" && mergedVal != "" {
			out[column] = mergedVal
		}
	}

	take("displayName", survivor.DisplayName, merged.DisplayName, "display_name")
	take("givenName", survivor.GivenName, merged.GivenName, "given_name")
	take("familyName", survivor.FamilyName, merged.FamilyName, "family_name")
	take("organization", survivor.Organization, merged.Organization, "organization")
	take("primaryEmail", survivor.PrimaryEmail, merged.PrimaryEmail, "primary_email")
	take("primaryPhone", survivor.PrimaryPhone, merged.PrimaryPhone, "primary_phone")
	take("address1", survivor.Address1, merged.Address1, "address1")
	take("address2", survivor.Address2, merged.Address2, "address2")
	take("city", survivor.City, merged.City, "city")
	take("state", survivor.State, merged.State, "state")
	take("postalCode", survivor.PostalCode, merged.PostalCode, "postal_code")
	take("ward", survivor.Ward, merged.Ward, "ward")

	// A do-not-contact flag on either record survives the merge. Losing it
	// would mean contacting someone who explicitly asked not to be.
	if merged.DoNotContact {
		out["do_not_contact"] = true
	}

	// Notes concatenate rather than overwrite — they are the part an agent
	// most expects to still be there afterwards.
	if merged.Notes != "" && merged.Notes != survivor.Notes {
		combined := strings.TrimSpace(survivor.Notes + "\n\n--- merged from " + merged.DisplayName + " ---\n" + merged.Notes)
		out["notes"] = combined
	}

	// Tags union.
	if len(merged.Tags) > 0 {
		seen := map[string]bool{}
		union := make(domain.StringList, 0, len(survivor.Tags)+len(merged.Tags))
		for _, t := range append(append(domain.StringList{}, survivor.Tags...), merged.Tags...) {
			if !seen[t] {
				seen[t] = true
				union = append(union, t)
			}
		}
		out["tags"] = union
	}

	return out
}

func toJSONMap(m map[string]any) domain.JSONMap {
	out := domain.JSONMap{}
	for k, v := range m {
		switch v.(type) {
		case string, bool, int, int64, uint, float64, domain.StringList:
			out[k] = v
		}
	}
	return out
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
