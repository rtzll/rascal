package validation

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateGateBlocksDeterministicFailures(t *testing.T) {
	decision := EvaluateGate([]ValidatorResult{
		{Name: "test", Status: StatusFail},
	}, nil, GatePolicy{
		BlockOnDeterministicFailure: true,
	})
	if !decision.Blocked {
		t.Fatal("expected deterministic failure to block")
	}
	if !strings.Contains(decision.Summary, "deterministic failures: test") {
		t.Fatalf("unexpected summary: %q", decision.Summary)
	}
}

func TestEvaluateGateBlocksCritiqueBlockers(t *testing.T) {
	decision := EvaluateGate(nil, []Finding{
		{Severity: SeverityBlocker, Category: "tests"},
	}, GatePolicy{
		BlockOnCritiqueBlocker: true,
	})
	if !decision.Blocked {
		t.Fatal("expected critique blocker to block")
	}
	if !strings.Contains(decision.Summary, "critique blockers: 1") {
		t.Fatalf("unexpected summary: %q", decision.Summary)
	}
}

func TestEvaluateGateAllowsWarningsByDefault(t *testing.T) {
	decision := EvaluateGate(nil, []Finding{
		{Severity: SeverityWarning, Category: "tests"},
	}, GatePolicy{})
	if decision.Blocked {
		t.Fatalf("did not expect warning-only critique to block: %+v", decision)
	}
	if !strings.Contains(decision.Summary, "passed with 1 warning") {
		t.Fatalf("unexpected summary: %q", decision.Summary)
	}
}

func TestWriteArtifacts(t *testing.T) {
	dir := t.TempDir()
	report := BuildReport(DefaultConfig(), []ValidatorResult{
		{Name: "lint", Status: StatusPass, Summary: "ok", DetailsPath: filepath.Join("validation", "lint.log")},
	}, CritiqueReport{
		Enabled:            true,
		Ran:                true,
		TestCritiqueEnable: true,
	}, []Finding{
		{Severity: SeverityWarning, Category: "tests", Path: "pkg/foo.go", Rationale: "needs coverage"},
	})

	if err := WriteArtifacts(dir, report); err != nil {
		t.Fatalf("WriteArtifacts returned error: %v", err)
	}

	got, err := ReadReport(filepath.Join(dir, DefaultJSONFile))
	if err != nil {
		t.Fatalf("ReadReport returned error: %v", err)
	}
	if got.Summary.ValidatorCount != 1 {
		t.Fatalf("validator_count = %d, want 1", got.Summary.ValidatorCount)
	}
	markdown := RenderMarkdown(got)
	if !strings.Contains(markdown, "`warning/tests` `pkg/foo.go` - needs coverage") {
		t.Fatalf("expected finding in markdown:\n%s", markdown)
	}
}
