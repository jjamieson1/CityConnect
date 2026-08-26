package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// performScan runs every configured check, captures its output as a raw report,
// classifies the result, and assembles a Run. A check whose tool isn't installed
// is recorded as "skipped" (with an install hint) rather than failing the scan —
// so the dashboard honestly shows both what ran and what's available to enable.
func performScan(cfg Config, dir, trigger string) Run {
	now := time.Now()
	run := Run{
		ID:        newRunID(now),
		StartedAt: now.UTC().Format(time.RFC3339),
		GitSHA:    gitOut("rev-parse", "--short", "HEAD"),
		GitBranch: gitOut("rev-parse", "--abbrev-ref", "HEAD"),
		Trigger:   trigger,
	}
	runDir := filepath.Join(dir, "runs", run.ID)
	_ = os.MkdirAll(runDir, 0o755)

	for _, def := range cfg.Checks {
		fmt.Printf("  · %-28s ", def.Key)
		res := runCheck(def, runDir)
		run.Checks = append(run.Checks, res)
		fmt.Printf("%s%s\n", statusGlyph(res.Status), func() string {
			if res.Summary != "" {
				return " — " + res.Summary
			}
			return ""
		}())
	}
	run.Totals = tally(run.Checks)
	return run
}

func runCheck(def CheckDef, runDir string) CheckResult {
	res := CheckResult{CheckDef: def}

	// Resolve the tool binary and skip cleanly if it's absent — but only for a
	// plain single-binary command. A command that uses shell syntax (pipes, ';',
	// env sourcing, an explicit `sh -c '…'`) has a first word that isn't a
	// resolvable binary (e.g. `sh`, `set`), so probing it would wrongly skip;
	// just run those.
	if bin := firstWord(def.Command); bin != "go" && bin != "npm" && !strings.ContainsAny(def.Command, ";|&$`") {
		if _, err := exec.LookPath(bin); err != nil {
			res.Status = "skipped"
			res.Summary = "not installed"
			return res
		}
	}
	if def.Dir != "" {
		if _, err := os.Stat(def.Dir); err != nil {
			res.Status = "skipped"
			res.Summary = def.Dir + " not present"
			return res
		}
	}

	start := time.Now()
	cmd := exec.Command("sh", "-c", def.Command)
	cmd.Dir = def.Dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	res.DurationMs = time.Since(start).Milliseconds()

	// Persist the raw report next to the run manifest and link it relatively.
	reportName := def.Key + ".txt"
	_ = os.WriteFile(filepath.Join(runDir, reportName), out.Bytes(), 0o644)
	// Linked relative to security/ so the dashboard opens reports locally.
	res.ReportFile = filepath.Join("runs", filepath.Base(runDir), reportName)

	exit := exitCode(runErr)
	parseResult(&res, out.String(), exit)
	return res
}

// parseResult applies the check's named parser to turn raw output + exit code
// into a status and finding count. Parsers are intentionally forgiving: if a
// tool's format shifts, we fall back to the exit code rather than crash.
func parseResult(res *CheckResult, output string, exit int) {
	switch res.Parser {
	case "govulncheck":
		// Prefer govulncheck's authoritative summary ("Your code is affected by N
		// vulnerabilities"); fall back to counting entries. Only *affecting* vulns
		// (ones your code calls) count as failures.
		n := 0
		if m := reAffected.FindStringSubmatch(output); len(m) == 2 {
			n = atoi(m[1])
		} else if strings.Contains(output, "No vulnerabilities found") {
			n = 0
		} else {
			n = len(reVuln.FindAllString(output, -1))
		}
		res.Findings = n
		if n > 0 {
			res.Status = "fail"
			res.Summary = plural(n, "vulnerability your code calls", "vulnerabilities your code calls")
		} else {
			res.Status = "pass"
			res.Summary = "no known vulnerable dependencies called"
		}
	case "gosec-json":
		res.Findings = countJSONArray(output, "Issues")
		gradeFindings(res, "issue", "issues")
	case "semgrep-json":
		res.Findings = countJSONArray(output, "results")
		gradeFindings(res, "finding", "findings")
	case "gitleaks":
		if m := reLeaks.FindStringSubmatch(output); len(m) == 2 {
			res.Findings = atoi(m[1])
		} else if exit != 0 {
			res.Findings = 1
		}
		if res.Findings > 0 {
			res.Status = "fail"
			res.Summary = plural(res.Findings, "secret leaked", "secrets leaked")
		} else {
			res.Status = "pass"
			res.Summary = "no secrets detected"
		}
	case "npm-audit":
		crit, high := npmAuditCounts(output)
		res.Findings = crit + high
		if crit > 0 {
			res.Status = "fail"
		} else if high > 0 {
			res.Status = "warn"
		} else {
			res.Status = "pass"
		}
		res.Summary = fmt.Sprintf("%d critical, %d high", crit, high)
	case "gotest":
		if exit == 0 {
			res.Status = "pass"
			if m := reTestOK.FindStringSubmatch(output); len(m) == 2 {
				res.Summary = "package tests passed"
			} else {
				res.Summary = "passed"
			}
		} else {
			res.Status = "fail"
			res.Findings = len(reTestFail.FindAllString(output, -1))
			res.Summary = plural(res.Findings, "test failed", "tests failed")
		}
	default: // "exit"
		if exit == 0 {
			res.Status = "pass"
			res.Summary = "passed"
		} else {
			res.Status = "fail"
			res.Summary = "non-zero exit"
		}
	}
}

// gradeFindings sets pass/warn from a raw finding count for advisory scanners.
func gradeFindings(res *CheckResult, one, many string) {
	if res.Findings > 0 {
		res.Status = "warn"
	} else {
		res.Status = "pass"
	}
	res.Summary = plural(res.Findings, one, many)
}

// ---- small parser helpers --------------------------------------------------

var (
	reVuln     = regexp.MustCompile(`Vulnerability #\d+`)
	reAffected = regexp.MustCompile(`affected by (\d+) vulnerabilit`)
	reLeaks    = regexp.MustCompile(`(?i)leaks found:?\s*(\d+)`)
	reTestOK   = regexp.MustCompile(`(?m)^ok\s+\S+`)
	reTestFail = regexp.MustCompile(`(?m)^--- FAIL`)
)

// countJSONArray parses output as JSON and returns len() of the named top-level
// array field (best-effort; 0 if absent/unparseable).
func countJSONArray(output, field string) int {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(output), &m) != nil {
		return 0
	}
	raw, ok := m[field]
	if !ok {
		return 0
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return 0
	}
	return len(arr)
}

// npmAuditCounts pulls critical/high counts from `npm audit --json`.
func npmAuditCounts(output string) (crit, high int) {
	var doc struct {
		Metadata struct {
			Vulnerabilities struct {
				Critical int `json:"critical"`
				High     int `json:"high"`
			} `json:"vulnerabilities"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(output), &doc) == nil {
		return doc.Metadata.Vulnerabilities.Critical, doc.Metadata.Vulnerabilities.High
	}
	return 0, 0
}

func tally(checks []CheckResult) Totals {
	var t Totals
	for _, c := range checks {
		switch c.Status {
		case "pass":
			t.Pass++
		case "fail", "error":
			t.Fail++
			if c.Gating {
				t.GatingFail++
			}
		case "warn":
			t.Warn++
		case "skipped":
			t.Skipped++
		}
	}
	return t
}

// ---- misc ------------------------------------------------------------------

func gitOut(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if bytes.Contains([]byte(err.Error()), []byte("executable file not found")) {
		return 127
	}
	if asExit(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func asExit(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

func firstWord(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), " ")
	return first
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func statusGlyph(status string) string {
	switch status {
	case "pass":
		return "✓ pass"
	case "fail", "error":
		return "✗ FAIL"
	case "warn":
		return "! warn"
	case "skipped":
		return "– skipped"
	}
	return status
}
