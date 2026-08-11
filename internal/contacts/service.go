// Package contacts owns the citizen record: identities, channels, groups,
// communication preferences, and the duplicate detection and merge that keep
// the CRM from fragmenting once several systems feed it.
package contacts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var (
	ErrNotFound     = errors.New("contacts: not found")
	ErrInvalidInput = errors.New("contacts: invalid input")
	ErrConflict     = errors.New("contacts: conflict")
	ErrStale        = errors.New("contacts: record changed since it was read")
)

// Service implements contact management.
type Service struct {
	db    *gorm.DB
	audit *audit.Service
	log   *slog.Logger
}

// NewService builds the contacts service.
func NewService(db *gorm.DB, aud *audit.Service, log *slog.Logger) *Service {
	return &Service{db: db, audit: aud, log: log.With("component", "contacts")}
}

// Filter narrows a contact listing.
type Filter struct {
	Query        string
	Tag          string
	GroupID      string
	Ward         string
	PostalCode   string
	Status       string
	C2Reachable  *bool
	DoNotContact *bool
	HasC2Link    *bool
}

// List returns a page of contacts.
func (s *Service) List(ctx context.Context, f Filter, page store.Page) (store.Result[domain.Contact], error) {
	q := s.db.WithContext(ctx).Model(&domain.Contact{}).Where("merged_into_id = '' OR merged_into_id IS NULL")

	if f.Query != "" {
		like := "%" + store.LikeEscape(f.Query) + "%"
		q = q.Where(
			"display_name LIKE ? OR organization LIKE ? OR primary_email LIKE ? OR primary_phone LIKE ?",
			like, like, like, like)
	}
	if f.Tag != "" {
		q = q.Where("tags LIKE ?", "%\""+store.LikeEscape(f.Tag)+"\"%")
	}
	if f.GroupID != "" {
		q = q.Where("id IN (SELECT contact_id FROM contact_group_members WHERE contact_group_id = ?)", f.GroupID)
	}
	if f.Ward != "" {
		q = q.Where("ward = ?", f.Ward)
	}
	if f.PostalCode != "" {
		q = q.Where("postal_code = ?", f.PostalCode)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.C2Reachable != nil {
		q = q.Where("c2_reachable = ?", *f.C2Reachable)
	}
	if f.DoNotContact != nil {
		q = q.Where("do_not_contact = ?", *f.DoNotContact)
	}
	if f.HasC2Link != nil {
		sub := "SELECT contact_id FROM contact_identities WHERE provider = ? AND deleted_at IS NULL"
		if *f.HasC2Link {
			q = q.Where("id IN ("+sub+")", domain.ProviderC2)
		} else {
			q = q.Where("id NOT IN ("+sub+")", domain.ProviderC2)
		}
	}

	var rows []domain.Contact
	return store.Paginate(q, page, map[string]string{
		"name":      "display_name",
		"createdAt": "created_at",
		"updatedAt": "updated_at",
		"email":     "primary_email",
		"ward":      "ward",
	}, "display_name", &rows)
}

// Get loads one contact with its identities, channels and groups.
func (s *Service) Get(ctx context.Context, id string) (*domain.Contact, error) {
	var c domain.Contact
	err := s.db.WithContext(ctx).
		Preload("Identities").Preload("Channels").Preload("Groups").
		First(&c, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &c, store.Translate(err)
}

// FindByC2Sub resolves a C2 subject to its contact.
//
// The subject lives on ContactIdentity rather than on Contact, which is what
// lets a merge preserve every external link and lets one person carry a C2
// subject alongside a permitting-system id.
func (s *Service) FindByC2Sub(ctx context.Context, sub string) (*domain.Contact, error) {
	var ident domain.ContactIdentity
	err := s.db.WithContext(ctx).
		Where("provider = ? AND external_id = ?", domain.ProviderC2, sub).
		First(&ident).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, store.Translate(err)
	}

	c, err := s.Get(ctx, ident.ContactID)
	if err != nil {
		return nil, err
	}
	// Follow a merge so an old subject link still resolves to the survivor.
	for hops := 0; c.MergedIntoID != "" && hops < 5; hops++ {
		next, err := s.Get(ctx, c.MergedIntoID)
		if err != nil {
			break
		}
		c = next
	}
	return c, nil
}

// EnsureByC2Sub returns the contact for a subject, creating a minimal record
// when none exists. Used by inbound partner calls that identify a citizen the
// CRM has not met yet.
func (s *Service) EnsureByC2Sub(ctx context.Context, actor audit.Actor, sub, displayName, email string) (*domain.Contact, error) {
	if c, err := s.FindByC2Sub(ctx, sub); err == nil {
		return c, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "Citizen " + shortSub(sub)
	}

	c := &domain.Contact{
		DisplayName:  name,
		PrimaryEmail: strings.ToLower(strings.TrimSpace(email)),
		Status:       domain.ContactActive,
		C2Reachable:  true,
	}

	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Create(c).Error; err != nil {
			return err
		}
		ident := domain.ContactIdentity{
			ContactID: c.ID, Provider: domain.ProviderC2,
			ExternalID: sub, Verified: true,
		}
		if err := tx.Create(&ident).Error; err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, actor, audit.Entry{
			Action: "contact.created", TargetType: "contact", TargetID: c.ID,
			Summary: "created from C2 subject", Changes: domain.JSONMap{"sub": sub},
		})
	})
	if err != nil {
		return nil, store.Translate(err)
	}
	return c, nil
}

// Create adds a contact.
func (s *Service) Create(ctx context.Context, actor audit.Actor, c *domain.Contact) (*domain.Contact, error) {
	if err := normalize(c); err != nil {
		return nil, err
	}
	c.Status = domain.ContactActive
	c.Version = 1

	if err := s.db.WithContext(ctx).Create(c).Error; err != nil {
		return nil, store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "contact.created", TargetType: "contact", TargetID: c.ID, Summary: c.DisplayName,
	})
	return c, nil
}

// Update applies an edit with optimistic concurrency. A stale version is a
// conflict, not a silent overwrite: two agents on one record during a phone
// call is routine, and last-write-wins loses whichever note was typed first.
func (s *Service) Update(ctx context.Context, actor audit.Actor, id string, expectedVersion uint, in *domain.Contact) (*domain.Contact, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if expectedVersion != 0 && current.Version != expectedVersion {
		return nil, fmt.Errorf("%w: expected version %d, found %d", ErrStale, expectedVersion, current.Version)
	}
	if err := normalize(in); err != nil {
		return nil, err
	}

	updates := map[string]any{
		"display_name":       in.DisplayName,
		"given_name":         in.GivenName,
		"family_name":        in.FamilyName,
		"organization":       in.Organization,
		"primary_email":      in.PrimaryEmail,
		"primary_phone":      in.PrimaryPhone,
		"preferred_language": in.PreferredLanguage,
		"preferred_channel":  in.PreferredChannel,
		"do_not_contact":     in.DoNotContact,
		"address1":           in.Address1,
		"address2":           in.Address2,
		"city":               in.City,
		"state":              in.State,
		"postal_code":        in.PostalCode,
		"ward":               in.Ward,
		"latitude":           in.Latitude,
		"longitude":          in.Longitude,
		"notes":              in.Notes,
		"tags":               in.Tags,
		"custom_fields":      in.CustomFields,
		"version":            current.Version + 1,
	}

	res := s.db.WithContext(ctx).Model(&domain.Contact{}).
		Where("id = ? AND version = ?", id, current.Version).
		Updates(updates)
	if res.Error != nil {
		return nil, store.Translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, ErrStale
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "contact.updated", TargetType: "contact", TargetID: id,
		Summary: in.DisplayName, Changes: diff(current, in),
	})
	return s.Get(ctx, id)
}

// Delete soft-deletes a contact that holds no requests.
func (s *Service) Delete(ctx context.Context, actor audit.Actor, id string) error {
	var open int64
	s.db.WithContext(ctx).Model(&domain.Request{}).Where("contact_id = ?", id).Count(&open)
	if open > 0 {
		return fmt.Errorf("%w: contact has %d service request(s); merge or reassign them first", ErrConflict, open)
	}
	if err := s.db.WithContext(ctx).Delete(&domain.Contact{}, "id = ?", id).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "contact.deleted", TargetType: "contact", TargetID: id,
	})
	return nil
}

// SetC2Reachable records what C2 told us about a citizen's reachability.
//
// A 403 means the citizen holds no active consent for our application and a
// 404 means C2 does not know the subject; neither is retryable. Surfacing it
// on the contact is what lets an agent see that this person must be phoned or
// posted rather than notified.
func (s *Service) SetC2Reachable(ctx context.Context, contactID string, reachable bool, code string) error {
	now := time.Now().UTC()
	return store.Translate(s.db.WithContext(ctx).Model(&domain.Contact{}).
		Where("id = ?", contactID).
		Updates(map[string]any{
			"c2_reachable":        reachable,
			"c2_unreachable_code": code,
			"c2_checked_at":       now,
		}).Error)
}

// ---------------------------------------------------------------------------
// Identities and channels
// ---------------------------------------------------------------------------

// AddIdentity links an external system's record to a contact.
func (s *Service) AddIdentity(ctx context.Context, actor audit.Actor, contactID string, ident *domain.ContactIdentity) (*domain.ContactIdentity, error) {
	if ident.Provider == "" || ident.ExternalID == "" {
		return nil, fmt.Errorf("%w: provider and externalId are required", ErrInvalidInput)
	}
	ident.ContactID = contactID

	if err := s.db.WithContext(ctx).Create(ident).Error; err != nil {
		return nil, store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "contact.identity_added", TargetType: "contact", TargetID: contactID,
		Summary: ident.Provider + ":" + ident.ExternalID,
	})
	return ident, nil
}

// RemoveIdentity unlinks an external identity.
func (s *Service) RemoveIdentity(ctx context.Context, actor audit.Actor, contactID, identityID string) error {
	res := s.db.WithContext(ctx).
		Where("id = ? AND contact_id = ?", identityID, contactID).
		Delete(&domain.ContactIdentity{})
	if res.Error != nil {
		return store.Translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "contact.identity_removed", TargetType: "contact", TargetID: contactID,
	})
	return nil
}

// SaveChannel adds or updates an addressable endpoint, keeping the primary
// flag exclusive per kind.
func (s *Service) SaveChannel(ctx context.Context, actor audit.Actor, contactID string, ch *domain.ContactChannel) (*domain.ContactChannel, error) {
	ch.Value = strings.TrimSpace(ch.Value)
	if ch.Value == "" || ch.Kind == "" {
		return nil, fmt.Errorf("%w: channel kind and value are required", ErrInvalidInput)
	}
	if ch.Kind == domain.ChannelEmail {
		ch.Value = strings.ToLower(ch.Value)
	}
	ch.ContactID = contactID

	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if ch.IsPrimary {
			if err := tx.Model(&domain.ContactChannel{}).
				Where("contact_id = ? AND kind = ?", contactID, ch.Kind).
				UpdateColumn("is_primary", false).Error; err != nil {
				return err
			}
		}
		if err := tx.Save(ch).Error; err != nil {
			return err
		}
		if !ch.IsPrimary {
			return nil
		}
		// Keep the denormalised primary on the contact in step, since lists
		// and the callout read it without joining.
		column := map[domain.ChannelKind]string{
			domain.ChannelEmail: "primary_email",
			domain.ChannelPhone: "primary_phone",
		}[ch.Kind]
		if column == "" {
			return nil
		}
		return tx.Model(&domain.Contact{}).Where("id = ?", contactID).
			UpdateColumn(column, ch.Value).Error
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "contact.channel_saved", TargetType: "contact", TargetID: contactID,
		Summary: string(ch.Kind) + " " + ch.Value,
	})
	return ch, nil
}

// DeleteChannel removes an endpoint.
func (s *Service) DeleteChannel(ctx context.Context, actor audit.Actor, contactID, channelID string) error {
	res := s.db.WithContext(ctx).
		Where("id = ? AND contact_id = ?", channelID, contactID).
		Delete(&domain.ContactChannel{})
	if res.Error != nil {
		return store.Translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "contact.channel_deleted", TargetType: "contact", TargetID: contactID,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Consent preferences
// ---------------------------------------------------------------------------

// SetConsent records a communication preference.
//
// This is CityConnect's own preference ledger, separate from C2 consent, which
// C2 owns and enforces on its side. Both gates apply to an outbound message.
func (s *Service) SetConsent(ctx context.Context, actor audit.Actor, contactID string, pref *domain.ConsentPreference) error {
	pref.ContactID = contactID
	pref.SetByID = actor.ID
	pref.SetAt = time.Now().UTC()

	var existing domain.ConsentPreference
	err := s.db.WithContext(ctx).
		Where("contact_id = ? AND purpose = ? AND channel = ?", contactID, pref.Purpose, pref.Channel).
		First(&existing).Error
	if err == nil {
		pref.ID = existing.ID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return store.Translate(err)
	}

	if err := s.db.WithContext(ctx).Save(pref).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "contact.consent_set", TargetType: "contact", TargetID: contactID,
		Changes: domain.JSONMap{
			"purpose": pref.Purpose, "channel": pref.Channel, "granted": pref.Granted,
		},
	})
	return nil
}

// Consents returns a contact's preference ledger.
func (s *Service) Consents(ctx context.Context, contactID string) ([]domain.ConsentPreference, error) {
	var out []domain.ConsentPreference
	err := s.db.WithContext(ctx).Where("contact_id = ?", contactID).Find(&out).Error
	return out, store.Translate(err)
}

// MayContact reports whether an outbound message is allowed on our side.
// A do-not-contact flag is a hard stop checked before anything else.
func (s *Service) MayContact(ctx context.Context, contactID string, purpose domain.ConsentPurpose, channel domain.ChannelKind) (bool, string, error) {
	var c domain.Contact
	if err := s.db.WithContext(ctx).First(&c, "id = ?", contactID).Error; err != nil {
		return false, "", ErrNotFound
	}
	if c.DoNotContact {
		return false, domain.SuppressDoNotContct, nil
	}

	var pref domain.ConsentPreference
	err := s.db.WithContext(ctx).
		Where("contact_id = ? AND purpose = ? AND channel = ?", contactID, purpose, channel).
		First(&pref).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// No explicit preference means service updates are permitted —
		// a citizen who filed a request expects to hear about it — while
		// anything promotional stays opt-in.
		if purpose == domain.ConsentServiceUpdates {
			return true, "", nil
		}
		return false, domain.SuppressOptedOut, nil
	}
	if err != nil {
		return false, "", store.Translate(err)
	}
	if !pref.Granted {
		return false, domain.SuppressOptedOut, nil
	}
	return true, "", nil
}

func normalize(c *domain.Contact) error {
	c.DisplayName = strings.TrimSpace(c.DisplayName)
	c.PrimaryEmail = strings.ToLower(strings.TrimSpace(c.PrimaryEmail))
	c.PrimaryPhone = strings.TrimSpace(c.PrimaryPhone)
	c.Tags = c.Tags.Normalized()

	if c.DisplayName == "" {
		joined := strings.TrimSpace(c.GivenName + " " + c.FamilyName)
		if joined == "" {
			joined = c.Organization
		}
		c.DisplayName = strings.TrimSpace(joined)
	}
	if c.DisplayName == "" {
		return fmt.Errorf("%w: a contact needs a name, an organization, or both", ErrInvalidInput)
	}
	if c.PreferredLanguage == "" {
		c.PreferredLanguage = "en"
	}
	return nil
}

func diff(before, after *domain.Contact) domain.JSONMap {
	out := domain.JSONMap{}
	if before.DisplayName != after.DisplayName {
		out["displayName"] = []string{before.DisplayName, after.DisplayName}
	}
	if before.PrimaryEmail != after.PrimaryEmail {
		out["primaryEmail"] = []string{before.PrimaryEmail, after.PrimaryEmail}
	}
	if before.PrimaryPhone != after.PrimaryPhone {
		out["primaryPhone"] = []string{before.PrimaryPhone, after.PrimaryPhone}
	}
	if before.DoNotContact != after.DoNotContact {
		out["doNotContact"] = []bool{before.DoNotContact, after.DoNotContact}
	}
	return out
}

func shortSub(sub string) string {
	if len(sub) <= 8 {
		return sub
	}
	return sub[:8]
}
