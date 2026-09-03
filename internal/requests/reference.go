package requests

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// referenceAlphabet is Crockford base32: the digits plus the letters, less
// I, L, O and U.
//
// I/L/1 and O/0 are the pairs people confuse when a reference is read down a
// phone line or copied off a printed confirmation, and U is dropped so a
// random draw cannot spell something the city would rather not print. Exactly
// 32 symbols, which also makes the draw below unbiased.
const referenceAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Reference shape: a prefix, then two groups of referenceGroup symbols.
//
// Sixteen symbols would be needlessly long to read aloud; eight gives 32^8 —
// about 1.1e12 — which at any rate limit a public endpoint would tolerate is
// not searchable, and the unique index catches the birthday case regardless.
const (
	referenceGroups = 2
	referenceGroup  = 4
)

// DefaultReferencePrefix is used when a deployment configures none.
//
// It matches the historical format's prefix so an existing deployment does not
// silently change the shape of the number its residents already quote; only
// the part after it stops being sequential.
const DefaultReferencePrefix = "SR"

// maxReferencePrefix keeps the whole reference quotable.
const maxReferencePrefix = 8

// referenceAttempts bounds the redraws on a collision. With 32^8 values a
// second attempt is already improbable; a third failing means the unique
// index is complaining about something other than luck, and that should
// surface rather than spin.
const referenceAttempts = 3

// NewReference returns a fresh, unguessable request reference such as
// BBY-7K4M-2QX9.
//
// The value carries no information: not a year, not a count, not a service
// type. That is the point — a reference is quoted over a public tracking
// endpoint, and anything derivable from it is derivable by everyone.
func NewReference(prefix string) (string, error) {
	buf := make([]byte, referenceGroups*referenceGroup)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("requests: generate reference: %w", err)
	}

	var b strings.Builder
	b.WriteString(NormalizeReferencePrefix(prefix))
	for i, r := range buf {
		if i%referenceGroup == 0 {
			b.WriteByte('-')
		}
		// len(referenceAlphabet) is 32, which divides 256 exactly, so the
		// modulo introduces no bias toward the low symbols.
		b.WriteByte(referenceAlphabet[int(r)%len(referenceAlphabet)])
	}
	return b.String(), nil
}

// generatedReference matches a reference this package would produce: a prefix,
// then referenceGroups groups of referenceGroup symbols drawn from the
// alphabet. Anything else is a historical value from the counter era.
var generatedReference = regexp.MustCompile(
	`^[A-Z0-9]{1,` + strconv.Itoa(maxReferencePrefix) + `}` +
		strings.Repeat(`-[`+referenceAlphabet+`]{`+strconv.Itoa(referenceGroup)+`}`, referenceGroups) +
		`$`)

// IsGeneratedReference reports whether a reference was drawn at random rather
// than allocated in sequence.
//
// It exists so an operator can tell which rows still carry a guessable
// reference — see the reissue-references command in ccadm.
func IsGeneratedReference(reference string) bool {
	return generatedReference.MatchString(reference)
}

// NormalizeReferencePrefix reduces a configured prefix to something safe to
// put in front of a reference: upper-case letters and digits only, bounded in
// length, falling back to the default when nothing usable is left.
//
// A prefix arriving from configuration must not be able to introduce a hyphen
// or punctuation, because NormalizeReference splits on the first hyphen to
// decide which part of a reference is the random half.
func NormalizeReferencePrefix(prefix string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(prefix)) {
		if b.Len() == maxReferencePrefix {
			break
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return DefaultReferencePrefix
	}
	return b.String()
}

// NormalizeReference canonicalises a reference the way somebody might have
// typed it, so a resident quoting one over the phone is not defeated by the
// shape of the glyphs.
//
// Case and surrounding space are normalised throughout. Beyond the first
// hyphen — the random half of a new reference, and the digits of a historical
// SR-2026-000001 — O is folded to 0 and I and L to 1.
//
// That folding is safe rather than merely convenient: referenceAlphabet omits
// those letters and the historical format's tail is digits, so no stored
// reference can contain one. The fold can therefore rescue a mistyped
// character but can never turn one request's reference into another's. The
// prefix is deliberately left alone, since a deployment may legitimately
// configure one containing an O or an I.
func NormalizeReference(reference string) string {
	s := strings.ToUpper(strings.TrimSpace(reference))

	i := strings.IndexByte(s, '-')
	if i < 0 {
		return s
	}

	var b strings.Builder
	b.WriteString(s[:i])
	for _, r := range s[i:] {
		switch r {
		case 'O':
			b.WriteByte('0')
		case 'I', 'L':
			b.WriteByte('1')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
