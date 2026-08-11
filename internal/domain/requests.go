package domain

import "time"

// RequestStatus is the lifecycle state of a service request. The set is fixed
// in code — labels are configurable in the UI, but the states are not, because
// SLA maths, routing and reporting all key off them.
type RequestStatus string

// Request statuses.
const (
	StatusNew                RequestStatus = "new"
	StatusTriaged            RequestStatus = "triaged"
	StatusAssigned           RequestStatus = "assigned"
	StatusInProgress         RequestStatus = "in_progress"
	StatusWaitingCitizen     RequestStatus = "waiting_citizen"
	StatusWaitingThirdParty  RequestStatus = "waiting_third_party"
	StatusResolved           RequestStatus = "resolved"
	StatusClosed             RequestStatus = "closed"
	StatusCancelled          RequestStatus = "cancelled"
)

// allowedTransitions is the state machine. Anything not listed is rejected
// with a 409 rather than written silently — an unconstrained status column is
// how a workflow quietly stops meaning anything.
var allowedTransitions = map[RequestStatus][]RequestStatus{
	StatusNew:               {StatusTriaged, StatusAssigned, StatusInProgress, StatusCancelled},
	StatusTriaged:           {StatusAssigned, StatusInProgress, StatusWaitingCitizen, StatusWaitingThirdParty, StatusCancelled},
	StatusAssigned:          {StatusInProgress, StatusTriaged, StatusWaitingCitizen, StatusWaitingThirdParty, StatusResolved, StatusCancelled},
	StatusInProgress:        {StatusWaitingCitizen, StatusWaitingThirdParty, StatusResolved, StatusAssigned, StatusCancelled},
	StatusWaitingCitizen:    {StatusInProgress, StatusResolved, StatusCancelled},
	StatusWaitingThirdParty: {StatusInProgress, StatusResolved, StatusCancelled},
	StatusResolved:          {StatusClosed, StatusInProgress}, // reopen
	StatusClosed:            {StatusInProgress},               // reopen
	StatusCancelled:         {StatusInProgress},               // reopen
}

// CanTransitionTo reports whether a move from s to next is legal.
func (s RequestStatus) CanTransitionTo(next RequestStatus) bool {
	if s == next {
		return false
	}
	for _, allowed := range allowedTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// NextStatuses lists the legal moves from s, for driving UI affordances.
func (s RequestStatus) NextStatuses() []RequestStatus {
	out := make([]RequestStatus, len(allowedTransitions[s]))
	copy(out, allowedTransitions[s])
	return out
}

// Terminal reports whether the request has left active work.
func (s RequestStatus) Terminal() bool {
	return s == StatusClosed || s == StatusCancelled
}

// Open reports whether the request still counts against open workload — and so
// whether it appears in the citizen's Service Card status bundle.
func (s RequestStatus) Open() bool {
	return s != StatusClosed && s != StatusCancelled
}

// Valid reports whether the value is a known status.
func (s RequestStatus) Valid() bool {
	_, ok := allowedTransitions[s]
	return ok
}

// Reopening reports whether moving from s to next constitutes a reopen, which
// is tracked separately because reopen rate is a quality signal.
func (s RequestStatus) Reopening(next RequestStatus) bool {
	return (s == StatusResolved || s == StatusClosed || s == StatusCancelled) && next == StatusInProgress
}

// Priority levels, ordered.
const (
	PriorityLow      = "low"
	PriorityNormal   = "normal"
	PriorityHigh     = "high"
	PriorityUrgent   = "urgent"
	PriorityCritical = "critical"
)

// PriorityRank orders priorities for sorting and comparison.
func PriorityRank(p string) int {
	switch p {
	case PriorityLow:
		return 1
	case PriorityNormal:
		return 2
	case PriorityHigh:
		return 3
	case PriorityUrgent:
		return 4
	case PriorityCritical:
		return 5
	}
	return 0
}

// Request sources.
const (
	SourceC2Card = "c2_card"
	SourceAPI    = "api"
	SourceAgent  = "agent"
	SourceEmail  = "email"
	SourceImport = "import"
)

// Request is a service request — the central work item. Reference is the
// human-quotable identifier a citizen reads over the phone; ID is the internal
// UUID.
type Request struct {
	Base
	Reference string `gorm:"size:32;uniqueIndex;not null" json:"reference"`

	ContactID     string `gorm:"type:char(36);index;not null" json:"contactId"`
	ServiceTypeID string `gorm:"type:char(36);index;not null" json:"serviceTypeId"`
	DepartmentID  string `gorm:"type:char(36);index" json:"departmentId,omitempty"`
	QueueID       string `gorm:"type:char(36);index" json:"queueId,omitempty"`

	// Exactly one assignee kind is set. A connected system is an assignee in
	// the same sense a person is.
	AssigneeUserID   string `gorm:"type:char(36);index" json:"assigneeUserId,omitempty"`
	AssigneeSystemID string `gorm:"type:char(36);index" json:"assigneeSystemId,omitempty"`

	Source       string `gorm:"size:20;not null;default:'agent';index" json:"source"`
	OriginSystem string `gorm:"size:60;index" json:"originSystem,omitempty"`
	ExternalRef  string `gorm:"size:120;index" json:"externalRef,omitempty"`

	Status   RequestStatus `gorm:"size:30;not null;default:'new';index" json:"status"`
	Priority string        `gorm:"size:20;not null;default:'normal';index" json:"priority"`

	Subject     string `gorm:"size:250;not null" json:"subject"`
	Description string `gorm:"type:text" json:"description,omitempty"`

	Address1   string  `gorm:"size:200" json:"address1,omitempty"`
	Address2   string  `gorm:"size:200" json:"address2,omitempty"`
	City       string  `gorm:"size:120" json:"city,omitempty"`
	State      string  `gorm:"size:60" json:"state,omitempty"`
	PostalCode string  `gorm:"size:20;index" json:"postalCode,omitempty"`
	Ward       string  `gorm:"size:60;index" json:"ward,omitempty"`
	ParcelID   string  `gorm:"size:80" json:"parcelId,omitempty"`
	Latitude   float64 `gorm:"index" json:"latitude,omitempty"`
	Longitude  float64 `gorm:"index" json:"longitude,omitempty"`

	FormData JSONMap    `gorm:"type:text" json:"formData"`
	Tags     StringList `gorm:"type:text" json:"tags"`

	OpenedAt        time.Time  `gorm:"index;not null" json:"openedAt"`
	FirstResponseAt *time.Time `json:"firstResponseAt,omitempty"`
	ResolvedAt      *time.Time `json:"resolvedAt,omitempty"`
	ClosedAt        *time.Time `gorm:"index" json:"closedAt,omitempty"`
	DueAt           *time.Time `gorm:"index" json:"dueAt,omitempty"`
	ResponseDueAt   *time.Time `gorm:"index" json:"responseDueAt,omitempty"`

	// SLA accounting. PausedTotal accumulates business time spent in a
	// pause status so the clock can be resumed correctly.
	SLAPolicyID      string     `gorm:"type:char(36);index" json:"slaPolicyId,omitempty"`
	SLABreached      bool       `gorm:"not null;default:false;index" json:"slaBreached"`
	SLAWarned        bool       `gorm:"not null;default:false" json:"slaWarned"`
	ResponseBreached bool       `gorm:"not null;default:false" json:"responseBreached"`
	PausedAt         *time.Time `json:"pausedAt,omitempty"`
	PausedMinutes    int        `gorm:"not null;default:0" json:"pausedMinutes"`

	ResolutionCode string `gorm:"size:60" json:"resolutionCode,omitempty"`
	ResolutionNote string `gorm:"type:text" json:"resolutionNote,omitempty"`

	CSATScore     *int       `json:"csatScore,omitempty"`
	CSATComment   string     `gorm:"type:text" json:"csatComment,omitempty"`
	CSATSentAt    *time.Time `json:"csatSentAt,omitempty"`
	ReopenCount   int        `gorm:"not null;default:0" json:"reopenCount"`
	LastActivityA time.Time  `gorm:"column:last_activity_at;index" json:"lastActivityAt"`

	// MergedIntoID is set when this request lost a duplicate merge. Every
	// reporter still receives updates from the survivor.
	MergedIntoID string `gorm:"type:char(36);index" json:"mergedIntoId,omitempty"`

	// Version drives optimistic concurrency. Two agents editing one ticket is
	// routine, and last-write-wins loses work silently.
	Version uint `gorm:"not null;default:1" json:"version"`

	Contact        *Contact         `gorm:"foreignKey:ContactID" json:"contact,omitempty"`
	ServiceType    *ServiceType     `gorm:"foreignKey:ServiceTypeID" json:"serviceType,omitempty"`
	Queue          *Queue           `gorm:"foreignKey:QueueID" json:"queue,omitempty"`
	Department     *Department      `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
	AssigneeUser   *User            `gorm:"foreignKey:AssigneeUserID" json:"assigneeUser,omitempty"`
	AssigneeSystem *ConnectedSystem `gorm:"foreignKey:AssigneeSystemID" json:"assigneeSystem,omitempty"`
}

// Assigned reports whether the request has an owner of either kind.
func (r *Request) Assigned() bool {
	return r.AssigneeUserID != "" || r.AssigneeSystemID != ""
}

// CommentVisibility controls who can see a comment. Citizen-visible comments
// are what feed the Service Card callout's description of actions taken.
type CommentVisibility string

// Comment visibilities.
const (
	VisibilityInternal CommentVisibility = "internal"
	VisibilityCitizen  CommentVisibility = "citizen"
)

// RequestComment is a note on a request, either internal or citizen-facing.
type RequestComment struct {
	Base
	RequestID  string            `gorm:"type:char(36);index;not null" json:"requestId"`
	AuthorID   string            `gorm:"type:char(36);index" json:"authorId,omitempty"`
	AuthorType string            `gorm:"size:20;not null;default:'user'" json:"authorType"` // user | system | citizen | job
	AuthorName string            `gorm:"size:200" json:"authorName,omitempty"`
	Visibility CommentVisibility `gorm:"size:20;not null;default:'internal';index" json:"visibility"`
	Body       string            `gorm:"type:text;not null" json:"body"`
	Notified   bool              `gorm:"not null;default:false" json:"notified"`
	MacroID    string            `gorm:"type:char(36)" json:"macroId,omitempty"`
	EditedAt   *time.Time        `json:"editedAt,omitempty"`
}

// Request event kinds recorded on the timeline.
const (
	EvtCreated         = "created"
	EvtStatusChanged   = "status_changed"
	EvtAssigned        = "assigned"
	EvtUnassigned      = "unassigned"
	EvtQueueChanged    = "queue_changed"
	EvtPriorityChanged = "priority_changed"
	EvtCommented       = "commented"
	EvtAttachmentAdded = "attachment_added"
	EvtNotificationSnt = "notification_sent"
	EvtCalloutServed   = "callout_served"
	EvtSLAWarning      = "sla_warning"
	EvtSLABreached     = "sla_breached"
	EvtLinked          = "linked"
	EvtMerged          = "merged"
	EvtReopened        = "reopened"
	EvtFieldsUpdated   = "fields_updated"
	EvtTransferred     = "transferred"
)

// RequestEvent is one entry in a request's append-only timeline.
type RequestEvent struct {
	Base
	RequestID  string  `gorm:"type:char(36);index;not null" json:"requestId"`
	Kind       string  `gorm:"size:40;not null;index" json:"kind"`
	ActorID    string  `gorm:"type:char(36);index" json:"actorId,omitempty"`
	ActorType  string  `gorm:"size:20;not null;default:'user'" json:"actorType"`
	ActorName  string  `gorm:"size:200" json:"actorName,omitempty"`
	Summary    string  `gorm:"size:400" json:"summary,omitempty"`
	FromValue  string  `gorm:"size:200" json:"fromValue,omitempty"`
	ToValue    string  `gorm:"size:200" json:"toValue,omitempty"`
	Detail     JSONMap `gorm:"type:text" json:"detail,omitempty"`
	CitizenVis bool    `gorm:"column:citizen_visible;not null;default:false" json:"citizenVisible"`
}

// Request link kinds.
const (
	LinkDuplicateOf = "duplicate_of"
	LinkRelatedTo   = "related_to"
	LinkChildOf     = "child_of"
)

// RequestLink joins two requests. Municipal workloads produce many reports of
// one pothole; linking them lets the crew work once while every reporter stays
// on the notification list.
type RequestLink struct {
	Base
	RequestID  string `gorm:"type:char(36);index;not null;uniqueIndex:idx_link_unique" json:"requestId"`
	TargetID   string `gorm:"type:char(36);index;not null;uniqueIndex:idx_link_unique" json:"targetId"`
	Kind       string `gorm:"size:30;not null;uniqueIndex:idx_link_unique" json:"kind"`
	CreatedBy  string `gorm:"type:char(36)" json:"createdBy,omitempty"`
	Note       string `gorm:"size:400" json:"note,omitempty"`
	TargetRef  string `gorm:"-" json:"targetRef,omitempty"`
	TargetSubj string `gorm:"-" json:"targetSubject,omitempty"`
}

// Attachment is a file stored against a request.
type Attachment struct {
	Base
	RequestID    string `gorm:"type:char(36);index;not null" json:"requestId"`
	Filename     string `gorm:"size:255;not null" json:"filename"`
	ContentType  string `gorm:"size:120;not null" json:"contentType"`
	SizeBytes    int64  `gorm:"not null" json:"sizeBytes"`
	StoragePath  string `gorm:"size:400;not null" json:"-"`
	Checksum     string `gorm:"size:64;index" json:"checksum"`
	UploadedByID string `gorm:"type:char(36);index" json:"uploadedById,omitempty"`
	Visibility   CommentVisibility `gorm:"size:20;not null;default:'internal'" json:"visibility"`
	ScanStatus   string `gorm:"size:20;not null;default:'pending'" json:"scanStatus"` // pending | clean | infected | skipped
	ScanNote     string `gorm:"size:400" json:"scanNote,omitempty"`
}

// ReferenceCounter backs the human-readable request reference. A dedicated row
// per year keeps references sequential and gap-free without scanning requests.
type ReferenceCounter struct {
	Year  int    `gorm:"primaryKey" json:"year"`
	Kind  string `gorm:"size:20;primaryKey" json:"kind"`
	Value uint64 `gorm:"not null" json:"value"`
}

// TableName names the reference counter table.
func (ReferenceCounter) TableName() string { return "reference_counters" }
