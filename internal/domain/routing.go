package domain

import "time"

// Queue kinds.
const (
	QueueKindHuman  = "human"
	QueueKindSystem = "system"
)

// Assignment strategies for pulling work out of a queue.
const (
	AssignManual      = "manual"
	AssignRoundRobin  = "round_robin"
	AssignLeastLoaded = "least_loaded"
)

// Queue is a pool of work owned by a department. Members may be staff users or
// connected systems, so a queue can be serviced by people, by an integration,
// or by both.
type Queue struct {
	Base
	Name         string `gorm:"size:160;not null" json:"name"`
	Code         string `gorm:"size:60;uniqueIndex;not null" json:"code"`
	Description  string `gorm:"type:text" json:"description,omitempty"`
	DepartmentID string `gorm:"type:char(36);index" json:"departmentId,omitempty"`
	Kind         string `gorm:"size:20;not null;default:'human'" json:"kind"`

	AssignmentStrategy string `gorm:"size:30;not null;default:'manual'" json:"assignmentStrategy"`
	CalendarID         string `gorm:"type:char(36);index" json:"calendarId,omitempty"`
	EscalationQueueID  string `gorm:"type:char(36);index" json:"escalationQueueId,omitempty"`

	// RoundRobinCursor persists the rotation position so restarts do not reset
	// distribution to the first member.
	RoundRobinCursor int  `gorm:"not null;default:0" json:"-"`
	Active           bool `gorm:"not null;default:true" json:"active"`
	SortOrder        int  `gorm:"not null;default:0" json:"sortOrder"`

	Department *Department       `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
	Members    []User            `gorm:"many2many:queue_members" json:"members,omitempty"`
	Systems    []ConnectedSystem `gorm:"many2many:queue_systems" json:"systems,omitempty"`

	OpenCount int `gorm:"-" json:"openCount,omitempty"`
}

// RoutingRule assigns incoming requests to a queue, owner and priority. Rules
// are evaluated in Priority order on creation and on re-triage.
//
// Conditions are a small, typed predicate set rather than an expression
// language: a routing DSL is the kind of thing that becomes unmaintainable and
// unauditable within a year.
type RoutingRule struct {
	Base
	Name        string `gorm:"size:160;not null" json:"name"`
	Description string `gorm:"size:400" json:"description,omitempty"`
	Priority    int    `gorm:"not null;default:100;index" json:"priority"`
	Active      bool   `gorm:"not null;default:true;index" json:"active"`

	// Continue lets a matching rule fall through to later rules instead of
	// stopping evaluation. Default behaviour is first-match-wins.
	Continue bool `gorm:"not null;default:false" json:"continue"`

	// Conditions holds {"all":[Condition,…]} and/or {"any":[Condition,…]}.
	Conditions JSONMap `gorm:"type:text" json:"conditions"`
	// Actions holds the RuleActions shape below.
	Actions JSONMap `gorm:"type:text" json:"actions"`

	MatchCount  int        `gorm:"not null;default:0" json:"matchCount"`
	LastMatched *time.Time `json:"lastMatchedAt,omitempty"`
}

// Condition is one typed predicate in a routing rule.
type Condition struct {
	Field string   `json:"field"` // service_type | category | priority | source | ward | postal_code | subject | description | tag | department | form.<key>
	Op    string   `json:"op"`    // eq | neq | in | not_in | contains | not_contains | starts_with | gt | lt | exists | not_exists
	Value string   `json:"value,omitempty"`
	List  []string `json:"list,omitempty"`
}

// RuleActions is what a matching rule does.
type RuleActions struct {
	QueueID      string   `json:"queueId,omitempty"`
	AssigneeID   string   `json:"assigneeUserId,omitempty"`
	SystemID     string   `json:"assigneeSystemId,omitempty"`
	DepartmentID string   `json:"departmentId,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	SLAPolicyID  string   `json:"slaPolicyId,omitempty"`
	AddTags      []string `json:"addTags,omitempty"`
	Notify       bool     `json:"notify,omitempty"`
	SetStatus    string   `json:"setStatus,omitempty"`
}

// QueueMember is the staff side of a queue's membership.
type QueueMember struct {
	QueueID  string    `gorm:"type:char(36);primaryKey;column:queue_id" json:"queueId"`
	UserID   string    `gorm:"type:char(36);primaryKey;column:user_id" json:"userId"`
	JoinedAt time.Time `json:"joinedAt"`
}

// TableName matches the many2many join GORM derives from Queue.Members.
func (QueueMember) TableName() string { return "queue_members" }

// QueueSystem is the connected-system side of a queue's membership.
type QueueSystem struct {
	QueueID           string    `gorm:"type:char(36);primaryKey;column:queue_id" json:"queueId"`
	ConnectedSystemID string    `gorm:"type:char(36);primaryKey;column:connected_system_id" json:"systemId"`
	JoinedAt          time.Time `json:"joinedAt"`
}

// TableName matches the many2many join GORM derives from Queue.Systems.
func (QueueSystem) TableName() string { return "queue_systems" }
