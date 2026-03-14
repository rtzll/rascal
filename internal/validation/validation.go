package validation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultConfigFile = "validation_config.json"
	DefaultJSONFile   = "validation.json"
	DefaultMarkdown   = "validation.md"
)

type Status string

const (
	StatusPass    Status = "pass"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
)

type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

type GateStatus string

const (
	GateStatusPass    GateStatus = "pass"
	GateStatusBlocked GateStatus = "blocked"
)

type GatePolicy struct {
	BlockOnDeterministicFailure bool `json:"block_on_deterministic_failure"`
	BlockOnCritiqueBlocker      bool `json:"block_on_critique_blocker"`
	BlockOnCritiqueWarning      bool `json:"block_on_critique_warning"`
}

type Config struct {
	Enabled            bool       `json:"enabled"`
	CritiqueEnabled    bool       `json:"critique_enabled"`
	TestCritiqueEnable bool       `json:"test_critique_enabled"`
	Gate               GatePolicy `json:"gate"`
}

type ValidatorResult struct {
	Name        string `json:"name"`
	Command     string `json:"command,omitempty"`
	Source      string `json:"source,omitempty"`
	Status      Status `json:"status"`
	Summary     string `json:"summary"`
	DetailsPath string `json:"details_path,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
}

type Finding struct {
	Source            string   `json:"source,omitempty"`
	Severity          Severity `json:"severity"`
	Category          string   `json:"category"`
	Path              string   `json:"path,omitempty"`
	Rationale         string   `json:"rationale"`
	SuggestedFollowUp string   `json:"suggested_follow_up,omitempty"`
	RelatedValidator  string   `json:"related_validator,omitempty"`
}

type CritiqueReport struct {
	Enabled            bool   `json:"enabled"`
	Ran                bool   `json:"ran"`
	SkippedReason      string `json:"skipped_reason,omitempty"`
	TestCritiqueEnable bool   `json:"test_critique_enabled"`
}

type GateDecision struct {
	Status  GateStatus `json:"status"`
	Blocked bool       `json:"blocked"`
	Summary string     `json:"summary"`
	Reasons []string   `json:"reasons,omitempty"`
	Policy  GatePolicy `json:"policy"`
}

type Summary struct {
	ValidatorsByStatus map[Status]int   `json:"validators_by_status"`
	FindingsBySeverity map[Severity]int `json:"findings_by_severity"`
	ValidatorCount     int              `json:"validator_count"`
	FindingCount       int              `json:"finding_count"`
}

type Report struct {
	SchemaVersion       string            `json:"schema_version"`
	GeneratedAt         time.Time         `json:"generated_at"`
	Phase               string            `json:"phase"`
	Enabled             bool              `json:"enabled"`
	DeterministicResult []ValidatorResult `json:"deterministic_results"`
	Critique            CritiqueReport    `json:"critique"`
	Findings            []Finding         `json:"findings"`
	Gate                GateDecision      `json:"gate"`
	Summary             Summary           `json:"summary"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		CritiqueEnabled:    true,
		TestCritiqueEnable: true,
		Gate: GatePolicy{
			BlockOnDeterministicFailure: true,
			BlockOnCritiqueBlocker:      true,
			BlockOnCritiqueWarning:      false,
		},
	}
}

func (cfg Config) Normalize() Config {
	if !cfg.Enabled {
		cfg.CritiqueEnabled = false
	}
	if !cfg.CritiqueEnabled {
		cfg.TestCritiqueEnable = false
	}
	return cfg
}

func NewDisabledReport() Report {
	cfg := DefaultConfig()
	cfg.Enabled = false
	cfg.CritiqueEnabled = false
	cfg.TestCritiqueEnable = false
	return BuildReport(cfg, nil, CritiqueReport{
		Enabled:            false,
		Ran:                false,
		SkippedReason:      "validation disabled",
		TestCritiqueEnable: false,
	}, nil)
}

func BuildReport(cfg Config, results []ValidatorResult, critique CritiqueReport, findings []Finding) Report {
	cfg = cfg.Normalize()
	report := Report{
		SchemaVersion:       "v1",
		GeneratedAt:         time.Now().UTC(),
		Phase:               "validation",
		Enabled:             cfg.Enabled,
		DeterministicResult: append([]ValidatorResult(nil), results...),
		Critique:            critique,
		Findings:            append([]Finding(nil), findings...),
	}
	report.Gate = EvaluateGate(results, findings, cfg.Gate)
	report.Summary = Summary{
		ValidatorsByStatus: countValidatorStatuses(results),
		FindingsBySeverity: countFindingSeverities(findings),
		ValidatorCount:     len(results),
		FindingCount:       len(findings),
	}
	return report
}

func EvaluateGate(results []ValidatorResult, findings []Finding, policy GatePolicy) GateDecision {
	reasons := make([]string, 0, 3)
	failingValidators := make([]string, 0)
	for _, result := range results {
		if result.Status == StatusFail {
			failingValidators = append(failingValidators, result.Name)
		}
	}
	if policy.BlockOnDeterministicFailure && len(failingValidators) > 0 {
		reasons = append(reasons, "deterministic failures: "+strings.Join(failingValidators, ", "))
	}

	var blockers int
	var warnings int
	for _, finding := range findings {
		switch finding.Severity {
		case SeverityBlocker:
			blockers++
		case SeverityWarning:
			warnings++
		}
	}
	if policy.BlockOnCritiqueBlocker && blockers > 0 {
		reasons = append(reasons, fmt.Sprintf("critique blockers: %d", blockers))
	}
	if policy.BlockOnCritiqueWarning && warnings > 0 {
		reasons = append(reasons, fmt.Sprintf("critique warnings: %d", warnings))
	}

	decision := GateDecision{
		Status:  GateStatusPass,
		Blocked: false,
		Summary: "Validation passed.",
		Policy:  policy,
	}
	if len(reasons) == 0 {
		if warnings > 0 {
			decision.Summary = fmt.Sprintf("Validation passed with %d warning(s).", warnings)
		}
		return decision
	}
	decision.Status = GateStatusBlocked
	decision.Blocked = true
	decision.Reasons = reasons
	decision.Summary = "Validation blocked: " + strings.Join(reasons, "; ")
	return decision
}

func SummaryLine(report Report) string {
	if !report.Enabled {
		return "Validation disabled."
	}
	pass := report.Summary.ValidatorsByStatus[StatusPass]
	warn := report.Summary.ValidatorsByStatus[StatusWarn]
	fail := report.Summary.ValidatorsByStatus[StatusFail]
	findings := report.Summary.FindingCount
	if report.Gate.Blocked {
		return fmt.Sprintf("Validation blocked: %d fail, %d warn, %d finding(s).", fail, warn, findings)
	}
	return fmt.Sprintf("Validation passed: %d pass, %d warn, %d finding(s).", pass, warn, findings)
}

func WriteConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg.Normalize(), "", "  ")
	if err != nil {
		return fmt.Errorf("encode validation config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write validation config: %w", err)
	}
	return nil
}

func ReadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read validation config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode validation config: %w", err)
	}
	return cfg.Normalize(), nil
}

func WriteArtifacts(dir string, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode validation report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, DefaultJSONFile), data, 0o644); err != nil {
		return fmt.Errorf("write validation json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, DefaultMarkdown), []byte(RenderMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write validation markdown: %w", err)
	}
	return nil
}

func ReadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("read validation report: %w", err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("decode validation report: %w", err)
	}
	return report, nil
}

func RenderMarkdown(report Report) string {
	var b strings.Builder
	b.WriteString("# Validation Report\n\n")
	b.WriteString("- Gate: `" + string(report.Gate.Status) + "`\n")
	b.WriteString("- Summary: " + report.Gate.Summary + "\n")
	_, _ = fmt.Fprintf(&b, "- Validators: %d\n", report.Summary.ValidatorCount)
	_, _ = fmt.Fprintf(&b, "- Findings: %d\n\n", report.Summary.FindingCount)

	b.WriteString("## Deterministic Validators\n\n")
	if len(report.DeterministicResult) == 0 {
		b.WriteString("_No validator results._\n\n")
	} else {
		for _, result := range report.DeterministicResult {
			_, _ = fmt.Fprintf(&b, "- `%s`: `%s`", result.Name, result.Status)
			if result.Summary != "" {
				b.WriteString(" - " + result.Summary)
			}
			if result.DetailsPath != "" {
				b.WriteString(" (`" + result.DetailsPath + "`)")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Critique\n\n")
	if !report.Critique.Enabled {
		b.WriteString("_Critique disabled._\n\n")
	} else if !report.Critique.Ran {
		if report.Critique.SkippedReason == "" {
			b.WriteString("_Critique skipped._\n\n")
		} else {
			b.WriteString("_Critique skipped: " + report.Critique.SkippedReason + "._\n\n")
		}
	} else if len(report.Findings) == 0 {
		b.WriteString("_No critique findings._\n\n")
	} else {
		for _, finding := range report.Findings {
			_, _ = fmt.Fprintf(&b, "- `%s/%s`", finding.Severity, finding.Category)
			if finding.Path != "" {
				b.WriteString(" `" + finding.Path + "`")
			}
			if finding.Rationale != "" {
				b.WriteString(" - " + finding.Rationale)
			}
			if finding.SuggestedFollowUp != "" {
				b.WriteString(" Follow-up: " + finding.SuggestedFollowUp)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(report.Gate.Reasons) > 0 {
		b.WriteString("## Gate Reasons\n\n")
		for _, reason := range report.Gate.Reasons {
			b.WriteString("- " + reason + "\n")
		}
	}
	return b.String()
}

func countValidatorStatuses(results []ValidatorResult) map[Status]int {
	counts := map[Status]int{
		StatusPass:    0,
		StatusWarn:    0,
		StatusFail:    0,
		StatusSkipped: 0,
	}
	for _, result := range results {
		counts[result.Status]++
	}
	return counts
}

func countFindingSeverities(findings []Finding) map[Severity]int {
	counts := map[Severity]int{
		SeverityBlocker: 0,
		SeverityWarning: 0,
		SeverityInfo:    0,
	}
	for _, finding := range findings {
		counts[finding.Severity]++
	}
	return counts
}

func SortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if severityRank(findings[i].Severity) != severityRank(findings[j].Severity) {
			return severityRank(findings[i].Severity) < severityRank(findings[j].Severity)
		}
		if findings[i].Category != findings[j].Category {
			return findings[i].Category < findings[j].Category
		}
		return findings[i].Path < findings[j].Path
	})
}

func severityRank(severity Severity) int {
	switch severity {
	case SeverityBlocker:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}
