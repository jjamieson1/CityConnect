package contacts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// GuestDetails is what somebody tells us about themselves when they report
// without an account.
type GuestDetails struct {
	Name  string
	Email string
	Phone string
}

// EnsureGuest returns the contact for a guest reporter, creating one when this
// is the first time they have written in.
//
// Matching is on email, and deliberately only against contacts that hold no C2
// identity. That restriction is the whole security decision here.
//
// A guest has typed an address; they have not proved they own it. Matching an
// unauthenticated submission onto an account-holder's contact record because
// the addresses agree would let anyone file a report into a named resident's
// history, and send that resident notifications about something they never
// reported, from a form with no sign-in. So an address that belongs to somebody
// with an account gets a separate guest record instead.
//
// The cost is a duplicate contact for one human, which is a known and solved
// problem: contacts already merge, reversibly, and a staff member can join the
// two deliberately once they can see both. That is the right way round —
// merging on human judgement rather than splitting after an automated mistake.
func (s *Service) EnsureGuest(ctx context.Context, actor audit.Actor, in GuestDetails) (*domain.Contact, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" {
		return nil, fmt.Errorf("%w: an email address is required to report as a guest", ErrInvalidInput)
	}

	if existing, err := s.findGuestByEmail(ctx, email); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		// The local part is a poor name but a better one than "Citizen", and it
		// is what an agent will recognise when they call back.
		name, _, _ = strings.Cut(email, "@")
	}

	c := &domain.Contact{
		DisplayName:  name,
		PrimaryEmail: email,
		PrimaryPhone: strings.TrimSpace(in.Phone),
		Status:       domain.ContactActive,
		// No C2 identity, so nothing to be reachable through there. Leaving
		// this true would tell an agent the citizen can be messaged in-app when
		// they have no account at all.
		C2Reachable: false,
	}
	if err := normalize(c); err != nil {
		return nil, err
	}

	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Create(c).Error; err != nil {
			return err
		}
		// Deliberately no ContactIdentity row. An identity is a link to an
		// account that vouched for this person; a typed address is not that,
		// and recording one would make a guest indistinguishable from a
		// citizen who actually signed in.
		return s.audit.RecordTx(ctx, tx, actor, audit.Entry{
			Action: "contact.created", TargetType: "contact", TargetID: c.ID,
			Summary: "created from a guest report",
		})
	})
	if err != nil {
		return nil, store.Translate(err)
	}
	return c, nil
}

// findGuestByEmail looks for an existing guest contact with this address.
//
// The NOT EXISTS is the point: a contact linked to a C2 account is not a guest
// record and must not be matched onto from an unauthenticated form.
func (s *Service) findGuestByEmail(ctx context.Context, email string) (*domain.Contact, error) {
	var c domain.Contact
	err := s.db.WithContext(ctx).
		Where("LOWER(primary_email) = ?", email).
		Where("NOT EXISTS (?)", s.db.Model(&domain.ContactIdentity{}).
			Select("1").
			Where("contact_identities.contact_id = contacts.id").
			Where("contact_identities.provider = ?", domain.ProviderC2)).
		Order("created_at ASC").
		First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, store.Translate(err)
	}

	// Follow a merge, so a guest who has since been merged into a survivor
	// reports against the surviving record rather than reviving a dead one.
	for hops := 0; c.MergedIntoID != "" && hops < 5; hops++ {
		next, err := s.Get(ctx, c.MergedIntoID)
		if err != nil {
			break
		}
		c = *next
	}
	return &c, nil
}
