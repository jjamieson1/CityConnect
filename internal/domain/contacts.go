package domain

import "time"

// Identity providers that can link an external record to a contact.
const (
	ProviderC2 = "c2"
)

// ContactStatus tracks whether a contact record is in active use.
type ContactStatus string

// Contact statuses. Merged contacts are retained (soft) so their id keeps
// resolving to the survivor.
const (
	ContactActive   ContactStatus = "active"
	ContactInactive ContactStatus = "inactive"
	ContactMerged   ContactStatus = "merged"
)

// Contact is a citizen or business the city deals with. It is deliberately not
// keyed on a C2 subject: a walk-in or a phone caller with no C2 account is
// still a contact, and one contact may hold several external identities.
// Contacts are city-wide — they belong to the municipality, not a department.
type Contact struct {
	Base
	DisplayName  string        `gorm:"size:250;index" json:"displayName"`
	GivenName    string        `gorm:"size:120" json:"givenName,omitempty"`
	FamilyName   string        `gorm:"size:120" json:"familyName,omitempty"`
	Organization string        `gorm:"size:200" json:"organization,omitempty"`
	Status       ContactStatus `gorm:"size:20;not null;default:'active';index" json:"status"`

	PrimaryEmail string `gorm:"size:255;index" json:"primaryEmail,omitempty"`
	PrimaryPhone string `gorm:"size:60;index" json:"primaryPhone,omitempty"`

	PreferredLanguage string `gorm:"size:12;not null;default:'en'" json:"preferredLanguage"`
	PreferredChannel  string `gorm:"size:20" json:"preferredChannel,omitempty"`

	// DoNotContact suppresses every outbound message regardless of channel
	// preferences. It is a hard stop, checked before the C2 consent gate.
	DoNotContact bool `gorm:"not null;default:false" json:"doNotContact"`

	// C2Reachable is maintained from C2's notification responses: a 403 (no
	// active consent) or 404 (unknown sub) clears it so agents can see at a
	// glance that this citizen must be reached by phone or mail instead.
	C2Reachable       bool       `gorm:"not null;default:true" json:"c2Reachable"`
	C2UnreachableCode string     `gorm:"size:40" json:"c2UnreachableCode,omitempty"`
	C2CheckedAt       *time.Time `json:"c2CheckedAt,omitempty"`

	Address1   string  `gorm:"size:200" json:"address1,omitempty"`
	Address2   string  `gorm:"size:200" json:"address2,omitempty"`
	City       string  `gorm:"size:120" json:"city,omitempty"`
	State      string  `gorm:"size:60" json:"state,omitempty"`
	PostalCode string  `gorm:"size:20;index" json:"postalCode,omitempty"`
	Ward       string  `gorm:"size:60;index" json:"ward,omitempty"`
	Latitude   float64 `json:"latitude,omitempty"`
	Longitude  float64 `json:"longitude,omitempty"`

	Notes        string     `gorm:"type:text" json:"notes,omitempty"`
	Tags         StringList `gorm:"type:text" json:"tags"`
	CustomFields JSONMap    `gorm:"type:text" json:"customFields"`

	// MergedIntoID points at the survivor when this record lost a merge.
	MergedIntoID string `gorm:"type:char(36);index" json:"mergedIntoId,omitempty"`

	Version uint `gorm:"not null;default:1" json:"version"`

	Identities []ContactIdentity `gorm:"foreignKey:ContactID" json:"identities,omitempty"`
	Channels   []ContactChannel  `gorm:"foreignKey:ContactID" json:"channels,omitempty"`
	Groups     []ContactGroup    `gorm:"many2many:contact_group_members" json:"groups,omitempty"`
}

// ContactIdentity links a contact to a record in an external system. The C2
// `sub` lands here rather than on Contact itself, which is what lets a merge
// keep every identity link and lets one person carry a C2 subject plus a
// permitting-system id.
type ContactIdentity struct {
	Base
	ContactID  string     `gorm:"type:char(36);index;not null" json:"contactId"`
	Provider   string     `gorm:"size:40;not null;uniqueIndex:idx_identity_provider_external" json:"provider"`
	ExternalID string     `gorm:"size:255;not null;uniqueIndex:idx_identity_provider_external" json:"externalId"`
	Label      string     `gorm:"size:160" json:"label,omitempty"`
	Verified   bool       `gorm:"not null;default:false" json:"verified"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	Metadata   JSONMap    `gorm:"type:text" json:"metadata,omitempty"`
}

// ChannelKind enumerates the ways a contact can be reached.
type ChannelKind string

// Channel kinds.
const (
	ChannelEmail   ChannelKind = "email"
	ChannelPhone   ChannelKind = "phone"
	ChannelSMS     ChannelKind = "sms"
	ChannelAddress ChannelKind = "address"
)

// ContactChannel is one addressable endpoint for a contact.
type ContactChannel struct {
	Base
	ContactID string      `gorm:"type:char(36);index;not null" json:"contactId"`
	Kind      ChannelKind `gorm:"size:20;not null;index" json:"kind"`
	Value     string      `gorm:"size:400;not null;index" json:"value"`
	Label     string      `gorm:"size:80" json:"label,omitempty"`
	Verified  bool        `gorm:"not null;default:false" json:"verified"`
	IsPrimary bool        `gorm:"not null;default:false" json:"isPrimary"`
	Notes     string      `gorm:"size:400" json:"notes,omitempty"`
}

// ContactGroup is a named collection such as "Ward 3" or "Snow route A".
type ContactGroup struct {
	Base
	Name        string `gorm:"size:160;uniqueIndex;not null" json:"name"`
	Description string `gorm:"type:text" json:"description,omitempty"`
	Kind        string `gorm:"size:40;index" json:"kind,omitempty"`
	Active      bool   `gorm:"not null;default:true" json:"active"`
	MemberCount int    `gorm:"-" json:"memberCount,omitempty"`
}

// ContactGroupMember joins contacts to groups.
type ContactGroupMember struct {
	ContactID    string    `gorm:"type:char(36);primaryKey" json:"contactId"`
	GroupID      string    `gorm:"type:char(36);primaryKey;column:contact_group_id" json:"groupId"`
	AddedAt      time.Time `json:"addedAt"`
	AddedByID    string    `gorm:"type:char(36)" json:"addedById,omitempty"`
	ContactGroup *ContactGroup
	Contact      *Contact
}

// TableName matches the join table GORM derives from the many2many tag.
func (ContactGroupMember) TableName() string { return "contact_group_members" }

// ConsentPurpose distinguishes what a contact has agreed to receive. This is
// CityConnect's own communication preference and is separate from C2 consent,
// which C2 owns and enforces on its side.
type ConsentPurpose string

// Consent purposes.
const (
	ConsentServiceUpdates ConsentPurpose = "service_updates"
	ConsentSurveys        ConsentPurpose = "surveys"
	ConsentAnnouncements  ConsentPurpose = "announcements"
)

// ConsentPreference records one contact's stance on one purpose over one
// channel, with the provenance needed to defend it later.
type ConsentPreference struct {
	Base
	ContactID string         `gorm:"type:char(36);index;not null;uniqueIndex:idx_consent_unique" json:"contactId"`
	Purpose   ConsentPurpose `gorm:"size:40;not null;uniqueIndex:idx_consent_unique" json:"purpose"`
	Channel   ChannelKind    `gorm:"size:20;not null;uniqueIndex:idx_consent_unique" json:"channel"`
	Granted   bool           `gorm:"not null" json:"granted"`
	Source    string         `gorm:"size:60" json:"source,omitempty"`
	Note      string         `gorm:"size:400" json:"note,omitempty"`
	SetByID   string         `gorm:"type:char(36)" json:"setById,omitempty"`
	SetAt     time.Time      `json:"setAt"`
}

// MergeRecord captures a contact merge in enough detail to undo it. Duplicate
// contacts are inevitable once several systems feed the CRM, and an
// irreversible merge is how a CRM loses data permanently.
type MergeRecord struct {
	Base
	SurvivorID string  `gorm:"type:char(36);index;not null" json:"survivorId"`
	MergedID   string  `gorm:"type:char(36);index;not null" json:"mergedId"`
	PerformedB string  `gorm:"type:char(36);column:performed_by_id" json:"performedById,omitempty"`
	FieldsKept JSONMap `gorm:"type:text" json:"fieldsKept,omitempty"`
	Snapshot   JSONMap `gorm:"type:text" json:"snapshot,omitempty"`
	Moved      JSONMap `gorm:"type:text" json:"moved,omitempty"`
	UndoneAt   *time.Time
	Note       string `gorm:"size:400" json:"note,omitempty"`
}

// InteractionKind enumerates how an interaction happened.
type InteractionKind string

// Interaction kinds.
const (
	InteractionCall    InteractionKind = "call"
	InteractionEmail   InteractionKind = "email"
	InteractionMeeting InteractionKind = "meeting"
	InteractionSMS     InteractionKind = "sms"
	InteractionPortal  InteractionKind = "portal"
	InteractionNote    InteractionKind = "note"
	InteractionWalkIn  InteractionKind = "walk_in"
)

// Direction distinguishes inbound from outbound communication.
type Direction string

// Directions.
const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
	DirectionInternal Direction = "internal"
)

// Interaction is a logged touchpoint with a contact — a call taken, an email
// sent, a counter visit. It may or may not relate to a service request.
type Interaction struct {
	Base
	ContactID       string          `gorm:"type:char(36);index;not null" json:"contactId"`
	RequestID       string          `gorm:"type:char(36);index" json:"requestId,omitempty"`
	UserID          string          `gorm:"type:char(36);index" json:"userId,omitempty"`
	DepartmentID    string          `gorm:"type:char(36);index" json:"departmentId,omitempty"`
	Kind            InteractionKind `gorm:"size:20;not null;index" json:"kind"`
	Direction       Direction       `gorm:"size:20;not null;default:'inbound'" json:"direction"`
	Subject         string          `gorm:"size:250" json:"subject,omitempty"`
	Summary         string          `gorm:"type:text" json:"summary,omitempty"`
	OccurredAt      time.Time       `gorm:"index;not null" json:"occurredAt"`
	DurationSeconds int             `json:"durationSeconds,omitempty"`
	Outcome         string          `gorm:"size:80" json:"outcome,omitempty"`
	Tags            StringList      `gorm:"type:text" json:"tags"`

	Contact *Contact `gorm:"foreignKey:ContactID" json:"contact,omitempty"`
	User    *User    `gorm:"foreignKey:UserID" json:"user,omitempty"`
}
