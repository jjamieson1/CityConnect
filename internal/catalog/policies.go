package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// ---------------------------------------------------------------------------
// SLA policies
// ---------------------------------------------------------------------------

// ListSLAPolicies returns the SLA policies.
func (s *Service) ListSLAPolicies(ctx context.Context) ([]domain.SLAPolicy, error) {
	var out []domain.SLAPolicy
	err := s.db.WithContext(ctx).Preload("Calendar").Order("name ASC").Find(&out).Error
	return out, store.Translate(err)
}

// GetSLAPolicy loads one policy.
func (s *Service) GetSLAPolicy(ctx context.Context, id string) (*domain.SLAPolicy, error) {
	var p domain.SLAPolicy
	err := s.db.WithContext(ctx).Preload("Calendar").First(&p, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &p, store.Translate(err)
}

// SaveSLAPolicy creates or updates an SLA policy.
func (s *Service) SaveSLAPolicy(ctx context.Context, actor audit.Actor, p *domain.SLAPolicy) (*domain.SLAPolicy, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("%w: policy name is required", ErrInvalidInput)
	}
	if p.FirstResponseMinutes <= 0 || p.ResolutionMinutes <= 0 {
		return nil, fmt.Errorf("%w: response and resolution targets must be positive", ErrInvalidInput)
	}
	if p.WarnAtPercent <= 0 || p.WarnAtPercent > 100 {
		p.WarnAtPercent = 80
	}
	if len(p.PauseStatuses) == 0 {
		// Without a pause the clock runs while the city waits on the citizen,
		// and every such request eventually breaches through no fault of staff.
		p.PauseStatuses = domain.StringList{
			string(domain.StatusWaitingCitizen),
			string(domain.StatusWaitingThirdParty),
		}
	}

	action := "sla_policy.created"
	if p.ID != "" {
		action = "sla_policy.updated"
	}
	if err := s.db.WithContext(ctx).Save(p).Error; err != nil {
		return nil, store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "sla_policy", TargetID: p.ID, Summary: p.Name,
	})
	return p, nil
}

// SLATargets are the deadlines computed for one request.
type SLATargets struct {
	ResponseDueAt time.Time
	DueAt         time.Time
	PolicyID      string
	WarnAt        time.Time
}

// ComputeTargets applies a policy to a start time, in business hours.
func (s *Service) ComputeTargets(ctx context.Context, policyID, priority string, from time.Time) (*SLATargets, error) {
	if policyID == "" {
		return nil, nil
	}
	policy, err := s.GetSLAPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}
	cal, err := s.CalendarFor(ctx, policy.CalendarID)
	if err != nil {
		return nil, err
	}

	responseMin, resolutionMin := policy.TargetsFor(priority)
	warnMin := resolutionMin * policy.WarnAtPercent / 100

	return &SLATargets{
		PolicyID:      policy.ID,
		ResponseDueAt: cal.Add(from, responseMin),
		DueAt:         cal.Add(from, resolutionMin),
		WarnAt:        cal.Add(from, warnMin),
	}, nil
}

// ---------------------------------------------------------------------------
// Business calendars
// ---------------------------------------------------------------------------

// ListCalendars returns the business calendars.
func (s *Service) ListCalendars(ctx context.Context) ([]domain.BusinessCalendar, error) {
	var out []domain.BusinessCalendar
	err := s.db.WithContext(ctx).Order("name ASC").Find(&out).Error
	return out, store.Translate(err)
}

// SaveCalendar creates or updates a calendar and drops the cached copy.
func (s *Service) SaveCalendar(ctx context.Context, actor audit.Actor, c *domain.BusinessCalendar) (*domain.BusinessCalendar, error) {
	if strings.TrimSpace(c.Name) == "" {
		return nil, fmt.Errorf("%w: calendar name is required", ErrInvalidInput)
	}
	if c.TimeZone == "" {
		c.TimeZone = "America/Toronto"
	}
	if _, err := time.LoadLocation(c.TimeZone); err != nil {
		return nil, fmt.Errorf("%w: unknown time zone %q", ErrInvalidInput, c.TimeZone)
	}
	// Reject a calendar that cannot be loaded rather than discovering it when
	// the SLA scheduler runs at 3am.
	if _, err := LoadCalendar(c); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	action := "calendar.created"
	if c.ID != "" {
		action = "calendar.updated"
	}
	if err := s.db.WithContext(ctx).Save(c).Error; err != nil {
		return nil, store.Translate(err)
	}

	s.mu.Lock()
	delete(s.calendars, c.ID)
	s.mu.Unlock()

	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "calendar", TargetID: c.ID, Summary: c.Name,
	})
	return c, nil
}

// CalendarFor returns a prepared calendar, falling back to the default and
// then to always-open.
func (s *Service) CalendarFor(ctx context.Context, id string) (*Calendar, error) {
	s.mu.RLock()
	if cal, ok := s.calendars[id]; ok {
		s.mu.RUnlock()
		return cal, nil
	}
	s.mu.RUnlock()

	var model domain.BusinessCalendar
	var err error
	if id != "" {
		err = s.db.WithContext(ctx).First(&model, "id = ?", id).Error
	} else {
		err = s.db.WithContext(ctx).Where("is_default = ?", true).First(&model).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return LoadCalendar(nil)
	}
	if err != nil {
		return nil, store.Translate(err)
	}

	cal, err := LoadCalendar(&model)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.calendars[id] = cal
	s.mu.Unlock()
	return cal, nil
}

// ---------------------------------------------------------------------------
// Notification templates
// ---------------------------------------------------------------------------

// ListTemplates returns the notification templates.
func (s *Service) ListTemplates(ctx context.Context) ([]domain.NotificationTemplate, error) {
	var out []domain.NotificationTemplate
	err := s.db.WithContext(ctx).Order("event ASC, language ASC").Find(&out).Error
	return out, store.Translate(err)
}

// SaveTemplate creates or updates a template.
func (s *Service) SaveTemplate(ctx context.Context, actor audit.Actor, t *domain.NotificationTemplate) (*domain.NotificationTemplate, error) {
	if t.Event == "" || strings.TrimSpace(t.Subject) == "" || strings.TrimSpace(t.Body) == "" {
		return nil, fmt.Errorf("%w: template event, subject and body are required", ErrInvalidInput)
	}
	if t.Language == "" {
		t.Language = "en"
	}
	if t.Category == "" {
		t.Category = "BUSINESS"
	}
	if err := ValidateTemplate(t); err != nil {
		return nil, err
	}

	action := "template.created"
	if t.ID != "" {
		action = "template.updated"
	}
	if err := s.db.WithContext(ctx).Save(t).Error; err != nil {
		return nil, store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "notification_template", TargetID: t.ID,
		Summary: t.Event + " (" + t.Language + ")",
	})
	return t, nil
}

// FindTemplate resolves the best template for an event, preferring one bound
// to the service type and falling back to the generic one, then to English.
func (s *Service) FindTemplate(ctx context.Context, event, serviceTypeID, language string) (*domain.NotificationTemplate, error) {
	if language == "" {
		language = "en"
	}

	candidates := []struct {
		serviceType string
		lang        string
	}{
		{serviceTypeID, language},
		{"", language},
		{serviceTypeID, "en"},
		{"", "en"},
	}

	for _, c := range candidates {
		var t domain.NotificationTemplate
		q := s.db.WithContext(ctx).Where("event = ? AND language = ? AND active = ?", event, c.lang, true)
		if c.serviceType != "" {
			q = q.Where("service_type_id = ?", c.serviceType)
		} else {
			q = q.Where("service_type_id = '' OR service_type_id IS NULL")
		}
		if err := q.First(&t).Error; err == nil {
			return &t, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteTemplate removes a template.
func (s *Service) DeleteTemplate(ctx context.Context, actor audit.Actor, id string) error {
	if err := s.db.WithContext(ctx).Delete(&domain.NotificationTemplate{}, "id = ?", id).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "template.deleted", TargetType: "notification_template", TargetID: id,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Macros
// ---------------------------------------------------------------------------

// ListMacros returns the macros available to a department.
func (s *Service) ListMacros(ctx context.Context, departmentID string) ([]domain.Macro, error) {
	q := s.db.WithContext(ctx).Model(&domain.Macro{}).Where("active = ?", true)
	if departmentID != "" {
		q = q.Where("department_id = ? OR department_id = '' OR department_id IS NULL", departmentID)
	}
	var out []domain.Macro
	err := q.Order("usage_count DESC, name ASC").Find(&out).Error
	return out, store.Translate(err)
}

// GetMacro loads one macro.
func (s *Service) GetMacro(ctx context.Context, id string) (*domain.Macro, error) {
	var m domain.Macro
	err := s.db.WithContext(ctx).First(&m, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &m, store.Translate(err)
}

// SaveMacro creates or updates a macro.
func (s *Service) SaveMacro(ctx context.Context, actor audit.Actor, m *domain.Macro) (*domain.Macro, error) {
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("%w: macro name is required", ErrInvalidInput)
	}
	if m.Visibility == "" {
		m.Visibility = string(domain.VisibilityInternal)
	}
	m.AddTags = m.AddTags.Normalized()

	action := "macro.created"
	if m.ID != "" {
		action = "macro.updated"
	}
	if err := s.db.WithContext(ctx).Save(m).Error; err != nil {
		return nil, store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "macro", TargetID: m.ID, Summary: m.Name,
	})
	return m, nil
}

// RecordMacroUse increments the usage counter that orders the macro picker.
func (s *Service) RecordMacroUse(ctx context.Context, id string) {
	s.db.WithContext(ctx).Model(&domain.Macro{}).Where("id = ?", id).
		UpdateColumn("usage_count", gorm.Expr("usage_count + 1"))
}

// DeleteMacro removes a macro.
func (s *Service) DeleteMacro(ctx context.Context, actor audit.Actor, id string) error {
	if err := s.db.WithContext(ctx).Delete(&domain.Macro{}, "id = ?", id).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "macro.deleted", TargetType: "macro", TargetID: id,
	})
	return nil
}
