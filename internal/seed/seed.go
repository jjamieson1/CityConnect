// Package seed installs the baseline configuration a fresh deployment needs
// to be usable: a default calendar, SLA policies, departments, queues,
// notification templates and a starter service catalogue.
//
// Every step is idempotent and keyed on a stable code, so it runs safely on
// every boot and never overwrites what an administrator has since changed.
package seed

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
)

// Run installs the baseline configuration.
func Run(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
	log = log.With("component", "seed")

	calendarID, err := seedCalendar(ctx, db)
	if err != nil {
		return err
	}
	policies, err := seedSLAPolicies(ctx, db, calendarID)
	if err != nil {
		return err
	}
	departments, err := seedDepartments(ctx, db)
	if err != nil {
		return err
	}
	queues, err := seedQueues(ctx, db, departments)
	if err != nil {
		return err
	}
	if err := seedServiceTypes(ctx, db, departments, queues, policies); err != nil {
		return err
	}
	if err := seedTemplates(ctx, db); err != nil {
		return err
	}
	if err := seedRetention(ctx, db); err != nil {
		return err
	}
	if err := seedMacros(ctx, db, departments); err != nil {
		return err
	}

	log.Info("baseline configuration verified",
		"departments", len(departments), "queues", len(queues))
	return nil
}

// firstOrCreate inserts a row only when nothing matches the lookup, so an
// administrator's later edits survive the next boot.
func firstOrCreate[T any](ctx context.Context, db *gorm.DB, where string, args []any, row *T) (bool, error) {
	var existing T
	err := db.WithContext(ctx).Where(where, args...).First(&existing).Error
	if err == nil {
		*row = existing
		return false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return false, err
	}
	return true, nil
}

func seedCalendar(ctx context.Context, db *gorm.DB) (string, error) {
	cal := domain.BusinessCalendar{
		Name: "City Hall business hours", TimeZone: "America/Toronto",
		Hours: catalog.DefaultHours(), IsDefault: true,
	}
	if _, err := firstOrCreate(ctx, db, "is_default = ?", []any{true}, &cal); err != nil {
		return "", err
	}

	// Emergency services run around the clock; a shared calendar would make
	// every after-hours water main break look compliant until Monday.
	always := domain.BusinessCalendar{
		Name: "Round the clock", TimeZone: "America/Toronto", AlwaysOpen: true,
	}
	if _, err := firstOrCreate(ctx, db, "always_open = ?", []any{true}, &always); err != nil {
		return "", err
	}
	return cal.ID, nil
}

func seedSLAPolicies(ctx context.Context, db *gorm.DB, calendarID string) (map[string]string, error) {
	pause := domain.StringList{
		string(domain.StatusWaitingCitizen),
		string(domain.StatusWaitingThirdParty),
	}

	defs := []struct {
		key      string
		name     string
		response int
		resolve  int
	}{
		{"standard", "Standard service", 8 * 60, 5 * 8 * 60},   // 1 day / 5 days
		{"urgent", "Urgent service", 60, 8 * 60},                // 1 hour / 1 day
		{"routine", "Routine service", 2 * 8 * 60, 20 * 8 * 60}, // 2 days / 20 days
	}

	out := map[string]string{}
	for _, d := range defs {
		p := domain.SLAPolicy{
			Name: d.name, CalendarID: calendarID,
			FirstResponseMinutes: d.response, ResolutionMinutes: d.resolve,
			PauseStatuses: pause, WarnAtPercent: 80, Active: true,
			// Priority overrides mean a critical report is not held to the
			// same clock as a routine one under the same policy.
			PriorityOverrides: domain.JSONMap{
				domain.PriorityCritical: map[string]any{
					"firstResponseMinutes": 30, "resolutionMinutes": 4 * 60,
				},
				domain.PriorityUrgent: map[string]any{
					"firstResponseMinutes": 60, "resolutionMinutes": 8 * 60,
				},
				domain.PriorityLow: map[string]any{
					"firstResponseMinutes": d.response * 2, "resolutionMinutes": d.resolve * 2,
				},
			},
		}
		if _, err := firstOrCreate(ctx, db, "name = ?", []any{d.name}, &p); err != nil {
			return nil, err
		}
		out[d.key] = p.ID
	}
	return out, nil
}

func seedDepartments(ctx context.Context, db *gorm.DB) (map[string]string, error) {
	defs := []domain.Department{
		{Code: "PW", Name: "Public Works", PublicName: "Public Works",
			ContactEmail: "publicworks@city.example", ContactPhone: "+1 555 0100",
			City: "Rivermont", State: "ON", SortOrder: 1, Active: true},
		{Code: "BYLAW", Name: "Bylaw Enforcement", PublicName: "Bylaw Services",
			ContactEmail: "bylaw@city.example", ContactPhone: "+1 555 0101",
			City: "Rivermont", State: "ON", SortOrder: 2, Active: true},
		{Code: "WATER", Name: "Water and Wastewater", PublicName: "Water Services",
			ContactEmail: "water@city.example", ContactPhone: "+1 555 0102",
			City: "Rivermont", State: "ON", SortOrder: 3, Active: true},
		{Code: "PARKS", Name: "Parks and Recreation", PublicName: "Parks and Recreation",
			ContactEmail: "parks@city.example", ContactPhone: "+1 555 0103",
			City: "Rivermont", State: "ON", SortOrder: 4, Active: true},
		{Code: "311", Name: "Customer Service", PublicName: "City of Rivermont",
			ContactEmail: "311@city.example", ContactPhone: "+1 555 0311",
			City: "Rivermont", State: "ON", SortOrder: 0, Active: true},
	}

	out := map[string]string{}
	for i := range defs {
		d := defs[i]
		if _, err := firstOrCreate(ctx, db, "code = ?", []any{d.Code}, &d); err != nil {
			return nil, err
		}
		out[d.Code] = d.ID
	}
	return out, nil
}

func seedQueues(ctx context.Context, db *gorm.DB, depts map[string]string) (map[string]string, error) {
	defs := []domain.Queue{
		{Code: "INTAKE", Name: "311 Intake", DepartmentID: depts["311"],
			AssignmentStrategy: domain.AssignManual, SortOrder: 0, Active: true, Kind: domain.QueueKindHuman},
		{Code: "ROADS", Name: "Roads and Sidewalks", DepartmentID: depts["PW"],
			AssignmentStrategy: domain.AssignLeastLoaded, SortOrder: 1, Active: true, Kind: domain.QueueKindHuman},
		{Code: "WASTE", Name: "Waste Collection", DepartmentID: depts["PW"],
			AssignmentStrategy: domain.AssignRoundRobin, SortOrder: 2, Active: true, Kind: domain.QueueKindHuman},
		{Code: "BYLAW-GEN", Name: "Bylaw Complaints", DepartmentID: depts["BYLAW"],
			AssignmentStrategy: domain.AssignRoundRobin, SortOrder: 3, Active: true, Kind: domain.QueueKindHuman},
		{Code: "WATER-OPS", Name: "Water Operations", DepartmentID: depts["WATER"],
			AssignmentStrategy: domain.AssignLeastLoaded, SortOrder: 4, Active: true, Kind: domain.QueueKindHuman},
		{Code: "PARKS-MAINT", Name: "Parks Maintenance", DepartmentID: depts["PARKS"],
			AssignmentStrategy: domain.AssignManual, SortOrder: 5, Active: true, Kind: domain.QueueKindHuman},
		{Code: "ESCALATION", Name: "Supervisor Escalation", DepartmentID: depts["311"],
			AssignmentStrategy: domain.AssignManual, SortOrder: 9, Active: true, Kind: domain.QueueKindHuman},
	}

	out := map[string]string{}
	for i := range defs {
		q := defs[i]
		if _, err := firstOrCreate(ctx, db, "code = ?", []any{q.Code}, &q); err != nil {
			return nil, err
		}
		out[q.Code] = q.ID
	}

	// Every operational queue escalates to the supervisor queue, so a breach
	// has somewhere to go rather than sitting where nobody is looking.
	escalation := out["ESCALATION"]
	for _, code := range []string{"ROADS", "WASTE", "BYLAW-GEN", "WATER-OPS", "PARKS-MAINT"} {
		if err := db.WithContext(ctx).Model(&domain.Queue{}).
			Where("code = ? AND (escalation_queue_id = '' OR escalation_queue_id IS NULL)", code).
			UpdateColumn("escalation_queue_id", escalation).Error; err != nil {
			return nil, err
		}
	}
	return out, nil
}

func seedServiceTypes(ctx context.Context, db *gorm.DB, depts, queues, policies map[string]string) error {
	defs := []domain.ServiceType{
		{
			Code: "POTHOLE", Name: "Pothole repair", Category: "Roads",
			Description:      "Report a pothole or damaged road surface.",
			DepartmentID:     depts["PW"], DefaultQueueID: queues["ROADS"],
			SLAPolicyID:      policies["standard"], DefaultPriority: domain.PriorityNormal,
			RequiresLocation: true, PublicVisible: true, Active: true, AllowsAttachments: true,
			IntakeForm: domain.JSONMap{"fields": []domain.FormField{
				{Key: "size", Label: "Approximate size", Type: "select",
					Options: []string{"Small (under 30cm)", "Medium", "Large (over 1m)"}, Required: true},
				{Key: "lane", Label: "Lane or location detail", Type: "text"},
				{Key: "hazard", Label: "Is it an immediate hazard?", Type: "checkbox"},
			}},
		},
		{
			Code: "MISSED-COLLECTION", Name: "Missed waste collection", Category: "Waste",
			Description:      "Report waste, recycling or organics that was not collected.",
			DepartmentID:     depts["PW"], DefaultQueueID: queues["WASTE"],
			SLAPolicyID:      policies["standard"], DefaultPriority: domain.PriorityNormal,
			RequiresLocation: true, PublicVisible: true, Active: true,
			IntakeForm: domain.JSONMap{"fields": []domain.FormField{
				{Key: "stream", Label: "Which stream", Type: "select",
					Options: []string{"Garbage", "Recycling", "Organics", "Yard waste"}, Required: true},
				{Key: "collectionDate", Label: "Scheduled collection date", Type: "date", Required: true},
			}},
		},
		{
			Code: "WATER-MAIN", Name: "Water main break", Category: "Water",
			Description:      "Report a suspected water main break or major leak.",
			DepartmentID:     depts["WATER"], DefaultQueueID: queues["WATER-OPS"],
			SLAPolicyID:      policies["urgent"], DefaultPriority: domain.PriorityUrgent,
			RequiresLocation: true, PublicVisible: true, Active: true,
			IntakeForm: domain.JSONMap{"fields": []domain.FormField{
				{Key: "flowing", Label: "Is water flowing onto the road?", Type: "checkbox"},
				{Key: "pressureLoss", Label: "Have you lost water pressure?", Type: "checkbox"},
			}},
		},
		{
			Code: "NOISE", Name: "Noise complaint", Category: "Bylaw",
			Description:  "Report a noise bylaw concern.",
			DepartmentID: depts["BYLAW"], DefaultQueueID: queues["BYLAW-GEN"],
			SLAPolicyID:  policies["standard"], DefaultPriority: domain.PriorityNormal,
			RequiresLocation: true, PublicVisible: true, Active: true,
			IntakeForm: domain.JSONMap{"fields": []domain.FormField{
				{Key: "noiseType", Label: "Type of noise", Type: "select",
					Options: []string{"Construction", "Music or party", "Vehicle", "Animal", "Other"}, Required: true},
				{Key: "ongoing", Label: "Is it happening now?", Type: "checkbox"},
				{Key: "times", Label: "When does it usually occur?", Type: "textarea"},
			}},
		},
		{
			Code: "PARK-MAINT", Name: "Park maintenance", Category: "Parks",
			Description:  "Report damaged or unsafe park equipment, litter or vandalism.",
			DepartmentID: depts["PARKS"], DefaultQueueID: queues["PARKS-MAINT"],
			SLAPolicyID:  policies["routine"], DefaultPriority: domain.PriorityLow,
			RequiresLocation: true, PublicVisible: true, Active: true,
			IntakeForm: domain.JSONMap{"fields": []domain.FormField{
				{Key: "parkName", Label: "Park name", Type: "text", Required: true},
				{Key: "issue", Label: "What needs attention?", Type: "select",
					Options: []string{"Playground equipment", "Litter", "Vandalism", "Trees or grounds", "Lighting"}},
			}},
		},
		{
			Code: "GENERAL", Name: "General enquiry", Category: "General",
			Description:  "Any request that does not fit another category.",
			DepartmentID: depts["311"], DefaultQueueID: queues["INTAKE"],
			SLAPolicyID:  policies["standard"], DefaultPriority: domain.PriorityNormal,
			PublicVisible: true, Active: true,
		},
	}

	for i := range defs {
		st := defs[i]
		if _, err := firstOrCreate(ctx, db, "code = ?", []any{st.Code}, &st); err != nil {
			return err
		}
	}
	return nil
}

func seedTemplates(ctx context.Context, db *gorm.DB) error {
	// Templates address the citizen plainly and always carry the reference,
	// because the reference is what they will quote back on the phone.
	defs := []domain.NotificationTemplate{
		{
			Event: domain.EventRequestCreated, Language: "en", Category: "BUSINESS",
			Subject: "We received your request {{.Reference}}",
			Body: "Hello {{.ContactFirst}},\n\n" +
				"We have received your request about {{.Subject}} and given it reference {{.Reference}}.\n\n" +
				"{{if .DueAt}}We aim to have it resolved by {{.DueAt}}.{{end}}\n\n" +
				"You can check its progress any time from your account.\n\n{{.CityName}}",
			ShortBody:   "{{.Reference}} received. We'll be in touch.",
			Description: "Sent when a request is opened.", Active: true,
		},
		{
			Event: domain.EventStatusChanged, Language: "en", Category: "BUSINESS",
			Subject: "Update on {{.Reference}}",
			Body: "Hello {{.ContactFirst}},\n\n" +
				"Your request {{.Reference}} ({{.Subject}}) is now {{.StatusLabel | lower}}.\n\n" +
				"{{if .Comment}}{{.Comment}}\n\n{{end}}{{.CityName}}",
			ShortBody:   "{{.Reference}} is now {{.StatusLabel | lower}}.",
			Description: "Sent when a request's status changes.", Active: true,
		},
		{
			Event: domain.EventRequestResolved, Language: "en", Category: "BUSINESS",
			Subject: "{{.Reference}} has been resolved",
			Body: "Hello {{.ContactFirst}},\n\n" +
				"Your request {{.Reference}} about {{.Subject}} has been resolved.\n\n" +
				"{{if .ResolutionNote}}{{.ResolutionNote}}\n\n{{end}}" +
				"If the problem has not been fixed, reply and we will reopen it.\n\n{{.CityName}}",
			ShortBody:   "{{.Reference}} resolved.",
			Description: "Sent when a request is resolved.", Active: true,
		},
		{
			Event: domain.EventCitizenComment, Language: "en", Category: "BUSINESS",
			Subject: "A note about {{.Reference}}",
			Body: "Hello {{.ContactFirst}},\n\n" +
				"{{.Comment}}\n\n" +
				"This relates to your request {{.Reference}} about {{.Subject}}.\n\n{{.CityName}}",
			ShortBody:   "Update on {{.Reference}}.",
			Description: "Sent when staff post a citizen-visible note.", Active: true,
		},
		{
			Event: domain.EventCSATSurvey, Language: "en", Category: "BUSINESS",
			Subject: "How did we do with {{.Reference}}?",
			Body: "Hello {{.ContactFirst}},\n\n" +
				"We recently completed your request {{.Reference}} about {{.Subject}}.\n\n" +
				"If you have a moment, we would appreciate your feedback on how it was handled.\n\n" +
				"{{.RequestURL}}\n\n{{.CityName}}",
			ShortBody:   "How did we do with {{.Reference}}?",
			Description: "Satisfaction survey sent after resolution.", Active: true,
		},
		{
			Event: domain.EventRequestAssigned, Language: "en", Category: "BUSINESS",
			Subject: "{{.Reference}} has been assigned",
			Body: "Hello {{.ContactFirst}},\n\n" +
				"Your request {{.Reference}} about {{.Subject}} has been assigned to " +
				"{{.Department | default \"our team\"}} and is being looked at.\n\n{{.CityName}}",
			ShortBody:   "{{.Reference}} assigned.",
			Description: "Sent when a request is assigned to an owner.", Active: true,
		},
	}

	for i := range defs {
		t := defs[i]
		if err := catalog.ValidateTemplate(&t); err != nil {
			return err
		}
		if _, err := firstOrCreate(ctx, db,
			"event = ? AND language = ? AND (service_type_id = '' OR service_type_id IS NULL)",
			[]any{t.Event, t.Language}, &t); err != nil {
			return err
		}
	}
	return nil
}

func seedRetention(ctx context.Context, db *gorm.DB) error {
	// Disabled by default. Destroying municipal records is a decision an
	// administrator makes deliberately, against their own retention schedule —
	// never something that starts happening because a default was left on.
	defs := []domain.RetentionPolicy{
		{Entity: "request", RetainMonths: 84, Action: "anonymize", Enabled: false,
			Description: "Closed service requests: anonymise after 7 years, keeping operational statistics."},
		{Entity: "contact", RetainMonths: 84, Action: "anonymize", Enabled: false,
			Description: "Contacts with no recent activity: anonymise after 7 years."},
		{Entity: "interaction", RetainMonths: 60, Action: "purge", Enabled: false,
			Description: "Interaction logs: delete after 5 years."},
		{Entity: "notification", RetainMonths: 24, Action: "purge", Enabled: false,
			Description: "Notification delivery log: delete after 2 years."},
		{Entity: "webhook_delivery", RetainMonths: 6, Action: "purge", Enabled: false,
			Description: "Webhook delivery log: delete after 6 months."},
		{Entity: "callout_log", RetainMonths: 3, Action: "purge", Enabled: false,
			Description: "Service Card callout log: delete after 3 months."},
	}

	for i := range defs {
		p := defs[i]
		if _, err := firstOrCreate(ctx, db, "entity = ?", []any{p.Entity}, &p); err != nil {
			return err
		}
	}
	return nil
}

func seedMacros(ctx context.Context, db *gorm.DB, depts map[string]string) error {
	defs := []domain.Macro{
		{
			Name: "Crew scheduled", Description: "Tell the citizen a crew is booked.",
			Body:       "A crew has been scheduled to attend. We will update you once the work is complete.",
			Visibility: string(domain.VisibilityCitizen), SetStatus: string(domain.StatusInProgress),
			NotifyCitzn: true, Active: true,
		},
		{
			Name: "Need more information", Description: "Ask the citizen for detail and pause the clock.",
			Body: "We need a little more information before we can proceed. " +
				"Could you reply with the exact location and, if possible, a photograph?",
			Visibility: string(domain.VisibilityCitizen), SetStatus: string(domain.StatusWaitingCitizen),
			NotifyCitzn: true, Active: true,
		},
		{
			Name: "Duplicate report", Description: "Close as a duplicate of existing work.",
			Body: "Thank you for reporting this. We already have this issue logged and " +
				"work is under way. We will let you know when it is complete.",
			Visibility: string(domain.VisibilityCitizen), NotifyCitzn: true,
			AddTags: domain.StringList{"duplicate"}, Active: true,
		},
		{
			Name: "Referred to another department", DepartmentID: depts["311"],
			Description: "Internal note when handing a request on.",
			Body:        "Referred to the appropriate department for action.",
			Visibility:  string(domain.VisibilityInternal), Active: true,
		},
		{
			Name: "Seasonal deferral", Description: "Work deferred until conditions allow.",
			Body: "This work has been added to our schedule but cannot be completed until " +
				"conditions allow. We will contact you when it is scheduled.",
			Visibility: string(domain.VisibilityCitizen), NotifyCitzn: true,
			AddTags: domain.StringList{"deferred", "seasonal"}, Active: true,
		},
	}

	for i := range defs {
		m := defs[i]
		if _, err := firstOrCreate(ctx, db, "name = ?", []any{m.Name}, &m); err != nil {
			return err
		}
	}
	return nil
}

// DemoData installs a sample contact and a handful of requests, for a local
// walkthrough. It is never run automatically.
//
// referencePrefix should be the deployment's configured prefix, so the sample
// request quotes a reference in the same shape the running system would issue.
func DemoData(ctx context.Context, db *gorm.DB, log *slog.Logger, referencePrefix string) error {
	var count int64
	db.WithContext(ctx).Model(&domain.Request{}).Count(&count)
	if count > 0 {
		log.Info("demo data skipped; requests already exist", "count", count)
		return nil
	}

	contact := domain.Contact{
		DisplayName: "Alex Citizen", GivenName: "Alex", FamilyName: "Citizen",
		PrimaryEmail: "alex.citizen@example.gov", PrimaryPhone: "+1 555 0190",
		Address1: "12 Oak Street", City: "Rivermont", State: "ON",
		PostalCode: "K1A 0B1", Ward: "Ward 3",
		Status: domain.ContactActive, C2Reachable: true, PreferredLanguage: "en",
	}
	if _, err := firstOrCreate(ctx, db, "primary_email = ?", []any{contact.PrimaryEmail}, &contact); err != nil {
		return err
	}

	// The demo subject matches the c2stub's default citizen, so a local
	// Service Card callout returns real data straight away.
	ident := domain.ContactIdentity{
		ContactID: contact.ID, Provider: domain.ProviderC2,
		ExternalID: "citizen-001", Verified: true,
	}
	if _, err := firstOrCreate(ctx, db, "provider = ? AND external_id = ?",
		[]any{domain.ProviderC2, "citizen-001"}, &ident); err != nil {
		return err
	}

	var pothole domain.ServiceType
	if err := db.WithContext(ctx).First(&pothole, "code = ?", "POTHOLE").Error; err != nil {
		return err
	}

	// A fixed reference rather than a drawn one, so re-running the demo seed
	// finds the row it made last time instead of adding another. It still
	// carries the shape a real reference has — the demo should not be the one
	// place a sequential-looking number survives.
	demoReference := requests.NormalizeReferencePrefix(referencePrefix) + "-DEM0-0001"

	now := time.Now().UTC()
	req := domain.Request{
		Reference: demoReference, ContactID: contact.ID,
		ServiceTypeID: pothole.ID, DepartmentID: pothole.DepartmentID,
		QueueID: pothole.DefaultQueueID, Source: domain.SourceAgent,
		Status: domain.StatusInProgress, Priority: domain.PriorityNormal,
		Subject: "Pothole on Oak Street", Description: "Large pothole outside number 12.",
		Address1: "12 Oak Street", City: "Rivermont", Ward: "Ward 3",
		OpenedAt: now.Add(-72 * time.Hour), LastActivityA: now.Add(-6 * time.Hour),
		SLAPolicyID: pothole.SLAPolicyID, Version: 1,
		FormData: domain.JSONMap{"size": "Large (over 1m)", "hazard": true},
	}
	created, err := firstOrCreate(ctx, db, "reference = ?", []any{req.Reference}, &req)
	if err != nil {
		return err
	}
	if created {
		if err := db.WithContext(ctx).Create(&domain.RequestComment{
			RequestID: req.ID, AuthorType: "user", AuthorName: "Demo Agent",
			Visibility: domain.VisibilityCitizen,
			Body:       "A crew has been scheduled to attend on Tuesday.",
		}).Error; err != nil {
			return err
		}
		if err := db.WithContext(ctx).Create(&domain.RequestEvent{
			RequestID: req.ID, Kind: domain.EvtCreated, ActorType: "user",
			ActorName: "Demo Agent", Summary: "request opened", CitizenVis: true,
		}).Error; err != nil {
			return err
		}
	}

	log.Info("demo data installed", "contact", contact.DisplayName, "request", req.Reference)
	return nil
}
