package domain

import "time"

// Role is a staff user's coarse authority level. Fine-grained checks combine
// the role with the user's department scope.
type Role string

// Roles, ordered from least to most authority.
const (
	RoleReadOnly   Role = "readonly"
	RoleAgent      Role = "agent"
	RoleSupervisor Role = "supervisor"
	RoleAdmin      Role = "admin"
)

// Rank orders roles so that comparisons are possible without a lookup table.
func (r Role) Rank() int {
	switch r {
	case RoleReadOnly:
		return 1
	case RoleAgent:
		return 2
	case RoleSupervisor:
		return 3
	case RoleAdmin:
		return 4
	}
	return 0
}

// Valid reports whether the role is one of the known roles.
func (r Role) Valid() bool { return r.Rank() > 0 }

// UserStatus tracks a staff account through its lifecycle. A user may be
// invited before they have ever authenticated; the invite binds to a C2
// subject on first successful login.
type UserStatus string

// User statuses.
const (
	UserInvited   UserStatus = "invited"
	UserActive    UserStatus = "active"
	UserSuspended UserStatus = "suspended"
)

// Department is a soft organisational boundary inside the one municipality:
// Public Works, Bylaw, Water. It scopes queues, service types and users, and
// supplies the contact block shown to citizens on Service Cards. It is
// deliberately not a tenant — contacts are city-wide, and cross-department
// transfer is a first-class action.
type Department struct {
	Base
	Name     string `gorm:"size:160;not null" json:"name"`
	Code     string `gorm:"size:40;uniqueIndex;not null" json:"code"`
	ParentID string `gorm:"type:char(36);index" json:"parentId,omitempty"`

	DefaultQueueID string `gorm:"type:char(36);index" json:"defaultQueueId,omitempty"`

	// Contact details surfaced in Service Card callout responses.
	ContactEmail   string `gorm:"size:200" json:"contactEmail,omitempty"`
	ContactPhone   string `gorm:"size:60" json:"contactPhone,omitempty"`
	Address1       string `gorm:"size:200" json:"address1,omitempty"`
	Address2       string `gorm:"size:200" json:"address2,omitempty"`
	City           string `gorm:"size:120" json:"city,omitempty"`
	State          string `gorm:"size:60" json:"state,omitempty"`
	PostalCode     string `gorm:"size:20" json:"postalCode,omitempty"`
	TimeZone       string `gorm:"size:60" json:"timeZone,omitempty"`
	Active         bool   `gorm:"not null;default:true" json:"active"`
	SortOrder      int    `gorm:"not null;default:0" json:"sortOrder"`
	Description    string `gorm:"type:text" json:"description,omitempty"`
	PublicName     string `gorm:"size:160" json:"publicName,omitempty"`
	EscalationMail string `gorm:"size:200" json:"escalationEmail,omitempty"`
}

// User is a staff member. C2 SSO is the only staff login, so C2Sub is the
// permanent identity link — but it is nullable because an admin can invite a
// colleague by email before that person has ever signed in.
type User struct {
	Base
	C2Sub        string     `gorm:"size:255;uniqueIndex" json:"c2Sub,omitempty"`
	Email        string     `gorm:"size:255;uniqueIndex;not null" json:"email"`
	Name         string     `gorm:"size:200" json:"name"`
	Title        string     `gorm:"size:160" json:"title,omitempty"`
	Phone        string     `gorm:"size:60" json:"phone,omitempty"`
	Status       UserStatus `gorm:"size:20;not null;default:'invited';index" json:"status"`
	Role         Role       `gorm:"size:20;not null;default:'agent';index" json:"role"`
	DepartmentID string     `gorm:"type:char(36);index" json:"departmentId,omitempty"`

	// CrossDepartment grants read/write beyond the user's own department.
	// Supervisors and admins get it implicitly; it can also be granted
	// individually to a shared-services agent.
	CrossDepartment bool `gorm:"not null;default:false" json:"crossDepartment"`

	InvitedByID string     `gorm:"type:char(36)" json:"invitedById,omitempty"`
	InvitedAt   *time.Time `json:"invitedAt,omitempty"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`

	Department *Department `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
	Queues     []Queue     `gorm:"many2many:queue_members" json:"queues,omitempty"`
}

// CanCrossDepartment reports whether the user may act outside their own
// department.
func (u *User) CanCrossDepartment() bool {
	return u.CrossDepartment || u.Role.Rank() >= RoleSupervisor.Rank()
}

// Session is a server-side staff session. Sessions are indexed by C2 subject
// because C2's back-channel logout token identifies the *user*, not one
// session — there is no `sid` — so a logout must terminate every session that
// subject holds.
type Session struct {
	Base
	TokenHash string    `gorm:"size:64;uniqueIndex;not null" json:"-"`
	UserID    string    `gorm:"type:char(36);index;not null" json:"userId"`
	C2Sub     string    `gorm:"size:255;index;not null" json:"c2Sub"`
	ExpiresAt time.Time `gorm:"index;not null" json:"expiresAt"`
	LastSeen  time.Time `json:"lastSeen"`
	UserAgent string    `gorm:"size:400" json:"userAgent,omitempty"`
	IP        string    `gorm:"size:64" json:"ip,omitempty"`

	// IDTokenHint is retained so RP-initiated logout can pass id_token_hint to
	// C2's end_session endpoint.
	IDTokenHint string `gorm:"type:text" json:"-"`

	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
	RevokeReason string     `gorm:"size:80" json:"revokeReason,omitempty"`
}

// Active reports whether the session may still be used.
func (s *Session) Active(now time.Time, idleTTL time.Duration) bool {
	if s.RevokedAt != nil || now.After(s.ExpiresAt) {
		return false
	}
	return idleTTL <= 0 || now.Sub(s.LastSeen) <= idleTTL
}

// LoginFlow is a pending authorization-code exchange. The verifier, state and
// nonce live server-side for the duration of the redirect round-trip.
type LoginFlow struct {
	Base
	State        string    `gorm:"size:64;uniqueIndex;not null" json:"-"`
	Nonce        string    `gorm:"size:64;not null" json:"-"`
	CodeVerifier string    `gorm:"size:128;not null" json:"-"`
	ReturnTo     string    `gorm:"size:400" json:"-"`
	Silent       bool      `gorm:"not null;default:false" json:"-"`
	ExpiresAt    time.Time `gorm:"index;not null" json:"-"`
}

// ApiToken is a personal access token for API clients and connected systems.
// Only the SHA-256 hash is stored; the plaintext `cc_pat_…` value is shown
// once at creation.
type ApiToken struct {
	Base
	Name        string     `gorm:"size:160;not null" json:"name"`
	Prefix      string     `gorm:"size:16;index;not null" json:"prefix"`
	TokenHash   string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	OwnerUserID string     `gorm:"type:char(36);index" json:"ownerUserId,omitempty"`
	SystemID    string     `gorm:"type:char(36);index" json:"systemId,omitempty"`
	Scopes      StringList `gorm:"type:text" json:"scopes"`
	ReadOnly    bool       `gorm:"not null;default:false" json:"readOnly"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

// Usable reports whether the token may authenticate a request.
func (t *ApiToken) Usable(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	return t.ExpiresAt == nil || now.Before(*t.ExpiresAt)
}

// ConnectedSystem is a non-human agent: a line-of-business application such as
// the permitting system. It can hold queue membership and be assigned requests
// exactly as a person can, and it receives event webhooks.
type ConnectedSystem struct {
	Base
	Name          string     `gorm:"size:160;not null" json:"name"`
	Code          string     `gorm:"size:40;uniqueIndex;not null" json:"code"`
	Description   string     `gorm:"type:text" json:"description,omitempty"`
	DepartmentID  string     `gorm:"type:char(36);index" json:"departmentId,omitempty"`
	BaseURL       string     `gorm:"size:400" json:"baseUrl,omitempty"`
	WebhookURL    string     `gorm:"size:400" json:"webhookUrl,omitempty"`
	WebhookSecret string     `gorm:"size:128" json:"-"`
	WebhookEvents StringList `gorm:"type:text" json:"webhookEvents"`
	ContactEmail  string     `gorm:"size:200" json:"contactEmail,omitempty"`
	Active        bool       `gorm:"not null;default:true" json:"active"`

	Queues []Queue `gorm:"many2many:queue_systems" json:"queues,omitempty"`
}

// AuditLog is an append-only record of every consequential action. Entries are
// hash-chained: each row commits to the previous row's hash, so a deletion or
// edit inside the chain is detectable. C2 keeps a tamper-evident log of every
// notification it accepts from us; matching that on our side makes the two
// reconcilable.
type AuditLog struct {
	Base
	// Seq is assigned by the application, not the database. A database
	// auto-increment on a non-primary-key column is not portable — SQLite
	// silently leaves it zero — and a chain whose order cannot be reproduced
	// is not verifiable. The audit service assigns it under the same lock
	// that serialises appends.
	Seq        uint64  `gorm:"uniqueIndex;not null" json:"seq"`
	ActorType  string  `gorm:"size:20;index;not null" json:"actorType"` // user | system | c2 | job
	ActorID    string  `gorm:"type:char(36);index" json:"actorId,omitempty"`
	ActorLabel string  `gorm:"size:200" json:"actorLabel,omitempty"`
	Action     string  `gorm:"size:80;index;not null" json:"action"`
	TargetType string  `gorm:"size:60;index" json:"targetType,omitempty"`
	TargetID   string  `gorm:"type:char(36);index" json:"targetId,omitempty"`
	Summary    string  `gorm:"size:400" json:"summary,omitempty"`
	Changes    JSONMap `gorm:"type:text" json:"changes,omitempty"`
	IP         string  `gorm:"size:64" json:"ip,omitempty"`
	RequestID  string  `gorm:"size:64;index" json:"requestId,omitempty"`
	PrevHash   string  `gorm:"size:64" json:"prevHash,omitempty"`
	Hash       string  `gorm:"size:64;index" json:"hash,omitempty"`
}

// IdempotencyKey stores the outcome of a partner POST so a retry with the same
// key returns the original response instead of creating a duplicate request.
type IdempotencyKey struct {
	Base
	ClientKey    string    `gorm:"size:128;index;not null" json:"clientKey"`
	Key          string    `gorm:"size:200;not null" json:"key"`
	Fingerprint  string    `gorm:"size:64;not null" json:"-"`
	StatusCode   int       `gorm:"not null" json:"statusCode"`
	ResponseBody string    `gorm:"type:text" json:"-"`
	TargetType   string    `gorm:"size:60" json:"targetType,omitempty"`
	TargetID     string    `gorm:"type:char(36)" json:"targetId,omitempty"`
	ExpiresAt    time.Time `gorm:"index;not null" json:"expiresAt"`
}

// TableName pins the composite-unique table name for idempotency records.
func (IdempotencyKey) TableName() string { return "idempotency_keys" }
