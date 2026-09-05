package mailer

import (
	"errors"
	"net/textproto"
	"strings"
	"testing"
)

// The distinction the whole transport rests on. A 4xx is "not now" — greylist,
// full mailbox, rate limit — and the message should wait. A 5xx is "not ever"
// for this address, and retrying wastes attempts and looks like abuse.
//
// Getting it backwards is expensive both ways: treating a 4xx as a bounce
// suppresses a resident who was perfectly reachable, and treating a 5xx as
// transient hammers a server that has already said no.
func TestClassifyDistinguishesTransientFromPermanent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"mailbox unavailable", &textproto.Error{Code: 550, Msg: "No such user"}, OutcomeBounced},
		{"blocked", &textproto.Error{Code: 554, Msg: "Rejected"}, OutcomeBounced},
		{"greylisted", &textproto.Error{Code: 450, Msg: "Try again later"}, OutcomeRetry},
		{"over quota", &textproto.Error{Code: 452, Msg: "Insufficient storage"}, OutcomeRetry},
		{"connection dropped", errors.New("write: broken pipe"), OutcomeRetry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(c.err, "rcpt to").Outcome; got != c.want {
				t.Errorf("classify(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// A wrapped error must still be read correctly — the code is what decides
// whether a resident's address gets suppressed, so it cannot be lost to
// wrapping on the way up.
func TestReplyCodeSurvivesWrapping(t *testing.T) {
	wrapped := errors.Join(errors.New("context"), &textproto.Error{Code: 550, Msg: "gone"})
	if got := replyCode(wrapped); got != 550 {
		t.Errorf("replyCode = %d, want 550", got)
	}
}

// A message with nothing in the To field cannot be delivered by anyone, and
// treating that as transient would retry it forever.
func TestEmptyRecipientIsPermanent(t *testing.T) {
	s := New(SMTP{Host: "mail.example", From: "city@example.gov"})
	res := s.Send(t.Context(), Message{To: "  ", Subject: "x", Body: "y"})
	if res.Outcome != OutcomeBounced {
		t.Errorf("outcome = %q, want %q", res.Outcome, OutcomeBounced)
	}
}

// No host means no mailer, so a deployment that forgot to configure one gets
// nil and has to decide what that means — rather than something that accepts
// mail and drops it.
func TestNoHostYieldsNoSender(t *testing.T) {
	if s := New(SMTP{}); s != nil {
		t.Error("an empty host produced a sender")
	}
	if s := New(SMTP{Host: "   "}); s != nil {
		t.Error("a blank host produced a sender")
	}
}

func TestRenderProducesAWellFormedMessage(t *testing.T) {
	s := New(SMTP{Host: "mail.example", From: "city@example.gov", FromName: "City of Burnaby"})

	out := s.render(Message{
		To:      "resident@example.com",
		Subject: "We have your report BBY-7K4M-2QX9",
		Body:    "Thank you.\nWe will be in touch.",
	})

	for _, want := range []string{
		"To: resident@example.com",
		"Content-Type: text/plain; charset=utf-8",
		"MIME-Version: 1.0",
		"Auto-Submitted: auto-generated",
		"city@example.gov",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered message is missing %q", want)
		}
	}

	// Headers and body have to be separated by a blank line, or the body
	// becomes headers and the resident sees nothing.
	if !strings.Contains(out, "\r\n\r\n") {
		t.Error("no blank line between headers and body")
	}
	// Bare newlines in a body are a common source of mangled mail.
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Error("the message contains bare newlines")
	}
}

// A line consisting of a single dot ends an SMTP message. A resident writing
// one in their description must not truncate their own confirmation.
func TestRenderStuffsLeadingDots(t *testing.T) {
	s := New(SMTP{Host: "mail.example", From: "city@example.gov"})

	out := s.render(Message{To: "r@example.com", Subject: "s", Body: "before\n.\nafter"})
	if !strings.Contains(out, "\r\n..\r\n") {
		t.Errorf("a lone dot was not stuffed:\n%s", out)
	}
}

// The subject carries a reference and may carry a place name. Non-ASCII must
// not corrupt it.
func TestRenderEncodesNonASCIISubjects(t *testing.T) {
	s := New(SMTP{Host: "mail.example", From: "city@example.gov"})

	out := s.render(Message{To: "r@example.com", Subject: "Réparation de la chaussée", Body: "x"})
	subject := ""
	for _, line := range strings.Split(out, "\r\n") {
		if strings.HasPrefix(line, "Subject: ") {
			subject = line
		}
	}
	if strings.Contains(subject, "é") {
		t.Errorf("subject was not encoded: %q", subject)
	}
	if !strings.Contains(subject, "=?utf-8?") {
		t.Errorf("subject is not RFC 2047 encoded: %q", subject)
	}
}
