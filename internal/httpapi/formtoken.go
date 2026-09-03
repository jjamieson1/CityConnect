package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Form-token bounds.
const (
	// formTokenMaxAge lets somebody fill a form at their own pace — look up a
	// postcode, find the house number, come back — without the submission being
	// refused for staleness.
	formTokenMaxAge = 3 * time.Hour

	// formTokenSweepEvery bounds how often spent nonces are cleared out.
	formTokenSweepEvery = 10 * time.Minute
)

// Form-token verification failures. Deliberately distinct internally and
// deliberately indistinguishable to the caller — see handlePortalCreate.
var (
	errFormTokenMalformed = errors.New("httpapi: malformed form token")
	errFormTokenBadSig    = errors.New("httpapi: form token signature does not verify")
	errFormTokenExpired   = errors.New("httpapi: form token has expired")
	errFormTokenTooFast   = errors.New("httpapi: form token used faster than a person could")
	errFormTokenReplayed  = errors.New("httpapi: form token already used")
)

// formTokens issues and verifies the single-use tokens that gate anonymous
// submission.
//
// This is the bot control, and its shape is dictated by WCAG 2.2 SC 3.3.8:
// a puzzle CAPTCHA is a cognitive function test, and offering one without an
// alternative fails the accessibility standard the project commits to. So the
// challenge is not something the resident solves — it is something the *server*
// knows and a script has to work for:
//
//   - Single use. One token, one submission. A flood needs a token per attempt.
//   - Time-bound in both directions. Too fast is a script; too old is a replay.
//   - Signed. Tokens cannot be minted, only fetched, and fetching is itself
//     rate limited — which is what actually caps the sustained rate.
//
// Nothing is sent to a third party, so it also survives the data-residency
// answer in Appendix I, which a hosted CAPTCHA would not.
type formTokens struct {
	secret []byte

	// minAge is the shortest time between issue and use that we will believe
	// came from a person. It penalises being *fast*: someone using a screen
	// reader or a keyboard alone is slower, never quicker, so unlike a puzzle
	// this cannot catch the people a CAPTCHA usually does. Zero disables it.
	minAge time.Duration

	mu        sync.Mutex
	spent     map[string]time.Time
	lastSweep time.Time
}

// newFormTokens builds an issuer. An empty secret gets a random one, which is
// correct for a single instance and wrong for several — see the config comment.
func newFormTokens(secret string, minAge time.Duration) *formTokens {
	key := []byte(secret)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			// A process that cannot read randomness cannot safely issue
			// anything. Failing here is better than issuing forgeable tokens.
			panic("httpapi: no entropy for the form-token secret: " + err.Error())
		}
	}
	return &formTokens{
		secret: key, minAge: minAge,
		spent: map[string]time.Time{}, lastSweep: time.Now(),
	}
}

// issue mints a token bound to now.
func (f *formTokens) issue(now time.Time) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(nonce) + "." +
		strconv.FormatInt(now.Unix(), 10)
	return body + "." + f.sign(body), nil
}

// verify checks a token and spends it. A token that verifies once never
// verifies again.
func (f *formTokens) verify(token string, now time.Time) error {
	nonce, rest, ok := strings.Cut(token, ".")
	if !ok {
		return errFormTokenMalformed
	}
	issued, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return errFormTokenMalformed
	}
	issuedAt, err := strconv.ParseInt(issued, 10, 64)
	if err != nil {
		return errFormTokenMalformed
	}

	// Constant time, and before anything else is trusted: the timestamp is only
	// meaningful once the signature says we wrote it.
	if !hmac.Equal([]byte(sig), []byte(f.sign(nonce+"."+issued))) {
		return errFormTokenBadSig
	}

	age := now.Sub(time.Unix(issuedAt, 0))
	switch {
	case age > formTokenMaxAge:
		return errFormTokenExpired
	case age < f.minAge:
		// Includes a token from the future, which is a clock problem or a
		// forgery attempt and is not something to accept either way.
		return errFormTokenTooFast
	}

	return f.spend(nonce, now)
}

// spend records a nonce, refusing one already seen.
func (f *formTokens) spend(nonce string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if now.Sub(f.lastSweep) > formTokenSweepEvery {
		for n, at := range f.spent {
			// A nonce older than the maximum age can no longer verify on its
			// timestamp, so remembering it buys nothing.
			if now.Sub(at) > formTokenMaxAge {
				delete(f.spent, n)
			}
		}
		f.lastSweep = now
	}

	if _, seen := f.spent[nonce]; seen {
		return errFormTokenReplayed
	}
	f.spent[nonce] = now
	return nil
}

func (f *formTokens) sign(body string) string {
	mac := hmac.New(sha256.New, f.secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
