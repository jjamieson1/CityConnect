package main

import (
	"html/template"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// render builds the self-contained dashboard.html from the config, the recorded
// runs, and the manual-task list. No external assets — it opens anywhere.
func render(dir string, cfg Config) error {
	runs := loadRuns(dir)
	tasks := loadTasks(dir)

	v := view{
		Cfg:         cfg,
		History:     runs,
		Tasks:       tasks.Tasks,
		ASVS:        cfg.ASVS,
		Conformance: cfg.Conformance,
		GeneratedAt: time.Now().Format("2006-01-02 15:04 MST"),
		ManualHref:  relFromSecurity(cfg.ManualDoc),
	}
	if len(runs) > 0 {
		v.Latest = &runs[0]
		v.Categories = groupByCategory(runs[0].Checks)
		v.Posture, v.PostureLabel, v.PostureNote = posture(runs[0])
	} else {
		v.Posture, v.PostureLabel, v.PostureNote = "none", "No scans yet", "Run: go run ./cmd/security-dashboard scan"
	}
	v.TaskGroups = groupTasks(tasks.Tasks)
	v.Trend = trendRuns(runs, 14)

	// The README badge tracks the same posture; write it alongside the page.
	if err := writeBadge(dir, v.Latest); err != nil {
		return err
	}

	tmpl, err := template.New("dash").Funcs(funcs).Parse(dashboardHTML)
	if err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "dashboard.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	return tmpl.Execute(f, v)
}

// ---- view model ------------------------------------------------------------

type view struct {
	Cfg          Config
	Latest       *Run
	Categories   []categoryGroup
	History      []Run
	Trend        []Run // chronological (oldest first) for the trend chart
	Tasks        []Task
	TaskGroups   []taskGroup
	ASVS         *ASVS
	Conformance  []Conformance
	Posture      string // green | amber | red | none
	PostureLabel string
	PostureNote  string
	GeneratedAt  string
	ManualHref   string
}

// trendRuns returns up to n most-recent runs in chronological order (oldest
// first) so the trend chart reads left-to-right in time.
func trendRuns(runs []Run, n int) []Run {
	if len(runs) > n {
		runs = runs[:n]
	}
	out := make([]Run, len(runs))
	for i, r := range runs {
		out[len(runs)-1-i] = r // reverse: loadRuns is newest-first
	}
	return out
}

type categoryGroup struct {
	Name   string
	Checks []CheckResult
}

type taskGroup struct {
	Cadence string
	Tasks   []Task
}

func groupByCategory(checks []CheckResult) []categoryGroup {
	order := []string{}
	byCat := map[string][]CheckResult{}
	for _, c := range checks {
		if _, ok := byCat[c.Category]; !ok {
			order = append(order, c.Category)
		}
		byCat[c.Category] = append(byCat[c.Category], c)
	}
	sort.Strings(order)
	var out []categoryGroup
	for _, name := range order {
		out = append(out, categoryGroup{Name: name, Checks: byCat[name]})
	}
	return out
}

func groupTasks(tasks []Task) []taskGroup {
	// Preserve a sensible cadence order.
	order := []string{"Every release", "Every PR", "Quarterly", "Quarterly + on federation changes"}
	seen := map[string]bool{}
	byCad := map[string][]Task{}
	var extra []string
	for _, t := range tasks {
		if _, ok := byCad[t.Cadence]; !ok && !slices.Contains(order, t.Cadence) {
			extra = append(extra, t.Cadence)
		}
		byCad[t.Cadence] = append(byCad[t.Cadence], t)
	}
	var out []taskGroup
	for _, c := range append(order, extra...) {
		if ts, ok := byCad[c]; ok && !seen[c] {
			seen[c] = true
			out = append(out, taskGroup{Cadence: c, Tasks: ts})
		}
	}
	return out
}

// posture derives the headline status from the latest run.
func posture(r Run) (code, label, note string) {
	switch {
	case r.Totals.GatingFail > 0:
		return "red", "Action required", plural(r.Totals.GatingFail, "gating check failing", "gating checks failing")
	case r.Totals.Fail > 0:
		return "amber", "Passing with issues", plural(r.Totals.Fail, "non-gating failure", "non-gating failures")
	case r.Totals.Warn > 0 || r.Totals.Skipped > 0:
		return "amber", "Passing with advisories", "advisories and/or checks awaiting enablement"
	default:
		return "green", "All checks passing", "every configured check is green"
	}
}

// relFromSecurity rewrites a repo-root-relative path to be relative to the
// security/ dir (where dashboard.html lives).
func relFromSecurity(p string) string {
	if p == "" {
		return ""
	}
	return "../" + p
}

// ---- template helpers ------------------------------------------------------

func (c CheckResult) StatusClass() string { return statusToClass(c.Status) }
func (c CheckResult) StatusLabel() string {
	switch c.Status {
	case "pass":
		return "Pass"
	case "fail", "error":
		return "Fail"
	case "warn":
		return "Advisory"
	case "skipped":
		return "Not run"
	}
	return c.Status
}

func statusToClass(s string) string {
	switch s {
	case "pass":
		return "ok"
	case "fail", "error":
		return "bad"
	case "warn":
		return "warn"
	case "skipped":
		return "muted"
	}
	return "muted"
}

func (r Run) When() string {
	if t, err := time.Parse(time.RFC3339, r.StartedAt); err == nil {
		return t.Local().Format("2006-01-02 15:04 MST")
	}
	return r.ID
}

var funcs = template.FuncMap{
	"statusClass": statusToClass,
	"taskClass": func(status string) string {
		switch strings.ToLower(status) {
		case "done", "passed", "green":
			return "ok"
		case "overdue", "failed":
			return "bad"
		case "in-progress", "in progress":
			return "warn"
		}
		return "muted"
	},
	"dash": func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "—"
		}
		return s
	},
}
