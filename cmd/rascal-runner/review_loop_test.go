package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewLoopExecutorNoFindings(t *testing.T) {
	loop := reviewLoopExecutor{cfg: reviewLoopConfig{
		Enabled:                     true,
		MaxInitialReviewerPasses:    1,
		MaxAuthorFixPasses:          1,
		MaxVerificationReviewerPass: 1,
	}}
	reviewerCalls := 0
	result, err := loop.Execute(reviewLoopHooks{
		RunReviewerPass: func(_ int, _ reviewPassPhase, _ []reviewFinding) (reviewerOutput, error) {
			reviewerCalls++
			return reviewerOutput{Summary: "No findings", Findings: nil}, nil
		},
		RunAuthorFixPass: func(_ int, _ []reviewFinding) error {
			t.Fatal("did not expect author fix pass")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Ran {
		t.Fatal("expected review loop to run")
	}
	if reviewerCalls != 1 {
		t.Fatalf("reviewerCalls = %d, want 1", reviewerCalls)
	}
	if result.AuthorFixPasses != 0 {
		t.Fatalf("AuthorFixPasses = %d, want 0", result.AuthorFixPasses)
	}
	if result.UnresolvedFindings {
		t.Fatal("expected no unresolved findings")
	}
}

func TestReviewLoopExecutorFindingsFixAndVerify(t *testing.T) {
	loop := reviewLoopExecutor{cfg: reviewLoopConfig{
		Enabled:                     true,
		MaxInitialReviewerPasses:    1,
		MaxAuthorFixPasses:          1,
		MaxVerificationReviewerPass: 1,
	}}
	reviewerCalls := 0
	fixCalls := 0
	result, err := loop.Execute(reviewLoopHooks{
		RunReviewerPass: func(pass int, phase reviewPassPhase, _ []reviewFinding) (reviewerOutput, error) {
			reviewerCalls++
			if pass == 1 && phase == reviewPassInitial {
				return reviewerOutput{
					Summary: "needs fix",
					Findings: []reviewFinding{{
						Severity:        reviewSeverityMustFix,
						Path:            "main.go",
						Rationale:       "bug",
						SuggestedChange: "patch",
					}},
				}, nil
			}
			return reviewerOutput{Summary: "clean", Findings: nil}, nil
		},
		RunAuthorFixPass: func(_ int, findings []reviewFinding) error {
			fixCalls++
			if len(findings) != 1 {
				t.Fatalf("expected one finding in fix pass, got %d", len(findings))
			}
			return nil
		},
		VerifyAfterFix: func(_ int) error { return nil },
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if reviewerCalls != 2 {
		t.Fatalf("reviewerCalls = %d, want 2", reviewerCalls)
	}
	if fixCalls != 1 {
		t.Fatalf("fixCalls = %d, want 1", fixCalls)
	}
	if result.UnresolvedFindings {
		t.Fatal("expected resolved findings")
	}
	if !result.FixesApplied {
		t.Fatal("expected fixes_applied=true")
	}
}

func TestReviewLoopExecutorBudgetExhausted(t *testing.T) {
	loop := reviewLoopExecutor{cfg: reviewLoopConfig{
		Enabled:                     true,
		MaxInitialReviewerPasses:    1,
		MaxAuthorFixPasses:          0,
		MaxVerificationReviewerPass: 1,
	}}
	result, err := loop.Execute(reviewLoopHooks{
		RunReviewerPass: func(_ int, _ reviewPassPhase, _ []reviewFinding) (reviewerOutput, error) {
			return reviewerOutput{
				Findings: []reviewFinding{{Severity: reviewSeverityShouldFix, Rationale: "cleanup", SuggestedChange: "rename"}},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.UnresolvedFindings {
		t.Fatal("expected unresolved findings")
	}
	if !result.BudgetExhausted {
		t.Fatal("expected budget exhausted")
	}
}

func TestReviewLoopExecutorBoundsRepeatedFindings(t *testing.T) {
	loop := reviewLoopExecutor{cfg: reviewLoopConfig{
		Enabled:                     true,
		MaxInitialReviewerPasses:    1,
		MaxAuthorFixPasses:          1,
		MaxVerificationReviewerPass: 1,
	}}
	reviewerCalls := 0
	fixCalls := 0
	result, err := loop.Execute(reviewLoopHooks{
		RunReviewerPass: func(_ int, _ reviewPassPhase, _ []reviewFinding) (reviewerOutput, error) {
			reviewerCalls++
			return reviewerOutput{
				Findings: []reviewFinding{{Severity: reviewSeverityMustFix, Rationale: "still broken", SuggestedChange: "fix"}},
			}, nil
		},
		RunAuthorFixPass: func(_ int, _ []reviewFinding) error {
			fixCalls++
			return nil
		},
		VerifyAfterFix: func(_ int) error { return nil },
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if reviewerCalls != 2 {
		t.Fatalf("reviewerCalls = %d, want 2", reviewerCalls)
	}
	if fixCalls != 1 {
		t.Fatalf("fixCalls = %d, want 1", fixCalls)
	}
	if !result.UnresolvedFindings {
		t.Fatal("expected unresolved findings after bounded retries")
	}
}

func TestRunDeterministicChecksFailureIsTerminalForRequiredChecks(t *testing.T) {
	cfg := config{
		RepoDir:                    t.TempDir(),
		DeterministicCheckCommands: []string{"go test ./..."},
	}
	run, err := runDeterministicChecks(fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, _ ...string) (string, error) {
			if name == "bash" {
				return "tests failed", errors.New("exit 1")
			}
			return "", nil
		},
	}, cfg, "post_author")
	if err == nil {
		t.Fatal("expected deterministic check error")
	}
	if run.Passed {
		t.Fatal("expected run.Passed=false")
	}
	if len(run.Checks) != 1 || !run.Checks[0].TerminalFail {
		t.Fatalf("expected terminal failed check, got %+v", run.Checks)
	}
}

func TestRunWithExecutorReviewLoopNoFindingsWritesArtifacts(t *testing.T) {
	metaDir, workRoot, repoDir := setupRunnerTestEnv(t, "run_review_clean")
	t.Setenv("RASCAL_REVIEW_LOOP_ENABLED", "true")

	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "user" {
				return `{"login":"rascalbot"}`, nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return `{"number":11,"url":"https://github.com/owner/repo/pull/11"}`, nil
			}
			if name == "git" && len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
				return "", nil
			}
			if name == "git" && len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
				return "0123456789abcdef0123456789abcdef01234567", nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				return nil
			}
			inst := instructionPathFromGooseArgs(args)
			if strings.Contains(inst, "review-instructions.pass-1.md") {
				findingsPath := filepath.Join(metaDir, "review-findings.pass-1.json")
				if err := os.WriteFile(findingsPath, []byte(`{"summary":"No findings","findings":[]}`), 0o644); err != nil {
					return fmt.Errorf("write reviewer findings: %w", err)
				}
				if err := os.WriteFile(filepath.Join(metaDir, "review-summary.pass-1.md"), []byte("No findings"), 0o644); err != nil {
					return fmt.Errorf("write reviewer summary: %w", err)
				}
			}
			if _, err := io.WriteString(stdout, `{"event":"message","usage":{"total_tokens":5}}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose log: %w", err)
			}
			return nil
		},
	}

	if err := runWithExecutor(ex); err != nil {
		t.Fatalf("runWithExecutor returned error: %v", err)
	}

	metaData, err := os.ReadFile(filepath.Join(metaDir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta struct {
		ReviewLoopRan      bool `json:"review_loop_ran"`
		ReviewFindingCount int  `json:"review_finding_count"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if !meta.ReviewLoopRan {
		t.Fatal("expected review_loop_ran=true")
	}
	if meta.ReviewFindingCount != 0 {
		t.Fatalf("review_finding_count = %d, want 0", meta.ReviewFindingCount)
	}
	for _, path := range []string{
		filepath.Join(metaDir, "review-loop.json"),
		filepath.Join(metaDir, "review-findings.json"),
		filepath.Join(metaDir, "review-summary.md"),
		filepath.Join(metaDir, "deterministic-checks.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}
	_ = workRoot
	_ = repoDir
}

func TestRunWithExecutorReviewLoopFindingsThenFix(t *testing.T) {
	metaDir, _, _ := setupRunnerTestEnv(t, "run_review_fix")
	t.Setenv("RASCAL_REVIEW_LOOP_ENABLED", "true")

	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "user" {
				return `{"login":"rascalbot"}`, nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return `{"number":12,"url":"https://github.com/owner/repo/pull/12"}`, nil
			}
			if name == "git" && len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
				return "", nil
			}
			if name == "git" && len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
				return "89abcdef0123456789abcdef0123456789abcdef", nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				return nil
			}
			inst := instructionPathFromGooseArgs(args)
			switch {
			case strings.Contains(inst, "review-instructions.pass-1.md"):
				if err := os.WriteFile(filepath.Join(metaDir, "review-findings.pass-1.json"), []byte(`{"summary":"needs fix","findings":[{"severity":"must_fix","path":"main.go","rationale":"bug","suggested_change":"add guard"}]}`), 0o644); err != nil {
					return fmt.Errorf("write reviewer findings pass 1: %w", err)
				}
				if err := os.WriteFile(filepath.Join(metaDir, "review-summary.pass-1.md"), []byte("needs fix"), 0o644); err != nil {
					return fmt.Errorf("write reviewer summary pass 1: %w", err)
				}
			case strings.Contains(inst, "review-instructions.pass-2.md"):
				if err := os.WriteFile(filepath.Join(metaDir, "review-findings.pass-2.json"), []byte(`{"summary":"clean","findings":[]}`), 0o644); err != nil {
					return fmt.Errorf("write reviewer findings pass 2: %w", err)
				}
				if err := os.WriteFile(filepath.Join(metaDir, "review-summary.pass-2.md"), []byte("clean"), 0o644); err != nil {
					return fmt.Errorf("write reviewer summary pass 2: %w", err)
				}
			}
			if _, err := io.WriteString(stdout, `{"event":"message","usage":{"total_tokens":6}}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose log: %w", err)
			}
			return nil
		},
	}

	if err := runWithExecutor(ex); err != nil {
		t.Fatalf("runWithExecutor returned error: %v", err)
	}

	metaData, err := os.ReadFile(filepath.Join(metaDir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta struct {
		ReviewFixesApplied       bool `json:"review_fixes_applied"`
		ReviewReviewerPasses     int  `json:"review_reviewer_passes"`
		ReviewAuthorFixPasses    int  `json:"review_author_fix_passes"`
		ReviewUnresolvedFindings bool `json:"review_unresolved_findings"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if !meta.ReviewFixesApplied {
		t.Fatal("expected fixes applied")
	}
	if meta.ReviewReviewerPasses != 2 {
		t.Fatalf("review_reviewer_passes = %d, want 2", meta.ReviewReviewerPasses)
	}
	if meta.ReviewAuthorFixPasses != 1 {
		t.Fatalf("review_author_fix_passes = %d, want 1", meta.ReviewAuthorFixPasses)
	}
	if meta.ReviewUnresolvedFindings {
		t.Fatal("expected unresolved findings=false")
	}
}

func TestRunWithExecutorReviewLoopBudgetExhausted(t *testing.T) {
	metaDir, _, _ := setupRunnerTestEnv(t, "run_review_exhausted")
	t.Setenv("RASCAL_REVIEW_LOOP_ENABLED", "true")
	t.Setenv("RASCAL_REVIEW_MAX_FIX_PASSES", "0")

	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "user" {
				return `{"login":"rascalbot"}`, nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return `{"number":13,"url":"https://github.com/owner/repo/pull/13"}`, nil
			}
			if name == "git" && len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
				return "", nil
			}
			if name == "git" && len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
				return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				return nil
			}
			inst := instructionPathFromGooseArgs(args)
			if strings.Contains(inst, "review-instructions.pass-1.md") {
				if err := os.WriteFile(filepath.Join(metaDir, "review-findings.pass-1.json"), []byte(`{"summary":"needs fix","findings":[{"severity":"should_fix","path":"x.go","rationale":"cleanup","suggested_change":"rename var"}]}`), 0o644); err != nil {
					return fmt.Errorf("write reviewer findings: %w", err)
				}
				if err := os.WriteFile(filepath.Join(metaDir, "review-summary.pass-1.md"), []byte("needs fix"), 0o644); err != nil {
					return fmt.Errorf("write reviewer summary: %w", err)
				}
			}
			if _, err := io.WriteString(stdout, `{"event":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose log: %w", err)
			}
			return nil
		},
	}

	if err := runWithExecutor(ex); err != nil {
		t.Fatalf("runWithExecutor returned error: %v", err)
	}
	metaData, err := os.ReadFile(filepath.Join(metaDir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta struct {
		ReviewUnresolvedFindings bool `json:"review_unresolved_findings"`
		ReviewAuthorFixPasses    int  `json:"review_author_fix_passes"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if !meta.ReviewUnresolvedFindings {
		t.Fatal("expected unresolved findings")
	}
	if meta.ReviewAuthorFixPasses != 0 {
		t.Fatalf("review_author_fix_passes = %d, want 0", meta.ReviewAuthorFixPasses)
	}
}

func TestRunWithExecutorDeterministicCheckFailureSkipsReviewLoop(t *testing.T) {
	metaDir, _, _ := setupRunnerTestEnv(t, "run_review_check_fail")
	t.Setenv("RASCAL_REVIEW_LOOP_ENABLED", "true")
	t.Setenv("RASCAL_DETERMINISTIC_CHECK_COMMANDS", "go test ./...")

	reviewerCalled := false
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "user" {
				return `{"login":"rascalbot"}`, nil
			}
			if name == "bash" {
				return "tests failed", errors.New("exit 1")
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, _ io.Writer, _ io.Writer, name string, args ...string) error {
			if name == "goose" && strings.Contains(instructionPathFromGooseArgs(args), "review-instructions.") {
				reviewerCalled = true
			}
			return nil
		},
	}

	err := runWithExecutor(ex)
	if err == nil || !strings.Contains(err.Error(), "custom_check_1 failed") {
		t.Fatalf("expected deterministic check failure, got: %v", err)
	}
	if reviewerCalled {
		t.Fatal("reviewer should not run when deterministic checks fail")
	}
	if _, statErr := os.Stat(filepath.Join(metaDir, "review-loop.json")); !os.IsNotExist(statErr) {
		t.Fatalf("did not expect review-loop artifact on check failure, stat err=%v", statErr)
	}
}

func setupRunnerTestEnv(t *testing.T, runID string) (metaDir, workRoot, repoDir string) {
	t.Helper()
	metaDir = filepath.Join(t.TempDir(), "meta")
	workRoot = filepath.Join(t.TempDir(), "work")
	repoDir = filepath.Join(workRoot, "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo git dir: %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}
	t.Setenv("RASCAL_RUN_ID", runID)
	t.Setenv("RASCAL_TASK_ID", "task_"+runID)
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_TASK", "Address feedback")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_REPO_DIR", repoDir)
	t.Setenv("RASCAL_AGENT_BACKEND", "goose")
	t.Setenv("RASCAL_GOOSE_DEBUG", "false")
	t.Setenv("RASCAL_DETERMINISTIC_CHECK_COMMANDS", "")
	t.Setenv("RASCAL_REVIEW_MAX_INITIAL_PASSES", "1")
	t.Setenv("RASCAL_REVIEW_MAX_FIX_PASSES", "1")
	t.Setenv("RASCAL_REVIEW_MAX_VERIFICATION_PASSES", "1")
	return metaDir, workRoot, repoDir
}

func instructionPathFromGooseArgs(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-i" {
			return args[i+1]
		}
	}
	return ""
}
