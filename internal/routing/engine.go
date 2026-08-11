// Package routing decides where an incoming request goes: which queue, which
// owner, what priority. Rules are a small typed predicate set evaluated in
// priority order, plus a simulator that dry-runs a rule set against real
// history before anyone activates it.
package routing

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// Facts are the attributes a rule can test. Assembling them once, explicitly,
// keeps rule evaluation independent of the request model and makes the
// simulator trivially correct: it feeds the same struct.
type Facts struct {
	ServiceTypeCode string
	Category        string
	Priority        string
	Source          string
	Ward            string
	PostalCode      string
	City            string
	Subject         string
	Description     string
	DepartmentID    string
	Tags            []string
	FormData        domain.JSONMap
}

// FactsFromRequest builds the fact set for a request.
func FactsFromRequest(r *domain.Request, serviceTypeCode, category string) Facts {
	return Facts{
		ServiceTypeCode: serviceTypeCode,
		Category:        category,
		Priority:        r.Priority,
		Source:          r.Source,
		Ward:            r.Ward,
		PostalCode:      r.PostalCode,
		City:            r.City,
		Subject:         r.Subject,
		Description:     r.Description,
		DepartmentID:    r.DepartmentID,
		Tags:            r.Tags,
		FormData:        r.FormData,
	}
}

// Decision is the outcome of evaluating a rule set.
type Decision struct {
	QueueID      string   `json:"queueId,omitempty"`
	AssigneeID   string   `json:"assigneeUserId,omitempty"`
	SystemID     string   `json:"assigneeSystemId,omitempty"`
	DepartmentID string   `json:"departmentId,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	SLAPolicyID  string   `json:"slaPolicyId,omitempty"`
	AddTags      []string `json:"addTags,omitempty"`
	SetStatus    string   `json:"setStatus,omitempty"`
	Notify       bool     `json:"notify,omitempty"`

	// MatchedRules records which rules fired, in order. The simulator surfaces
	// this, and a request's timeline records it, so "why did this land in
	// Bylaw?" has an answer.
	MatchedRules []MatchedRule `json:"matchedRules,omitempty"`
}

// MatchedRule identifies a rule that fired.
type MatchedRule struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Evaluate runs an ordered rule set against a fact set.
//
// Rules are first-match-wins unless a rule sets Continue, in which case
// evaluation carries on and later rules may add to the decision. Later rules
// never overwrite a field an earlier rule already set — otherwise rule order
// would mean the opposite of what an admin reading the list expects.
func Evaluate(rules []domain.RoutingRule, f Facts) Decision {
	var d Decision

	for i := range rules {
		rule := &rules[i]
		if !rule.Active {
			continue
		}
		ok, err := Matches(rule.Conditions, f)
		if err != nil || !ok {
			continue
		}

		d.MatchedRules = append(d.MatchedRules, MatchedRule{ID: rule.ID, Name: rule.Name})
		applyActions(&d, rule.Actions)

		if !rule.Continue {
			break
		}
	}
	return d
}

func applyActions(d *Decision, raw domain.JSONMap) {
	var a domain.RuleActions
	b, err := json.Marshal(raw)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return
	}

	setIfEmpty(&d.QueueID, a.QueueID)
	setIfEmpty(&d.AssigneeID, a.AssigneeID)
	setIfEmpty(&d.SystemID, a.SystemID)
	setIfEmpty(&d.DepartmentID, a.DepartmentID)
	setIfEmpty(&d.Priority, a.Priority)
	setIfEmpty(&d.SLAPolicyID, a.SLAPolicyID)
	setIfEmpty(&d.SetStatus, a.SetStatus)

	if a.Notify {
		d.Notify = true
	}
	// Tags accumulate across rules rather than competing, since they are
	// additive labels rather than a single assignment.
	for _, tag := range a.AddTags {
		if tag != "" && !containsFold(d.AddTags, tag) {
			d.AddTags = append(d.AddTags, strings.ToLower(tag))
		}
	}
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" && v != "" {
		*dst = v
	}
}

// Matches evaluates a condition group against the facts.
//
// The group shape is {"all": [...]} and/or {"any": [...]}. Both may be
// present, in which case both must hold. An empty group matches everything,
// which is how a catch-all rule is written.
func Matches(conditions domain.JSONMap, f Facts) (bool, error) {
	if len(conditions) == 0 {
		return true, nil
	}

	all, err := decodeConditions(conditions["all"])
	if err != nil {
		return false, err
	}
	anyOf, err := decodeConditions(conditions["any"])
	if err != nil {
		return false, err
	}

	for _, c := range all {
		ok, err := matchOne(c, f)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}

	if len(anyOf) > 0 {
		matched := false
		for _, c := range anyOf {
			ok, err := matchOne(c, f)
			if err != nil {
				return false, err
			}
			if ok {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
}

func decodeConditions(raw any) ([]domain.Condition, error) {
	if raw == nil {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("routing: encode conditions: %w", err)
	}
	var out []domain.Condition
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("routing: malformed conditions: %w", err)
	}
	return out, nil
}

func matchOne(c domain.Condition, f Facts) (bool, error) {
	// Tags are a list, so they get their own handling rather than being
	// squeezed into the scalar comparison below.
	if c.Field == "tag" {
		switch c.Op {
		case "eq", "contains", "in":
			wanted := c.List
			if c.Value != "" {
				wanted = append(wanted, c.Value)
			}
			for _, w := range wanted {
				if containsFold(f.Tags, w) {
					return true, nil
				}
			}
			return false, nil
		case "not_contains", "neq", "not_in":
			for _, w := range append(c.List, c.Value) {
				if w != "" && containsFold(f.Tags, w) {
					return false, nil
				}
			}
			return true, nil
		}
	}

	value, present := fieldValue(c.Field, f)

	switch c.Op {
	case "exists":
		return present && value != "", nil
	case "not_exists":
		return !present || value == "", nil
	case "eq":
		return strings.EqualFold(value, c.Value), nil
	case "neq":
		return !strings.EqualFold(value, c.Value), nil
	case "in":
		return containsFold(c.List, value), nil
	case "not_in":
		return !containsFold(c.List, value), nil
	case "contains":
		return strings.Contains(strings.ToLower(value), strings.ToLower(c.Value)), nil
	case "not_contains":
		return !strings.Contains(strings.ToLower(value), strings.ToLower(c.Value)), nil
	case "starts_with":
		return strings.HasPrefix(strings.ToLower(value), strings.ToLower(c.Value)), nil
	case "gt", "lt":
		a, err1 := strconv.ParseFloat(strings.TrimSpace(value), 64)
		b, err2 := strconv.ParseFloat(strings.TrimSpace(c.Value), 64)
		if err1 != nil || err2 != nil {
			// Priority is ordinal rather than numeric, so compare its rank.
			if c.Field == "priority" {
				ra, rb := domain.PriorityRank(value), domain.PriorityRank(c.Value)
				if c.Op == "gt" {
					return ra > rb, nil
				}
				return ra < rb && ra > 0, nil
			}
			return false, nil
		}
		if c.Op == "gt" {
			return a > b, nil
		}
		return a < b, nil
	}
	return false, fmt.Errorf("routing: unknown operator %q", c.Op)
}

func fieldValue(field string, f Facts) (string, bool) {
	if after, ok := strings.CutPrefix(field, "form."); ok {
		v, present := f.FormData[after]
		if !present || v == nil {
			return "", false
		}
		return fmt.Sprint(v), true
	}

	switch field {
	case "service_type":
		return f.ServiceTypeCode, f.ServiceTypeCode != ""
	case "category":
		return f.Category, f.Category != ""
	case "priority":
		return f.Priority, f.Priority != ""
	case "source":
		return f.Source, f.Source != ""
	case "ward":
		return f.Ward, f.Ward != ""
	case "postal_code":
		return f.PostalCode, f.PostalCode != ""
	case "city":
		return f.City, f.City != ""
	case "subject":
		return f.Subject, f.Subject != ""
	case "description":
		return f.Description, f.Description != ""
	case "department":
		return f.DepartmentID, f.DepartmentID != ""
	}
	return "", false
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// ValidateRule checks a rule's conditions and actions before it is saved. An
// unparseable rule that silently never fires is worse than one that is
// rejected: a queue quietly stops receiving work and nobody notices for weeks.
func ValidateRule(r *domain.RoutingRule) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("routing: rule name is required")
	}

	all, err := decodeConditions(r.Conditions["all"])
	if err != nil {
		return err
	}
	anyOf, err := decodeConditions(r.Conditions["any"])
	if err != nil {
		return err
	}

	for _, c := range append(all, anyOf...) {
		if c.Field == "" {
			return fmt.Errorf("routing: a condition has no field")
		}
		switch c.Op {
		case "eq", "neq", "in", "not_in", "contains", "not_contains",
			"starts_with", "gt", "lt", "exists", "not_exists":
		default:
			return fmt.Errorf("routing: unknown operator %q on field %q", c.Op, c.Field)
		}
		if (c.Op == "in" || c.Op == "not_in") && len(c.List) == 0 {
			return fmt.Errorf("routing: operator %q on %q needs a list of values", c.Op, c.Field)
		}
	}

	var actions domain.RuleActions
	b, _ := json.Marshal(r.Actions)
	if err := json.Unmarshal(b, &actions); err != nil {
		return fmt.Errorf("routing: malformed actions: %w", err)
	}
	if actions.QueueID == "" && actions.AssigneeID == "" && actions.SystemID == "" &&
		actions.Priority == "" && actions.DepartmentID == "" && actions.SLAPolicyID == "" &&
		len(actions.AddTags) == 0 && actions.SetStatus == "" && !actions.Notify {
		return fmt.Errorf("routing: rule %q would do nothing", r.Name)
	}
	if actions.Priority != "" && domain.PriorityRank(actions.Priority) == 0 {
		return fmt.Errorf("routing: unknown priority %q", actions.Priority)
	}
	return nil
}
