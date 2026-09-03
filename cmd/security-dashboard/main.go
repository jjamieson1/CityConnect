// Command security-dashboard turns the project's security + quality scans into a
// single, self-contained, shareable HTML dashboard — evidence that the app is
// tested with rigor, for developers and customers alike.
//
//	go run ./cmd/security-dashboard scan     # run all configured checks, record a run, re-render
//	go run ./cmd/security-dashboard render    # re-render dashboard.html from existing runs
//
// It is deliberately manifest-driven and language-agnostic: `scan` writes a JSON
// run manifest (+ raw reports) under security/runs/, and `render` builds the page
// from those manifests. Any project — Go or not — gets the same dashboard by
// dropping in a security/config.json describing its checks. That makes it a
// reusable piece of C2 for every client application (permitting, facility
// booking, …).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ---- data model (the security/ manifest schema) ---------------------------

type Config struct {
	App struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Repo        string `json:"repo"`
		Team        string `json:"team"`
		Contact     string `json:"contact"`
	} `json:"app"`
	ManualDoc   string        `json:"manualDoc"`
	Standards   []Standard    `json:"standards"`
	ASVS        *ASVS         `json:"asvs,omitempty"`
	Conformance []Conformance `json:"conformance,omitempty"`
	Checks      []CheckDef    `json:"checks"`
}

// Conformance is a formal conformance/certification test result (e.g. the
// OpenID Foundation OIDC suite) shown as compliance evidence.
type Conformance struct {
	Name    string `json:"name"`
	Suite   string `json:"suite,omitempty"`
	Status  string `json:"status"` // passed | issues | pending
	Summary string `json:"summary,omitempty"`
	LastRun string `json:"lastRun,omitempty"`
	Report  string `json:"report,omitempty"`
}

// ASVS captures the OWASP ASVS level the app targets and the evidence for each
// control family — persuasive for enterprise/gov buyers.
type ASVS struct {
	TargetLevel  string        `json:"targetLevel"`
	Note         string        `json:"note"`
	Requirements []ASVSReqItem `json:"requirements"`
}

type ASVSReqItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Coverage string `json:"coverage"` // automated | partial | manual
	Evidence string `json:"evidence"`
}

type Standard struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// CheckDef is a configured scan. Adding a check is a JSON edit, no code change:
// give it a command and one of the built-in parsers (see checks.go).
type CheckDef struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Tool        string `json:"tool"`
	Gating      bool   `json:"gating"`
	Command     string `json:"command"`
	Parser      string `json:"parser"`
	Dir         string `json:"dir,omitempty"`
	InstallHint string `json:"installHint,omitempty"`
}

// CheckResult is the outcome of running a CheckDef.
type CheckResult struct {
	CheckDef
	Status     string `json:"status"` // pass | fail | warn | skipped | error
	Summary    string `json:"summary"`
	Findings   int    `json:"findings"`
	ReportFile string `json:"reportFile,omitempty"` // relative to security/
	DurationMs int64  `json:"durationMs"`
}

// Run is one comprehensive scan.
type Run struct {
	ID        string        `json:"id"` // e.g. 20260825T101500Z
	StartedAt string        `json:"startedAt"`
	GitSHA    string        `json:"gitSha"`
	GitBranch string        `json:"gitBranch"`
	Trigger   string        `json:"trigger"` // manual | ci | nightly
	Checks    []CheckResult `json:"checks"`
	Totals    Totals        `json:"totals"`
}

type Totals struct {
	Pass, Fail, Warn, Skipped, GatingFail int
}

type Tasks struct {
	Tasks []Task `json:"tasks"`
}

type Task struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Category  string `json:"category"`
	Cadence   string `json:"cadence"`
	Owner     string `json:"owner"`
	Status    string `json:"status"`
	LastRun   string `json:"lastRun"`
	NextDue   string `json:"nextDue"`
	DocAnchor string `json:"docAnchor"`
	Notes     string `json:"notes"`
}

// ---- entrypoint ------------------------------------------------------------

func main() {
	dir := "security"
	trigger := "manual"
	args := os.Args[1:]
	cmd := "render"
	if len(args) > 0 && (args[0] == "scan" || args[0] == "render" || args[0] == "bundle") {
		cmd = args[0]
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			i++
			if i < len(args) {
				dir = args[i]
			}
		case "--trigger":
			i++
			if i < len(args) {
				trigger = args[i]
			}
		}
	}

	cfg, err := loadConfig(dir)
	if err != nil {
		fatal(err)
	}

	// `bundle` emits a full health bundle (security + compliance) to stdout, ready
	// to upload to C2's admin Health dashboard for this application. Availability
	// is left for the caller/monitoring to add.
	if cmd == "bundle" {
		b, err := buildBundle(dir, cfg)
		if err != nil {
			fatal(err)
		}
		_, _ = os.Stdout.Write(append(b, '\n'))
		return
	}

	if cmd == "scan" {
		run := performScan(cfg, dir, trigger)
		if err := saveRun(dir, run); err != nil {
			fatal(err)
		}
		fmt.Printf("security-dashboard: recorded run %s (%d pass, %d fail, %d warn, %d skipped)\n",
			run.ID, run.Totals.Pass, run.Totals.Fail, run.Totals.Warn, run.Totals.Skipped)
	}

	if err := render(dir, cfg); err != nil {
		fatal(err)
	}
	fmt.Printf("security-dashboard: wrote %s\n", filepath.Join(dir, "dashboard.html"))
}

// buildBundle assembles a health bundle for upload to C2: the latest security
// run plus the compliance evidence from config. Shape matches the admin Health
// upload contract ({security, compliance}).
func buildBundle(dir string, cfg Config) ([]byte, error) {
	runs := loadRuns(dir)
	if len(runs) == 0 {
		return nil, fmt.Errorf("no runs found in %s/runs — run `scan` first", dir)
	}
	compliance := map[string]any{
		"standards":   cfg.Standards,
		"conformance": cfg.Conformance,
	}
	if cfg.ASVS != nil {
		compliance["asvsLevel"] = cfg.ASVS.TargetLevel
		compliance["note"] = cfg.ASVS.Note
		compliance["requirements"] = cfg.ASVS.Requirements
	}
	bundle := map[string]any{
		"app":        map[string]string{"name": cfg.App.Name, "description": cfg.App.Description},
		"security":   runs[0],
		"compliance": compliance,
	}
	return json.MarshalIndent(bundle, "", "  ")
}

// ---- manifest IO -----------------------------------------------------------

func loadConfig(dir string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func loadTasks(dir string) Tasks {
	var t Tasks
	if b, err := os.ReadFile(filepath.Join(dir, "tasks.json")); err == nil {
		_ = json.Unmarshal(b, &t)
	}
	return t
}

func saveRun(dir string, run Run) error {
	runsDir := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runsDir, run.ID+".json"), append(b, '\n'), 0o644)
}

// loadRuns returns every recorded run, newest first.
func loadRuns(dir string) []Run {
	var runs []Run
	matches, _ := filepath.Glob(filepath.Join(dir, "runs", "*.json"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var r Run
		if json.Unmarshal(b, &r) == nil && r.ID != "" {
			runs = append(runs, r)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	return runs
}

func newRunID(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "security-dashboard:", err)
	os.Exit(1)
}
