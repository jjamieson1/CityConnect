package notifications

import (
	"context"
	"io"
	"log/slog"
	"net/textproto"
	"testing"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/mailer"
	"github.com/jjamieson1/CityConnect/internal/storetest"
)

// fakeSender records what it was asked to send and answers however the test
// needs, so none of this needs a mail server.
type fakeSender struct {
	sent   []mailer.Message
	result mailer.Result
}

func (f *fakeSender) Send(_ context.Context, msg mailer.Message) mailer.Result {
	f.sent = append(f.sent, msg)
	if f.result.Outcome == "" {
		return mailer.Result{Outcome: mailer.OutcomeSent, Code: 250}
	}
	return f.result
}

func newDispatchEnv(t *testing.T, sender mailer.Sender) (*Service, *gorm.DB) {
	t.Helper()

	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &Service{
		db:  db,
		cfg: &config.Config{Job: config.JobConfig{OutboxMaxAttempts: 3}},
		// No C2 client: these tests are about the email transport, and a row
		// queued for C2 would need one.
		audit: audit.NewService(db, log),
		log:   log,
	}
	svc.SetMailer(sender)
	return svc, db
}

func queueEmail(t *testing.T, svc *Service, to string) {
	t.Helper()
	err := svc.Enqueue(context.Background(), EnqueueInput{
		ContactID: domain.NewID(), Transport: domain.TransportEmail,
		Recipient: to, Event: domain.EventRequestCreated,
		Subject: "We have your report BBY-7K4M-2QX9",
		Body:    "Thank you. Quote BBY-7K4M-2QX9 if you contact us.",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

// The point of the story: somebody with no C2 account gets told something.
func TestEmailTransportDeliversToAGuest(t *testing.T) {
	sender := &fakeSender{}
	svc, db := newDispatchEnv(t, sender)
	queueEmail(t, svc, "guest@example.com")

	res, err := svc.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("sent %d, want 1 (result %+v)", res.Sent, res)
	}
	if len(sender.sent) != 1 || sender.sent[0].To != "guest@example.com" {
		t.Fatalf("mailer received %+v", sender.sent)
	}
	if !containsReference(sender.sent[0]) {
		t.Error("the confirmation does not carry the reference, which is its whole purpose")
	}

	var row domain.NotificationOutbox
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.State != domain.OutboxSent {
		t.Errorf("state = %q, want %q", row.State, domain.OutboxSent)
	}
	if row.SentAt == nil {
		t.Error("no sent timestamp recorded")
	}
}

func containsReference(msg mailer.Message) bool {
	return len(msg.Subject) > 0 && len(msg.Body) > 0 &&
		(indexOf(msg.Subject, "BBY-7K4M-2QX9") >= 0 || indexOf(msg.Body, "BBY-7K4M-2QX9") >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// A hard bounce is the email equivalent of C2's 403 and must behave like it:
// permanent, not retried, and recorded so an agent can see this resident has to
// be reached another way.
func TestHardBounceSuppressesRatherThanRetries(t *testing.T) {
	sender := &fakeSender{result: mailer.Result{
		Outcome: mailer.OutcomeBounced, Code: 550,
		Err: &textproto.Error{Code: 550, Msg: "No such user"},
	}}
	svc, db := newDispatchEnv(t, sender)
	queueEmail(t, svc, "nobody@example.com")

	res, err := svc.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res.Suppressed != 1 {
		t.Fatalf("suppressed %d, want 1 (result %+v)", res.Suppressed, res)
	}

	var row domain.NotificationOutbox
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.State != domain.OutboxSuppressed {
		t.Errorf("state = %q, want %q", row.State, domain.OutboxSuppressed)
	}
	// The reason has to say what actually happened. "No consent" would tell an
	// agent something untrue about a resident who never had a C2 account.
	if row.SuppressReason != domain.SuppressBounced {
		t.Errorf("reason = %q, want %q", row.SuppressReason, domain.SuppressBounced)
	}
}

// A transient refusal must wait rather than burn the address. Greylisting is
// the common case and is answered by trying again shortly.
func TestTransientFailureRetriesAndKeepsTheAddress(t *testing.T) {
	sender := &fakeSender{result: mailer.Result{
		Outcome: mailer.OutcomeRetry, Code: 450,
		Err: &textproto.Error{Code: 450, Msg: "Try again later"},
	}}
	svc, db := newDispatchEnv(t, sender)
	queueEmail(t, svc, "greylisted@example.com")

	res, err := svc.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res.Retrying != 1 {
		t.Fatalf("retrying %d, want 1 (result %+v)", res.Retrying, res)
	}

	var row domain.NotificationOutbox
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.State != domain.OutboxPending {
		t.Errorf("state = %q, want it still pending", row.State)
	}
	if row.SuppressReason != "" {
		t.Errorf("a greylisted message suppressed the address with %q", row.SuppressReason)
	}
}

// Both transports share one queue. An email-bound message with no mailer must
// wait, not vanish: configuring one later should deliver the backlog.
func TestEmailWaitsWhenNoMailerIsConfigured(t *testing.T) {
	svc, db := newDispatchEnv(t, nil)
	queueEmail(t, svc, "guest@example.com")

	res, err := svc.Drain(context.Background(), 10)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res.Sent != 0 {
		t.Errorf("sent %d with no mailer", res.Sent)
	}

	var row domain.NotificationOutbox
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.State == domain.OutboxSent || row.State == domain.OutboxSuppressed {
		t.Errorf("state = %q; the message should still be waiting", row.State)
	}
}

// The transport is chosen when the message is queued and must be recorded, or
// the dispatcher cannot tell what to do with it.
func TestEnqueueRequiresAnAddressForItsTransport(t *testing.T) {
	svc, _ := newDispatchEnv(t, &fakeSender{})
	ctx := context.Background()

	if err := svc.Enqueue(ctx, EnqueueInput{
		Transport: domain.TransportEmail, Subject: "s", Body: "b",
	}); err == nil {
		t.Error("an email with no recipient was queued")
	}
	if err := svc.Enqueue(ctx, EnqueueInput{
		Transport: domain.TransportC2, Subject: "s", Body: "b",
	}); err == nil {
		t.Error("a C2 message with no subject identifier was queued")
	}
	if err := svc.Enqueue(ctx, EnqueueInput{
		Transport: "carrier-pigeon", Recipient: "r@example.com", Subject: "s", Body: "b",
	}); err == nil {
		t.Error("an unknown transport was accepted")
	}
}

// Existing callers pass no transport and must keep meaning C2 — anything else
// would quietly reroute staff and API traffic.
func TestEnqueueDefaultsToC2(t *testing.T) {
	svc, db := newDispatchEnv(t, &fakeSender{})

	if err := svc.Enqueue(context.Background(), EnqueueInput{
		ContactID: domain.NewID(), C2Sub: "sub-123", Subject: "s", Body: "b",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var row domain.NotificationOutbox
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.Transport != domain.TransportC2 {
		t.Errorf("transport = %q, want %q", row.Transport, domain.TransportC2)
	}
}

// Collapsing duplicates is a property of the queue, not of C2, so it has to
// keep working for the second transport.
func TestDuplicateEmailsAreCollapsed(t *testing.T) {
	svc, db := newDispatchEnv(t, &fakeSender{})

	queueEmail(t, svc, "guest@example.com")
	queueEmail(t, svc, "guest@example.com")

	var rows int64
	if err := db.Model(&domain.NotificationOutbox{}).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d rows queued for an identical message, want 1", rows)
	}
}
