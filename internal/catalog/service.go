// Package catalog owns the service catalogue and the policies attached to it:
// service types and their intake forms, SLA policies, business calendars,
// notification templates and macros.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// Service errors.
var (
	ErrNotFound     = errors.New("catalog: not found")
	ErrInvalidInput = errors.New("catalog: invalid input")
	ErrConflict     = errors.New("catalog: conflict")
)

// Service implements catalogue management.
type Service struct {
	db    *gorm.DB
	audit *audit.Service
	log   *slog.Logger

	// calendars are cached because the SLA scheduler loads one per request it
	// evaluates, and parsing a time zone on every call is wasteful.
	mu        sync.RWMutex
	calendars map[string]*Calendar
}

// NewService builds the catalog service.
func NewService(db *gorm.DB, aud *audit.Service, log *slog.Logger) *Service {
	return &Service{
		db: db, audit: aud, log: log.With("component", "catalog"),
		calendars: map[string]*Calendar{},
	}
}

// ---------------------------------------------------------------------------
// Service types
// ---------------------------------------------------------------------------

// ServiceTypeFilter narrows a catalogue listing.
type ServiceTypeFilter struct {
	DepartmentID  string
	Category      string
	Query         string
	PublicOnly    bool
	IncludeHidden bool
}

// ListServiceTypes returns the catalogue.
func (s *Service) ListServiceTypes(ctx context.Context, f ServiceTypeFilter) ([]domain.ServiceType, error) {
	q := s.db.WithContext(ctx).Model(&domain.ServiceType{}).Preload("Department").Preload("SLAPolicy")

	if !f.IncludeHidden {
		q = q.Where("active = ?", true)
	}
	if f.PublicOnly {
		q = q.Where("public_visible = ?", true)
	}
	if f.DepartmentID != "" {
		q = q.Where("department_id = ?", f.DepartmentID)
	}
	if f.Category != "" {
		q = q.Where("category = ?", f.Category)
	}
	if f.Query != "" {
		like := "%" + store.LikeEscape(f.Query) + "%"
		q = q.Where("name LIKE ? OR code LIKE ?", like, like)
	}

	var out []domain.ServiceType
	err := q.Order("category ASC, name ASC").Find(&out).Error
	return out, store.Translate(err)
}

// GetServiceType loads one catalogue entry.
func (s *Service) GetServiceType(ctx context.Context, id string) (*domain.ServiceType, error) {
	var st domain.ServiceType
	err := s.db.WithContext(ctx).Preload("Department").Preload("SLAPolicy").
		First(&st, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &st, store.Translate(err)
}

// GetServiceTypeByCode resolves a catalogue entry by its stable code, which is
// what partner systems quote rather than a UUID.
func (s *Service) GetServiceTypeByCode(ctx context.Context, code string) (*domain.ServiceType, error) {
	var st domain.ServiceType
	err := s.db.WithContext(ctx).Preload("SLAPolicy").
		First(&st, "code = ?", strings.ToUpper(strings.TrimSpace(code))).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &st, store.Translate(err)
}

// SaveServiceType creates or updates a catalogue entry.
func (s *Service) SaveServiceType(ctx context.Context, actor audit.Actor, st *domain.ServiceType) (*domain.ServiceType, error) {
	st.Code = strings.ToUpper(strings.TrimSpace(st.Code))
	st.Name = strings.TrimSpace(st.Name)
	if st.Code == "" || st.Name == "" {
		return nil, fmt.Errorf("%w: service type code and name are required", ErrInvalidInput)
	}
	if _, err := ParseForm(st.IntakeForm); err != nil {
		return nil, err
	}
	if st.DefaultPriority == "" {
		st.DefaultPriority = domain.PriorityNormal
	}

	action := "service_type.created"
	if st.ID != "" {
		action = "service_type.updated"
	}
	// store.Save rather than gorm's: a service type created with Active or
	// PublicVisible unticked would otherwise be written as ticked, publishing
	// something to the citizen portal that an administrator staged as a draft.
	if err := store.Save(s.db.WithContext(ctx), st, st.ID); err != nil {
		return nil, err
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "service_type", TargetID: st.ID, Summary: st.Name,
	})
	return st, nil
}

// DeleteServiceType deactivates a catalogue entry. Entries are never hard
// deleted while requests reference them, since a request must always be able
// to say what was asked for.
func (s *Service) DeleteServiceType(ctx context.Context, actor audit.Actor, id string) error {
	var used int64
	s.db.WithContext(ctx).Model(&domain.Request{}).Where("service_type_id = ?", id).Count(&used)
	if used > 0 {
		if err := s.db.WithContext(ctx).Model(&domain.ServiceType{}).
			Where("id = ?", id).UpdateColumn("active", false).Error; err != nil {
			return store.Translate(err)
		}
		s.audit.Record(ctx, actor, audit.Entry{
			Action: "service_type.deactivated", TargetType: "service_type", TargetID: id,
			Summary: fmt.Sprintf("deactivated instead of deleted; %d request(s) reference it", used),
		})
		return nil
	}
	if err := s.db.WithContext(ctx).Delete(&domain.ServiceType{}, "id = ?", id).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "service_type.deleted", TargetType: "service_type", TargetID: id,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Intake forms
// ---------------------------------------------------------------------------

// ParseForm extracts the field list from a service type's intake form.
func ParseForm(form domain.JSONMap) ([]domain.FormField, error) {
	if form == nil {
		return nil, nil
	}
	raw, ok := form["fields"]
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: intake form is not encodable: %v", ErrInvalidInput, err)
	}
	var fields []domain.FormField
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, fmt.Errorf("%w: intake form fields are malformed: %v", ErrInvalidInput, err)
	}
	for i, f := range fields {
		if f.Key == "" {
			return nil, fmt.Errorf("%w: intake form field %d has no key", ErrInvalidInput, i)
		}
		if f.Pattern != "" {
			if _, err := regexp.Compile(f.Pattern); err != nil {
				return nil, fmt.Errorf("%w: field %q has an invalid pattern: %v", ErrInvalidInput, f.Key, err)
			}
		}
	}
	return fields, nil
}

// ValidateFormData checks submitted data against a service type's form and
// returns the cleaned values. Unknown keys are dropped rather than rejected,
// so an older partner client that still sends a retired field keeps working.
func ValidateFormData(st *domain.ServiceType, data domain.JSONMap) (domain.JSONMap, error) {
	fields, err := ParseForm(st.IntakeForm)
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return data, nil
	}

	out := domain.JSONMap{}
	var problems []string

	for _, f := range fields {
		raw, present := data[f.Key]
		if !present || raw == nil || raw == "" {
			if f.Required {
				problems = append(problems, fmt.Sprintf("%s is required", label(f)))
			}
			continue
		}

		switch f.Type {
		case "number":
			n, ok := toFloat(raw)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s must be a number", label(f)))
				continue
			}
			if f.Min != nil && n < *f.Min {
				problems = append(problems, fmt.Sprintf("%s must be at least %g", label(f), *f.Min))
				continue
			}
			if f.Max != nil && n > *f.Max {
				problems = append(problems, fmt.Sprintf("%s must be at most %g", label(f), *f.Max))
				continue
			}
			out[f.Key] = n

		case "checkbox":
			b, ok := raw.(bool)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s must be true or false", label(f)))
				continue
			}
			out[f.Key] = b

		case "select":
			v := fmt.Sprint(raw)
			if len(f.Options) > 0 && !containsString(f.Options, v) {
				problems = append(problems, fmt.Sprintf("%s must be one of: %s", label(f), strings.Join(f.Options, ", ")))
				continue
			}
			out[f.Key] = v

		case "multiselect":
			values, ok := toStringSlice(raw)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s must be a list", label(f)))
				continue
			}
			for _, v := range values {
				if len(f.Options) > 0 && !containsString(f.Options, v) {
					problems = append(problems, fmt.Sprintf("%s contains an unknown option %q", label(f), v))
				}
			}
			out[f.Key] = values

		case "date":
			v := fmt.Sprint(raw)
			if _, err := time.Parse("2006-01-02", v); err != nil {
				problems = append(problems, fmt.Sprintf("%s must be a date (YYYY-MM-DD)", label(f)))
				continue
			}
			out[f.Key] = v

		default: // text, textarea and anything unrecognised
			v := strings.TrimSpace(fmt.Sprint(raw))
			if f.Pattern != "" {
				re, err := regexp.Compile(f.Pattern)
				if err == nil && !re.MatchString(v) {
					problems = append(problems, fmt.Sprintf("%s is not in the expected format", label(f)))
					continue
				}
			}
			out[f.Key] = v
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidInput, strings.Join(problems, "; "))
	}
	return out, nil
}

func label(f domain.FormField) string {
	if f.Label != "" {
		return f.Label
	}
	return f.Key
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(n, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func toStringSlice(v any) ([]string, bool) {
	switch list := v.(type) {
	case []string:
		return list, true
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			out = append(out, fmt.Sprint(item))
		}
		return out, true
	}
	return nil, false
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
