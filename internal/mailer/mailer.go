// Package mailer sends email directly, for requesters C2 cannot reach.
//
// It exists for one case: somebody who reported a problem without a C2 account
// and would otherwise be told nothing at all — not a confirmation, not a
// resolution. It is a fallback, not a parallel notification system. A citizen
// who holds active consent is reached through C2, which gives the in-app inbox,
// the consent gate and their own channel preferences.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Outcomes, matching the vocabulary the notification dispatcher already speaks
// so a second transport needs no second set of rules.
const (
	// OutcomeSent — the server accepted it for delivery.
	OutcomeSent = "sent"
	// OutcomeBounced — a permanent refusal (5xx). The address is wrong or we
	// are not welcome. Retrying is pointless and looks like abuse.
	OutcomeBounced = "bounced"
	// OutcomeRetry — a transient refusal (4xx), a network failure, a greylist.
	OutcomeRetry = "retry"
)

// Result is the outcome of one send.
type Result struct {
	Outcome string
	// Code is the SMTP reply code where there was one.
	Code int
	Err  error
}

// Message is one email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender delivers a message. An interface so tests need no mail server and so a
// deployment that puts a different relay in front of us can.
type Sender interface {
	Send(ctx context.Context, msg Message) Result
}

// SMTP is a Sender over SMTP.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the envelope and header sender. A municipality's own address:
	// mail from a domain the resident recognises is mail they open.
	From     string
	FromName string
	Timeout  time.Duration
	// StartTLS upgrades the connection. On by default; off is for a relay on
	// localhost that has no certificate and never leaves the host.
	StartTLS bool
}

// New builds an SMTP sender. An empty host yields nil, meaning no mailer is
// configured — the caller decides what that means rather than being handed
// something that silently discards mail.
func New(cfg SMTP) *SMTP {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &cfg
}

// Send delivers one message.
func (s *SMTP) Send(ctx context.Context, msg Message) Result {
	if strings.TrimSpace(msg.To) == "" {
		return Result{Outcome: OutcomeBounced, Err: errors.New("mailer: no recipient")}
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dialer := net.Dialer{Timeout: s.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		// Unreachable is transient by default: a relay restarting is the common
		// case, and giving up on it would lose a resident's confirmation.
		return Result{Outcome: OutcomeRetry, Err: fmt.Errorf("mailer: dial %s: %w", addr, err)}
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(s.Timeout))
	}

	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return Result{Outcome: OutcomeRetry, Err: fmt.Errorf("mailer: greet: %w", err)}
	}
	defer func() { _ = client.Quit() }()

	if s.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return Result{Outcome: OutcomeRetry, Err: fmt.Errorf("mailer: starttls: %w", err)}
			}
		}
	}

	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := client.Auth(auth); err != nil {
			// Our credentials, not the recipient's problem. Permanent until an
			// operator fixes it, and it must never be mistaken for a bounce —
			// that would suppress the resident's address for our mistake.
			return Result{Outcome: OutcomeRetry, Err: fmt.Errorf("mailer: auth: %w", err)}
		}
	}

	if err := client.Mail(s.From); err != nil {
		return classify(err, "mail from")
	}
	if err := client.Rcpt(msg.To); err != nil {
		// This is where a bad address shows up, and where a bounce is decided.
		return classify(err, "rcpt to")
	}

	w, err := client.Data()
	if err != nil {
		return classify(err, "data")
	}
	if _, err := w.Write([]byte(s.render(msg))); err != nil {
		return Result{Outcome: OutcomeRetry, Err: fmt.Errorf("mailer: write: %w", err)}
	}
	if err := w.Close(); err != nil {
		return classify(err, "send")
	}

	return Result{Outcome: OutcomeSent, Code: 250}
}

// render builds the message. Plain text deliberately: a service-request
// confirmation is a reference number and a sentence, HTML would add a tracking
// surface and a rendering problem for no benefit, and plain text is what
// survives every client a resident might use.
func (s *SMTP) render(msg Message) string {
	from := s.From
	if s.FromName != "" {
		from = mime.QEncoding.Encode("utf-8", s.FromName) + " <" + s.From + ">"
	}

	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + msg.To + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	// A confirmation is not a newsletter, but a resident may still try, and a
	// mailbox that cannot unsubscribe marks us as spam instead.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	// Dot-stuffing: a line that is a single dot would otherwise end the message.
	b.WriteString(strings.ReplaceAll(normaliseNewlines(msg.Body), "\r\n.", "\r\n.."))
	b.WriteString("\r\n")
	return b.String()
}

func normaliseNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// classify turns an SMTP error into an outcome.
//
// The distinction that matters is 4xx from 5xx. A 4xx is the server saying "not
// now" — greylisting, a full mailbox, a rate limit — and the message should
// wait. A 5xx is "not ever" for this address, and retrying it wastes attempts
// and looks like abuse to the receiving server.
func classify(err error, stage string) Result {
	code := replyCode(err)

	switch {
	case code >= 500 && code < 600:
		return Result{Outcome: OutcomeBounced, Code: code,
			Err: fmt.Errorf("mailer: %s: %w", stage, err)}
	case code >= 400 && code < 500:
		return Result{Outcome: OutcomeRetry, Code: code,
			Err: fmt.Errorf("mailer: %s: %w", stage, err)}
	}
	// No code at all is a broken connection rather than a verdict.
	return Result{Outcome: OutcomeRetry, Err: fmt.Errorf("mailer: %s: %w", stage, err)}
}

// replyCode pulls the reply code out of an SMTP error.
//
// net/smtp surfaces server replies as *textproto.Error, which carries the code
// as an integer — read it there rather than parsing the message, because the
// difference between 4xx and 5xx decides whether a resident's address gets
// suppressed. Connection failures arrive as bare errors with no code, and those
// are transient by definition.
func replyCode(err error) int {
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return protoErr.Code
	}
	return 0
}
