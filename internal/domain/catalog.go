package domain

import "time"

// ServiceType is an entry in the service catalogue: the kind of thing a
// citizen can ask the city for. It carries the intake form schema, the routing
// default, the SLA policy, and the binding to a C2 Service Card.
type ServiceType struct {
	Base
	Code         string `gorm:"size:60;uniqueIndex;not null" json:"code"`
	Name         string `gorm:"size:200;not null" json:"name"`
	Category     string `gorm:"size:80;index" json:"category,omitempty"`
	Description  string `gorm:"type:text" json:"description,omitempty"`
	DepartmentID string `gorm:"type:char(36);index" json:"departmentId,omitempty"`

	DefaultQueueID    string `gorm:"type:char(36);index" json:"defaultQueueId,omitempty"`
	SLAPolicyID       string `gorm:"type:char(36);index" json:"slaPolicyId,omitempty"`
	DefaultPriority   string `gorm:"size:20;not null;default:'normal'" json:"defaultPriority"`
	C2ServiceCardID   string `gorm:"size:120;index" json:"c2ServiceCardId,omitempty"`
	RequiresLocation  bool   `gorm:"not null;default:false" json:"requiresLocation"`
	AllowsAttachments bool   `gorm:"not null;default:true" json:"allowsAttachments"`
	PublicVisible     bool   `gorm:"not null;default:true" json:"publicVisible"`
	Active            bool   `gorm:"not null;default:true" json:"active"`

	// IntakeForm describes the extra fields captured for this service type.
	// See FormField for the element shape.
	IntakeForm JSONMap `gorm:"type:text" json:"intakeForm"`

	// CitizenSummaryTemplate renders this request's line in the C2 Service Card
	// callout. Falls back to a generic summary when empty.
	CitizenSummaryTemplate string `gorm:"type:text" json:"citizenSummaryTemplate,omitempty"`

	Department *Department `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
	SLAPolicy  *SLAPolicy  `gorm:"foreignKey:SLAPolicyID" json:"slaPolicy,omitempty"`
}

// FormField is one element of a ServiceType's intake form. Stored inside
// IntakeForm as {"fields": [...]}, validated on request creation.
type FormField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text | textarea | number | date | select | multiselect | checkbox
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
	Help     string   `json:"help,omitempty"`
	Pattern  string   `json:"pattern,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
}

// SLAPolicy defines response and resolution targets. Targets are expressed in
// business minutes and measured against a BusinessCalendar, because a request
// logged at 4:55pm Friday is not late at 9am Monday.
type SLAPolicy struct {
	Base
	Name        string `gorm:"size:160;not null" json:"name"`
	Description string `gorm:"type:text" json:"description,omitempty"`
	CalendarID  string `gorm:"type:char(36);index" json:"calendarId,omitempty"`

	FirstResponseMinutes int `gorm:"not null;default:480" json:"firstResponseMinutes"`
	ResolutionMinutes    int `gorm:"not null;default:2880" json:"resolutionMinutes"`

	// PriorityOverrides maps a priority name to {"firstResponseMinutes":n,
	// "resolutionMinutes":n}. Absent priorities use the base values.
	PriorityOverrides JSONMap `gorm:"type:text" json:"priorityOverrides"`

	// PauseStatuses stop the clock while the city is waiting on someone else.
	// Without this, every request that waits on a citizen breaches.
	PauseStatuses StringList `gorm:"type:text" json:"pauseStatuses"`

	WarnAtPercent int  `gorm:"not null;default:80" json:"warnAtPercent"`
	Active        bool `gorm:"not null;default:true" json:"active"`

	Calendar *BusinessCalendar `gorm:"foreignKey:CalendarID" json:"calendar,omitempty"`
}

// TargetsFor returns the response and resolution targets for a priority.
func (p *SLAPolicy) TargetsFor(priority string) (firstResponse, resolution int) {
	firstResponse, resolution = p.FirstResponseMinutes, p.ResolutionMinutes
	raw, ok := p.PriorityOverrides[priority]
	if !ok {
		return
	}
	over, ok := raw.(map[string]any)
	if !ok {
		return
	}
	if v, ok := toInt(over["firstResponseMinutes"]); ok {
		firstResponse = v
	}
	if v, ok := toInt(over["resolutionMinutes"]); ok {
		resolution = v
	}
	return
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// BusinessCalendar describes when the city is open, so SLA clocks only run
// during working hours.
type BusinessCalendar struct {
	Base
	Name     string `gorm:"size:160;not null" json:"name"`
	TimeZone string `gorm:"size:60;not null;default:'America/Toronto'" json:"timeZone"`

	// Hours maps a weekday number ("0"=Sunday … "6"=Saturday) to a list of
	// {"start":"08:30","end":"16:30"} windows. A missing or empty weekday is a
	// closed day.
	Hours JSONMap `gorm:"type:text" json:"hours"`

	// Holidays are YYYY-MM-DD dates treated as fully closed.
	Holidays StringList `gorm:"type:text" json:"holidays"`

	// AlwaysOpen bypasses the calendar entirely, for 24/7 services such as
	// water main breaks.
	AlwaysOpen bool `gorm:"not null;default:false" json:"alwaysOpen"`

	IsDefault bool `gorm:"not null;default:false" json:"isDefault"`
}

// HoursWindow is one open period within a day.
type HoursWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// NotificationTemplate renders citizen-facing messages for an event. Templates
// are keyed by event plus language so a bilingual municipality can serve both
// without branching in code.
type NotificationTemplate struct {
	Base
	Event         string `gorm:"size:80;not null;uniqueIndex:idx_template_event_lang" json:"event"`
	Language      string `gorm:"size:12;not null;default:'en';uniqueIndex:idx_template_event_lang" json:"language"`
	ServiceTypeID string `gorm:"type:char(36);index;uniqueIndex:idx_template_event_lang" json:"serviceTypeId,omitempty"`

	Subject   string `gorm:"size:250;not null" json:"subject"`
	Body      string `gorm:"type:text;not null" json:"body"`
	ShortBody string `gorm:"size:400" json:"shortBody,omitempty"`
	Category  string `gorm:"size:20;not null;default:'BUSINESS'" json:"category"`
	Active    bool   `gorm:"not null;default:true" json:"active"`

	Description string `gorm:"size:400" json:"description,omitempty"`
}

// Notification events that templates can be registered against.
const (
	EventRequestCreated   = "request.created"
	EventRequestAssigned  = "request.assigned"
	EventStatusChanged    = "request.status_changed"
	EventCitizenComment   = "request.comment"
	EventRequestResolved  = "request.resolved"
	EventRequestClosed    = "request.closed"
	EventCSATSurvey       = "request.csat_survey"
	EventSLABreachWarning = "request.sla_warning"
)

// Macro is a canned response an agent can apply to a request: a comment body
// plus optional side effects such as a status change or tag.
type Macro struct {
	Base
	Name         string     `gorm:"size:160;not null" json:"name"`
	Description  string     `gorm:"size:400" json:"description,omitempty"`
	DepartmentID string     `gorm:"type:char(36);index" json:"departmentId,omitempty"`
	Body         string     `gorm:"type:text" json:"body"`
	Visibility   string     `gorm:"size:20;not null;default:'internal'" json:"visibility"`
	SetStatus    string     `gorm:"size:30" json:"setStatus,omitempty"`
	SetPriority  string     `gorm:"size:20" json:"setPriority,omitempty"`
	AddTags      StringList `gorm:"type:text" json:"addTags"`
	NotifyCitzn  bool       `gorm:"column:notify_citizen;not null;default:false" json:"notifyCitizen"`
	Active       bool       `gorm:"not null;default:true" json:"active"`
	UsageCount   int        `gorm:"not null;default:0" json:"usageCount"`
}

// SavedView is a named, shareable filter over requests or contacts. Without
// these a queue console is unusable past a few hundred open items.
type SavedView struct {
	Base
	Name       string  `gorm:"size:160;not null" json:"name"`
	Entity     string  `gorm:"size:30;not null;index" json:"entity"` // request | contact
	OwnerID    string  `gorm:"type:char(36);index" json:"ownerId,omitempty"`
	Shared     bool    `gorm:"not null;default:false" json:"shared"`
	Filters    JSONMap `gorm:"type:text" json:"filters"`
	Columns    StringList `gorm:"type:text" json:"columns"`
	SortBy     string  `gorm:"size:60" json:"sortBy,omitempty"`
	SortDir    string  `gorm:"size:8" json:"sortDir,omitempty"`
	IsDefault  bool    `gorm:"not null;default:false" json:"isDefault"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// RetentionPolicy expresses a municipal records schedule: how long an entity
// is kept, and whether it is then anonymised or destroyed.
type RetentionPolicy struct {
	Base
	Entity        string `gorm:"size:60;uniqueIndex;not null" json:"entity"`
	RetainMonths  int    `gorm:"not null" json:"retainMonths"`
	Action        string `gorm:"size:20;not null;default:'anonymize'" json:"action"` // anonymize | purge
	Enabled       bool   `gorm:"not null;default:false" json:"enabled"`
	Description   string `gorm:"size:400" json:"description,omitempty"`
	LastRunAt     *time.Time `json:"lastRunAt,omitempty"`
	LastAffected  int        `gorm:"not null;default:0" json:"lastAffected"`
}
