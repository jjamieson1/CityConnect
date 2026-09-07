package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// guestReport files with contact details and no account.
func guestReport(t *testing.T, e *env, subject, name, email string) map[string]any {
	t.Helper()

	var created map[string]any
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       subject,
		"address1":      "44 Elm Street",
		"formData":      map[string]any{"size": "Medium"},
		"formToken":     formToken(t, e),
		"contactName":   name,
		"contactEmail":  email,
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("guest report -> %d, want 201", code)
	}
	return created
}

// The middle path: give an email, get the full deal.
func TestGuestReportIsAttributedAndTrackable(t *testing.T) {
	e := newEnv(t)

	created := guestReport(t, e, "Pothole on Elm", "Alex Guest", "alex.guest@example.com")
	if trackable, _ := created["trackable"].(bool); !trackable {
		t.Error("a guest report is not trackable; giving an email bought nothing")
	}

	reference, _ := created["reference"].(string)
	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if req.Channel != domain.ChannelGuest {
		t.Errorf("channel = %q, want %q", req.Channel, domain.ChannelGuest)
	}
	if req.ContactID == "" {
		t.Fatal("a guest report has no contact; there is nobody to write back to")
	}

	var contact domain.Contact
	if err := e.db.First(&contact, "id = ?", req.ContactID).Error; err != nil {
		t.Fatalf("contact: %v", err)
	}
	if contact.PrimaryEmail != "alex.guest@example.com" {
		t.Errorf("email = %q, want it recorded", contact.PrimaryEmail)
	}

	// No identity row: a typed address is not an account that vouched for
	// anyone, and recording one would make a guest indistinguishable from a
	// citizen who actually signed in.
	var identities int64
	if err := e.db.Model(&domain.ContactIdentity{}).
		Where("contact_id = ?", req.ContactID).Count(&identities).Error; err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identities != 0 {
		t.Errorf("%d identity row(s) created for a guest", identities)
	}
}

// The whole point of the guest path over the anonymous one.
func TestGuestCanTrackTheirOwnReport(t *testing.T) {
	e := newEnv(t)

	created := guestReport(t, e, "Streetlight out", "Sam Guest", "sam.guest@example.com")
	reference, _ := created["reference"].(string)

	var view map[string]any
	code := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   reference,
		"verificationValue": "sam.guest@example.com",
	}, &view)
	if code != http.StatusOK {
		t.Fatalf("guest tracking their own report -> %d, want 200", code)
	}
	if view["reference"] != reference {
		t.Errorf("reference = %v, want %q", view["reference"], reference)
	}

	// And somebody else's guess still fails.
	if code := doJSON(t, newJarClient(), http.MethodPost, trackURL(e.api.URL), map[string]any{
		"referenceNumber":   reference,
		"verificationValue": "someone.else@example.com",
	}, nil); code != http.StatusNotFound {
		t.Errorf("tracking with the wrong email -> %d, want 404", code)
	}
}

// A repeat guest must not fan out into a contact row per report.
func TestRepeatGuestResolvesToOneContact(t *testing.T) {
	e := newEnv(t)

	first := guestReport(t, e, "First report", "Robin Guest", "robin@example.com")
	second := guestReport(t, e, "Second report", "Robin Guest", "ROBIN@example.com")

	firstRef, _ := first["reference"].(string)
	secondRef, _ := second["reference"].(string)

	a, err := e.lookupByReference(firstRef)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	b, err := e.lookupByReference(secondRef)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if a.ContactID != b.ContactID {
		t.Errorf("two reports from one guest produced two contacts: %s and %s",
			a.ContactID, b.ContactID)
	}

	var contacts int64
	if err := e.db.Model(&domain.Contact{}).
		Where("LOWER(primary_email) = ?", "robin@example.com").Count(&contacts).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if contacts != 1 {
		t.Errorf("%d contacts for one guest email, want 1", contacts)
	}
}

// The security decision in this story.
//
// A guest has typed an address, not proved they own it. Matching onto an
// account-holder's record would let anyone file a report into a named
// resident's history from a form with no sign-in, and send that resident
// notifications about something they never reported.
func TestGuestDoesNotJoinAnAccountHoldersContact(t *testing.T) {
	e := newEnv(t)

	// A real citizen signs in, which provisions a contact with a C2 identity.
	client := e.portalSignIn(t, "citizen-account")
	var profile map[string]any
	if code := doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/me", nil, &profile); code != 200 {
		t.Fatalf("profile -> %d", code)
	}

	var account domain.Contact
	if err := e.db.Joins("JOIN contact_identities ON contact_identities.contact_id = contacts.id").
		Where("contact_identities.provider = ?", domain.ProviderC2).
		First(&account).Error; err != nil {
		t.Fatalf("account contact: %v", err)
	}
	if account.PrimaryEmail == "" {
		t.Skip("the stub provisioned no email; nothing to collide with")
	}

	// Somebody now files a guest report quoting that same address.
	created := guestReport(t, e, "Filed by a stranger", "Not Them", account.PrimaryEmail)
	reference, _ := created["reference"].(string)
	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	if req.ContactID == account.ID {
		t.Fatal("a guest report was attached to an account-holder's contact; " +
			"anyone can now write into a named resident's history from an unauthenticated form")
	}

	// The account holder's own view must not contain it.
	var mine struct {
		Items []struct {
			Reference string `json:"reference"`
		} `json:"items"`
	}
	if code := doJSON(t, client, http.MethodGet, e.api.URL+"/api/portal/requests", nil, &mine); code != 200 {
		t.Fatalf("my requests -> %d", code)
	}
	for _, item := range mine.Items {
		if item.Reference == reference {
			t.Error("the injected report appears in the account holder's own reports")
		}
	}
}

// Contact details are what separates a guest from an anonymous report, so an
// empty email must take the anonymous path rather than creating a nameless
// contact nobody can be reached at.
func TestSubmissionWithoutContactDetailsStaysAnonymous(t *testing.T) {
	e := newEnv(t)

	var created map[string]any
	code := doJSON(t, newJarClient(), http.MethodPost, e.api.URL+"/api/portal/requests", map[string]any{
		"serviceTypeId": publicServiceType(t, e, "POTHOLE"),
		"subject":       "No details given",
		"address1":      "44 Elm Street",
		"formData":      map[string]any{"size": "Medium"},
		"formToken":     formToken(t, e),
		"contactName":   "  ",
		"contactEmail":  "  ",
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("report -> %d", code)
	}

	reference, _ := created["reference"].(string)
	req, err := e.lookupByReference(reference)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if req.Channel != domain.ChannelAnonymous {
		t.Errorf("channel = %q, want %q", req.Channel, domain.ChannelAnonymous)
	}
	if req.ContactID != "" {
		t.Error("a blank email produced a contact")
	}
}
