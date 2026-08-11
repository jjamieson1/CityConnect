package domain

import "time"

// Outbox delivery states.
const (
	OutboxPending    = "pending"
	OutboxSent       = "sent"
	OutboxFailed     = "failed"
	OutboxSuppressed = "suppressed"
)

// Suppression reasons. A 403 from C2 means the citizen holds no active consent
// for our application and a 404 means C2 does not know the subject at all.
// Neither is retryable — retrying them is how a client burns its rate limit
// achieving nothing — so both suppress permanently and flag the contact.
const (
	SuppressNoConsent   = "no_consent"
	SuppressUnknownSub  = "unknown_sub"
	SuppressNoC2Link    = "no_c2_identity"
	SuppressDoNotContct = "do_not_contact"
	SuppressOptedOut    = "opted_out"
	SuppressMaxAttempts = "max_attempts"
)

// NotificationOutbox is one durable citizen notification. Every send goes
// through this table rather than straight out over HTTP, so a C2 outage
// degrades into a queue rather than lost messages, and so the dispatcher can
// pace itself against C2's per-IP rate limit.
type NotificationOutbox struct {
	Base
	ContactID string `gorm:"type:char(36);index;not null" json:"contactId"`
	RequestID string `gorm:"type:char(36);index" json:"requestId,omitempty"`
	C2Sub     string `gorm:"size:255;index;not null" json:"c2Sub"`

	Event      string `gorm:"size:80;index" json:"event,omitempty"`
	Subject    string `gorm:"size:250;not null" json:"subject"`
	Body       string `gorm:"type:text;not null" json:"body"`
	ShortBody  string `gorm:"size:400" json:"shortBody,omitempty"`
	Category   string `gorm:"size:20;not null;default:'BUSINESS'" json:"category"`
	TemplateID string `gorm:"type:char(36)" json:"templateId,omitempty"`

	State         string     `gorm:"size:20;not null;default:'pending';index" json:"state"`
	Attempts      int        `gorm:"not null;default:0" json:"attempts"`
	NextAttemptAt time.Time  `gorm:"index;not null" json:"nextAttemptAt"`
	SentAt        *time.Time `json:"sentAt,omitempty"`

	// C2NotificationID and Channels come back on a 202. Channels lists what C2
	// dispatched beyond the always-created in-app notification; it is
	// informational, since the citizen chooses their channels, not us.
	C2NotificationID string     `gorm:"size:120;index" json:"c2NotificationId,omitempty"`
	Channels         StringList `gorm:"type:text" json:"channels"`

	LastStatusCode  int    `json:"lastStatusCode,omitempty"`
	LastError       string `gorm:"size:600" json:"lastError,omitempty"`
	SuppressReason  string `gorm:"size:40" json:"suppressReason,omitempty"`
	CreatedByID     string `gorm:"type:char(36)" json:"createdById,omitempty"`
	IdempotencyHash string `gorm:"size:64;index" json:"-"`
}

// Webhook delivery states mirror the outbox.
const (
	WebhookPending = "pending"
	WebhookSent    = "sent"
	WebhookFailed  = "failed"
	WebhookDead    = "dead"
)

// WebhookDelivery is one outbound event to a connected system. Failed
// deliveries land in a dead-letter state that an admin can inspect and replay,
// because a partner outage should not silently drop the events that happened
// during it.
type WebhookDelivery struct {
	Base
	SystemID  string `gorm:"type:char(36);index;not null" json:"systemId"`
	Event     string `gorm:"size:80;index;not null" json:"event"`
	RequestID string `gorm:"type:char(36);index" json:"requestId,omitempty"`
	URL       string `gorm:"size:400;not null" json:"url"`
	Payload   string `gorm:"type:text;not null" json:"payload"`

	State         string     `gorm:"size:20;not null;default:'pending';index" json:"state"`
	Attempts      int        `gorm:"not null;default:0" json:"attempts"`
	NextAttemptAt time.Time  `gorm:"index;not null" json:"nextAttemptAt"`
	DeliveredAt   *time.Time `json:"deliveredAt,omitempty"`

	LastStatusCode int    `json:"lastStatusCode,omitempty"`
	LastError      string `gorm:"size:600" json:"lastError,omitempty"`
	ReplayOfID     string `gorm:"type:char(36)" json:"replayOfId,omitempty"`
}

// CalloutLog records C2's Service Card callouts against us. C2 calls on every
// render and on a refresh timer while the card is on screen, so this is
// sampled rather than written per call.
type CalloutLog struct {
	Base
	C2Sub        string `gorm:"size:255;index;not null" json:"c2Sub"`
	ContactID    string `gorm:"type:char(36);index" json:"contactId,omitempty"`
	AuthMode     string `gorm:"size:20" json:"authMode,omitempty"`
	StatusCode   int    `json:"statusCode"`
	OpenRequests int    `json:"openRequests"`
	DurationMS   int64  `json:"durationMs"`
	Outcome      string `gorm:"size:40;index" json:"outcome,omitempty"`
	Error        string `gorm:"size:400" json:"error,omitempty"`
}

// ReportRollup is a pre-aggregated daily metric. Reports read from here rather
// than scanning the request table, which keeps dashboards fast as volume grows.
type ReportRollup struct {
	Base
	Day           time.Time `gorm:"index;not null;uniqueIndex:idx_rollup_unique" json:"day"`
	Metric        string    `gorm:"size:60;not null;uniqueIndex:idx_rollup_unique" json:"metric"`
	DepartmentID  string    `gorm:"type:char(36);uniqueIndex:idx_rollup_unique" json:"departmentId,omitempty"`
	QueueID       string    `gorm:"type:char(36);uniqueIndex:idx_rollup_unique" json:"queueId,omitempty"`
	ServiceTypeID string    `gorm:"type:char(36);uniqueIndex:idx_rollup_unique" json:"serviceTypeId,omitempty"`
	Dimension     string    `gorm:"size:80;uniqueIndex:idx_rollup_unique" json:"dimension,omitempty"`

	Count   int64   `gorm:"not null;default:0" json:"count"`
	SumVal  float64 `gorm:"not null;default:0" json:"sum"`
	AvgVal  float64 `gorm:"not null;default:0" json:"avg"`
	MinVal  float64 `gorm:"not null;default:0" json:"min"`
	MaxVal  float64 `gorm:"not null;default:0" json:"max"`
	P90Val  float64 `gorm:"not null;default:0" json:"p90"`
}

// Rollup metric names.
const (
	MetricRequestsOpened   = "requests_opened"
	MetricRequestsClosed   = "requests_closed"
	MetricSLAMet           = "sla_met"
	MetricSLABreached      = "sla_breached"
	MetricResolutionHours  = "resolution_hours"
	MetricFirstResponseHrs = "first_response_hours"
	MetricCSAT             = "csat"
	MetricReopened         = "reopened"
	MetricNotifications    = "notifications_sent"
)
