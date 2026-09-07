package domain

import "time"

// CollectionNotice is the wording shown to a resident at the moment their
// personal information is collected.
//
// PIPEDA requires the purposes to be identified before or at collection, and
// the OPC's meaningful-consent guidance requires them to be legible *there* —
// not in a privacy policy behind a link nobody opens. A municipality's legal
// wording is theirs, so this is configuration rather than a string in our
// source.
//
// Versions are immutable. Editing the text in place would rewrite what past
// submissions were shown, which is exactly the record the requirement exists to
// preserve: "we told them" is worth nothing if the telling can be changed
// afterwards. A change creates a new version and supersedes the old one.
type CollectionNotice struct {
	Base

	// ServiceTypeID scopes a notice to one service. Empty is the municipality's
	// default, used wherever a service has nothing more specific — which is
	// most of them, since a City usually has one collection statement.
	ServiceTypeID string `gorm:"type:char(36);index" json:"serviceTypeId,omitempty"`

	// Version increments per scope. Quoted back in the evidence record, so a
	// complaint years later can be answered with the text as it stood.
	Version int `gorm:"not null;default:1;index" json:"version"`

	Body string `gorm:"type:text;not null" json:"body"`

	// Superseded marks a version replaced by a later one. Kept rather than
	// deleted: the submissions that were shown it still point here.
	Superseded  bool      `gorm:"not null;default:false;index" json:"superseded"`
	EffectiveAt time.Time `gorm:"not null" json:"effectiveAt"`
}

// RequestNotice records which notice a submission was actually shown.
//
// The text is snapshotted rather than only referenced. A reference is enough
// while the notice row survives, and this record has to outlive it — the whole
// value is being able to reproduce, years later, the words a resident read
// before they typed their address. A few hundred bytes per request is nothing
// against a municipal volume, and it is the difference between evidence and a
// pointer to evidence.
type RequestNotice struct {
	Base
	RequestID string `gorm:"type:char(36);index;not null" json:"requestId"`

	NoticeID string `gorm:"type:char(36);index" json:"noticeId,omitempty"`
	Version  int    `gorm:"not null" json:"version"`
	Body     string `gorm:"type:text;not null" json:"body"`

	ShownAt time.Time `gorm:"not null" json:"shownAt"`
}
