package httpapi

import (
	"strings"
	"testing"
	"time"
)

// In-package, so the min-age and expiry can be exercised at precise ages
// instead of by sleeping.

func TestFormTokenRoundTrip(t *testing.T) {
	f := newFormTokens("test-secret", 2*time.Second)
	now := time.Now()

	token, err := f.issue(now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := f.verify(token, now.Add(5*time.Second)); err != nil {
		t.Errorf("a token filled in at a human pace was refused: %v", err)
	}
}

// Single use is what makes a flood expensive: one token, one report.
func TestFormTokenIsSingleUse(t *testing.T) {
	f := newFormTokens("test-secret", 2*time.Second)
	now := time.Now()

	token, _ := f.issue(now)
	if err := f.verify(token, now.Add(5*time.Second)); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := f.verify(token, now.Add(6*time.Second)); err != errFormTokenReplayed {
		t.Errorf("second use = %v, want %v", err, errFormTokenReplayed)
	}
}

// The check penalises being fast, which is the direction that matters: someone
// using a screen reader is slower than average, never quicker.
func TestFormTokenRejectsSubmissionsFasterThanAPerson(t *testing.T) {
	f := newFormTokens("test-secret", 2*time.Second)
	now := time.Now()

	token, _ := f.issue(now)
	if err := f.verify(token, now.Add(200*time.Millisecond)); err != errFormTokenTooFast {
		t.Errorf("instant submission = %v, want %v", err, errFormTokenTooFast)
	}
}

// A token from the future is a clock problem or a forgery. Neither is a reason
// to accept it.
func TestFormTokenRejectsTokensFromTheFuture(t *testing.T) {
	f := newFormTokens("test-secret", 2*time.Second)
	now := time.Now()

	token, _ := f.issue(now.Add(time.Hour))
	if err := f.verify(token, now); err != errFormTokenTooFast {
		t.Errorf("future token = %v, want %v", err, errFormTokenTooFast)
	}
}

func TestFormTokenExpires(t *testing.T) {
	f := newFormTokens("test-secret", 2*time.Second)
	now := time.Now()

	token, _ := f.issue(now)
	if err := f.verify(token, now.Add(formTokenMaxAge+time.Minute)); err != errFormTokenExpired {
		t.Errorf("stale token = %v, want %v", err, errFormTokenExpired)
	}
}

// Minting is the attack that would make the whole control pointless.
func TestFormTokenCannotBeForged(t *testing.T) {
	f := newFormTokens("test-secret", 2*time.Second)
	other := newFormTokens("a-different-secret", 2*time.Second)
	now := time.Now()

	token, _ := other.issue(now)
	if err := f.verify(token, now.Add(5*time.Second)); err != errFormTokenBadSig {
		t.Errorf("token signed with another key = %v, want %v", err, errFormTokenBadSig)
	}

	// Backdating the timestamp to defeat the max age must not verify either:
	// the signature covers it.
	valid, _ := f.issue(now)
	parts := strings.Split(valid, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape %q", valid)
	}
	tampered := parts[0] + "." + "9999999999" + "." + parts[2]
	if err := f.verify(tampered, now.Add(5*time.Second)); err != errFormTokenBadSig {
		t.Errorf("token with an edited timestamp = %v, want %v", err, errFormTokenBadSig)
	}
}

func TestFormTokenRejectsRubbish(t *testing.T) {
	f := newFormTokens("test-secret", 2*time.Second)
	now := time.Now()

	for _, bad := range []string{"", "nonsense", "one.two", "a.b.c", "a.notanumber.c"} {
		if err := f.verify(bad, now); err == nil {
			t.Errorf("verify(%q) was accepted", bad)
		}
	}
}

// A zero min-age disables only that check. Everything else must still hold, or
// a test configuration would quietly be an insecure one.
func TestFormTokenZeroMinAgeStillEnforcesTheRest(t *testing.T) {
	f := newFormTokens("test-secret", 0)
	now := time.Now()

	token, _ := f.issue(now)
	if err := f.verify(token, now); err != nil {
		t.Fatalf("immediate use with no min age: %v", err)
	}
	if err := f.verify(token, now); err != errFormTokenReplayed {
		t.Errorf("replay = %v, want %v", err, errFormTokenReplayed)
	}
}
