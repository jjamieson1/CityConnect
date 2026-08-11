package domain

// AllModels returns every persisted model, in dependency order, for
// AutoMigrate. Production uses versioned SQL migrations instead; AutoMigrate is
// a development and test convenience only.
func AllModels() []any {
	return []any{
		// Organisation and access
		&Department{},
		&User{},
		&Session{},
		&CitizenSession{},
		&LoginFlow{},
		&ApiToken{},
		&ConnectedSystem{},
		&AuditLog{},
		&IdempotencyKey{},

		// Contacts
		&Contact{},
		&ContactIdentity{},
		&ContactChannel{},
		&ContactGroup{},
		&ContactGroupMember{},
		&ConsentPreference{},
		&MergeRecord{},
		&Interaction{},

		// Catalogue
		&BusinessCalendar{},
		&SLAPolicy{},
		&ServiceType{},
		&NotificationTemplate{},
		&Macro{},
		&SavedView{},
		&RetentionPolicy{},

		// Routing
		&Queue{},
		&QueueMember{},
		&QueueSystem{},
		&RoutingRule{},

		// Requests
		&Request{},
		&RequestComment{},
		&RequestEvent{},
		&RequestLink{},
		&Attachment{},
		&ReferenceCounter{},

		// Communications and reporting
		&NotificationOutbox{},
		&WebhookDelivery{},
		&CalloutLog{},
		&ReportRollup{},
	}
}

// FullTextIndexes are MariaDB FULLTEXT indexes created after AutoMigrate.
// GORM cannot express these portably, and they are what make the global search
// usable without a separate search service.
var FullTextIndexes = []struct {
	Name    string
	Table   string
	Columns string
}{
	{"ft_requests_search", "requests", "subject, description"},
	{"ft_contacts_search", "contacts", "display_name, organization, notes"},
	{"ft_comments_search", "request_comments", "body"},
}
