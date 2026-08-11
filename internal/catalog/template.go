package catalog

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// TemplateContext is everything a notification template may reference. It is a
// flat, documented struct rather than a raw map so an admin editing a template
// in the console can be shown exactly what is available, and so a typo fails
// at save time rather than silently rendering nothing.
type TemplateContext struct {
	Reference   string `json:"reference"`
	Subject     string `json:"subject"`
	Status      string `json:"status"`
	StatusLabel string `json:"statusLabel"`
	Priority    string `json:"priority"`
	ServiceType string `json:"serviceType"`
	Department  string `json:"department"`
	Assignee    string `json:"assignee"`
	Queue       string `json:"queue"`

	ContactName  string `json:"contactName"`
	ContactFirst string `json:"contactFirst"`

	Address string `json:"address"`
	Ward    string `json:"ward"`

	OpenedAt   string `json:"openedAt"`
	UpdatedAt  string `json:"updatedAt"`
	DueAt      string `json:"dueAt"`
	ResolvedAt string `json:"resolvedAt"`

	Comment        string `json:"comment"`
	ResolutionNote string `json:"resolutionNote"`

	CityName  string `json:"cityName"`
	PortalURL string `json:"portalUrl"`
	RequestURL string `json:"requestUrl"`
}

// StatusLabels are the citizen-facing names for internal statuses. A citizen
// should not be told their request is "waiting_third_party".
var StatusLabels = map[domain.RequestStatus]string{
	domain.StatusNew:               "Received",
	domain.StatusTriaged:           "Under review",
	domain.StatusAssigned:          "Assigned to a crew",
	domain.StatusInProgress:        "In progress",
	domain.StatusWaitingCitizen:    "Waiting for your reply",
	domain.StatusWaitingThirdParty: "Waiting on a third party",
	domain.StatusResolved:          "Resolved",
	domain.StatusClosed:            "Closed",
	domain.StatusCancelled:         "Cancelled",
}

// StatusLabel returns the citizen-facing label for a status.
func StatusLabel(s domain.RequestStatus) string {
	if label, ok := StatusLabels[s]; ok {
		return label
	}
	return strings.ReplaceAll(string(s), "_", " ")
}

// funcs are the helpers available inside a template.
var funcs = template.FuncMap{
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"title": func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	},
	"truncate": func(n int, s string) string {
		if len(s) <= n {
			return s
		}
		return s[:n-1] + "…"
	},
	"default": func(fallback, s string) string {
		if strings.TrimSpace(s) == "" {
			return fallback
		}
		return s
	},
}

// Rendered is the output of a template.
type Rendered struct {
	Subject   string
	Body      string
	ShortBody string
	Category  string
}

// Render fills a template from a context.
func Render(t *domain.NotificationTemplate, ctx TemplateContext) (*Rendered, error) {
	subject, err := renderOne("subject", t.Subject, ctx)
	if err != nil {
		return nil, err
	}
	body, err := renderOne("body", t.Body, ctx)
	if err != nil {
		return nil, err
	}
	short := ""
	if t.ShortBody != "" {
		if short, err = renderOne("shortBody", t.ShortBody, ctx); err != nil {
			return nil, err
		}
	}

	category := t.Category
	if category == "" {
		category = "BUSINESS"
	}

	return &Rendered{
		Subject:   strings.TrimSpace(subject),
		Body:      strings.TrimSpace(body),
		ShortBody: strings.TrimSpace(short),
		Category:  category,
	}, nil
}

func renderOne(name, tmpl string, ctx TemplateContext) (string, error) {
	// missingkey=error turns a typo into a save-time failure instead of a
	// notification that reaches a citizen with "<no value>" in it.
	t, err := template.New(name).Funcs(funcs).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("%w: template %s: %v", ErrInvalidInput, name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("%w: template %s: %v", ErrInvalidInput, name, err)
	}
	return buf.String(), nil
}

// ValidateTemplate compiles a template against a sample context so a broken
// one is rejected at save time rather than at send time, when the failure
// would sit silently in the outbox.
func ValidateTemplate(t *domain.NotificationTemplate) error {
	sample := TemplateContext{
		Reference: "SR-2026-000123", Subject: "Pothole on Oak Street",
		Status: "in_progress", StatusLabel: "In progress", Priority: "normal",
		ServiceType: "Pothole repair", Department: "Public Works",
		Assignee: "A. Agent", Queue: "Roads",
		ContactName: "Alex Citizen", ContactFirst: "Alex",
		Address: "12 Oak St", Ward: "Ward 3",
		OpenedAt: "6 August 2026", UpdatedAt: "8 August 2026",
		DueAt: "12 August 2026", ResolvedAt: "",
		Comment: "A crew is scheduled.", ResolutionNote: "",
		CityName: "Rivermont", PortalURL: "https://example.gov",
		RequestURL: "https://example.gov/requests/SR-2026-000123",
	}
	_, err := Render(t, sample)
	return err
}

// FormatDate renders a date for citizen-facing text.
func FormatDate(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format("2 January 2006")
}
