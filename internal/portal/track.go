package portal

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
)

// TrackInput is a lookup by somebody who is not signed in.
type TrackInput struct {
	Reference    string
	Verification string
}

// decoyDigest is hashed against when there is nothing real to compare, so the
// not-found path does the same work as the wrong-answer path.
var decoyDigest = sha256.Sum256([]byte("portal: no such request"))

// Track resolves one request for a requester with no session, given the
// reference and the contact detail the request was filed under.
//
// This is the only way to read a request without authenticating, so the
// verification value *is* the authorization — there is no session behind it to
// fall back on. Three properties follow, and none of them are optional:
//
//   - Every failure is identical. An unknown reference, a reference belonging
//     to somebody else, a request with no contact, and a wrong verification
//     value all return ErrNotFound with the same message. Distinguishing them
//     would turn this into an oracle for which references exist, which is
//     exactly the enumeration the random reference format was adopted to
//     prevent.
//   - Comparison is over fixed-length digests in constant time, so neither the
//     value nor its length leaks through timing.
//   - Nothing beyond the citizen projection is loaded or returned.
//
// Timing is equalised where it is cheap to do so, but a database hit and a miss
// are not identical however carefully this is written. The rate limiter in
// front of the endpoint is the real control; this reduces the signal rather
// than eliminating it.
func (s *Service) Track(ctx context.Context, in TrackInput) (*MyRequest, error) {
	verification := normalizeVerification(in.Verification)

	req, err := s.requests.GetByReference(ctx, in.Reference)
	if err != nil || verification == "" {
		// Still compare, so a miss costs roughly what a hit costs.
		constantTimeEqual(decoyDigest, sha256.Sum256([]byte(verification)))
		return nil, ErrNotFound
	}

	// An anonymous report has no contact and so has nothing to verify against.
	// It is untrackable by design (G·1-016) and must fail like anything else:
	// a distinct "anonymous reports cannot be tracked" here would confirm the
	// reference is real. The tracking form says it up front instead, which is
	// where a resident can act on it.
	if req.ContactID == "" {
		constantTimeEqual(decoyDigest, sha256.Sum256([]byte(verification)))
		return nil, ErrNotFound
	}

	contact, err := s.contacts.Get(ctx, req.ContactID)
	if err != nil {
		constantTimeEqual(decoyDigest, sha256.Sum256([]byte(verification)))
		return nil, ErrNotFound
	}

	if !verificationMatches(contact, verification) {
		return nil, ErrNotFound
	}

	view := s.project(req)
	updates, err := s.updatesFor(ctx, req)
	if err != nil {
		return nil, err
	}
	view.Updates = updates

	// Successful tracking discloses personal information to an unauthenticated
	// caller, so it belongs in the record. Failures deliberately do not: they
	// are attacker-controlled, and letting an unauthenticated flood append to a
	// hash-chained log is its own denial of service. The rate limiter counts
	// those instead.
	s.audit.Record(ctx, audit.Actor{Type: audit.ActorSystem, Label: "public tracking"}, audit.Entry{
		Action: "request.tracked", TargetType: "request", TargetID: req.ID,
		Summary: req.Reference + " viewed by reference and verification",
	})

	return &view, nil
}

// verificationMatches reports whether the value quoted matches a contact detail
// the request was filed under.
//
// Email or phone, because a resident who gave a phone number should not be
// asked for an email they never supplied. Both are compared, and both are
// compared even when the first matches, so the answer takes the same time
// whichever detail was right.
func verificationMatches(contact *domain.Contact, verification string) bool {
	// A contact holding neither detail would otherwise be matched by an empty
	// submission hashing equal to an empty stored value. Track refuses an empty
	// verification before reaching here; this is the second lock on that door.
	if verification == "" {
		return false
	}

	candidate := sha256.Sum256([]byte(verification))

	email := constantTimeEqual(candidate, sha256.Sum256([]byte(normalizeVerification(contact.PrimaryEmail))))
	phone := constantTimeEqual(candidate, sha256.Sum256([]byte(normalizePhone(contact.PrimaryPhone))))

	return email || phone
}

func constantTimeEqual(a, b [32]byte) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// normalizeVerification canonicalises an email address for comparison. Case
// and surrounding space are the differences a resident introduces retyping an
// address they gave months ago.
func normalizeVerification(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// normalizePhone reduces a phone number to its digits, so +1 (555) 010-0100
// and 5550100100 are the same answer. Returns "" for a value with no digits,
// which never matches.
func normalizePhone(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() < 7 {
		// Too short to be a phone number, and too short to be worth accepting
		// as a secret.
		return ""
	}
	return b.String()
}
