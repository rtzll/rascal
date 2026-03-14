package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rtzll/rascal/internal/validation"
)

const validationLogsDir = "validation"

func runValidationStage(ex commandExecutor, cfg config) (validation.Report, error) {
	if !cfg.Validation.Enabled {
		report := validation.NewDisabledReport()
		if err := validation.WriteArtifacts(cfg.MetaDir, report); err != nil {
			return report, fmt.Errorf("write disabled validation artifacts: %w", err)
		}
		return report, nil
	}
	if err := os.MkdirAll(filepath.Join(cfg.MetaDir, validationLogsDir), 0o755); err != nil {
		return validation.Report{}, fmt.Errorf("create validation log dir: %w", err)
	}

	results := []validation.ValidatorResult{
		runMakeValidator(ex, cfg, "lint"),
		runMakeValidator(ex, cfg, "test"),
		runMakeValidator(ex, cfg, "verify"),
	}

	critique := validation.CritiqueReport{
		Enabled:            cfg.Validation.CritiqueEnabled,
		Ran:                false,
		TestCritiqueEnable: cfg.Validation.TestCritiqueEnable,
	}
	findings := []validation.Finding(nil)
	if !cfg.Validation.CritiqueEnabled {
		critique.SkippedReason = "critique disabled"
	} else if cfg.Validation.Gate.BlockOnDeterministicFailure && hasDeterministicFailure(results) {
		critique.SkippedReason = "deterministic failures"
	} else {
		var err error
		findings, err = generateCritiqueFindings(ex, cfg, results)
		if err != nil {
			log.Printf("[%s] validation critique skipped: %v", nowUTC(), err)
			critique.SkippedReason = err.Error()
		} else {
			critique.Ran = true
			validation.SortFindings(findings)
		}
	}

	report := validation.BuildReport(cfg.Validation, results, critique, findings)
	if err := validation.WriteArtifacts(cfg.MetaDir, report); err != nil {
		return report, fmt.Errorf("write validation artifacts: %w", err)
	}
	if report.Gate.Blocked {
		return report, errors.New(report.Gate.Summary)
	}
	return report, nil
}

func runMakeValidator(ex commandExecutor, cfg config, target string) validation.ValidatorResult {
	result := validation.ValidatorResult{
		Name:    target,
		Command: "make " + target,
		Source:  "make",
	}

	makefilePath := filepath.Join(cfg.RepoDir, "Makefile")
	if _, err := os.Stat(makefilePath); errors.Is(err, os.ErrNotExist) {
		result.Status = validation.StatusSkipped
		result.Summary = "Makefile not found."
		return result
	}
	if err := ex.LookPath("make"); err != nil {
		result.Status = validation.StatusFail
		result.Summary = "make is required to run configured validators."
		exitCode := 127
		result.ExitCode = &exitCode
		return result
	}

	out, err := ex.CombinedOutput(cfg.RepoDir, nil, "make", target)
	output := strings.TrimSpace(out)
	if err == nil {
		result.Status = validation.StatusPass
		result.Summary = summarizeValidationOutput(output, "completed successfully")
		result.DetailsPath = writeValidatorOutput(cfg, target, output)
		return result
	}

	combinedErr := firstNonEmptyLine(output + "\n" + err.Error())
	if isMissingMakeTarget(output, err) {
		result.Status = validation.StatusSkipped
		result.Summary = fmt.Sprintf("Target %q is not available.", target)
		return result
	}

	exitCode := commandExitCode(err)
	result.ExitCode = &exitCode
	result.Status = validation.StatusFail
	result.Summary = summarizeValidationOutput(output, combinedErr)
	result.DetailsPath = writeValidatorOutput(cfg, target, output+"\n"+err.Error())
	return result
}

func hasDeterministicFailure(results []validation.ValidatorResult) bool {
	for _, result := range results {
		if result.Status == validation.StatusFail {
			return true
		}
	}
	return false
}

func summarizeValidationOutput(output, fallback string) string {
	summary := firstNonEmptyLine(output)
	if summary == "" {
		summary = strings.TrimSpace(fallback)
	}
	summary = strings.Join(strings.Fields(summary), " ")
	if summary == "" {
		summary = "completed"
	}
	const maxLen = 180
	if len(summary) > maxLen {
		return summary[:maxLen-3] + "..."
	}
	return summary
}

func writeValidatorOutput(cfg config, name, output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	relPath := filepath.Join(validationLogsDir, name+".log")
	path := filepath.Join(cfg.MetaDir, relPath)
	if err := os.WriteFile(path, []byte(output+"\n"), 0o644); err != nil {
		log.Printf("[%s] write validator output %s failed: %v", nowUTC(), name, err)
		return ""
	}
	return relPath
}

func isMissingMakeTarget(output string, err error) bool {
	text := strings.ToLower(strings.TrimSpace(output + "\n" + err.Error()))
	return strings.Contains(text, "no rule to make target") || strings.Contains(text, "no targets specified and no makefile found")
}

func commandExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func generateCritiqueFindings(ex commandExecutor, cfg config, results []validation.ValidatorResult) ([]validation.Finding, error) {
	changedFiles, err := gitDiffNameOnly(ex, cfg)
	if err != nil {
		return nil, fmt.Errorf("load changed files: %w", err)
	}
	diffText, err := gitDiffPatch(ex, cfg)
	if err != nil {
		return nil, fmt.Errorf("load changed diff: %w", err)
	}

	changedTests := make([]string, 0)
	changedProd := make([]string, 0)
	for _, path := range changedFiles {
		switch {
		case isTestPath(path):
			changedTests = append(changedTests, path)
		case isProductionCodePath(path):
			changedProd = append(changedProd, path)
		}
	}

	findings := make([]validation.Finding, 0)
	if len(changedTests) > 0 {
		testResult, ok := validatorByName(results, "test")
		if !ok || testResult.Status != validation.StatusPass {
			findings = append(findings, validation.Finding{
				Source:            "test_critique",
				Severity:          validation.SeverityBlocker,
				Category:          "tests",
				Path:              changedTests[0],
				Rationale:         "Changed tests were not confirmed by a passing test validator.",
				SuggestedFollowUp: "Run the test validator and confirm the new or modified tests execute.",
				RelatedValidator:  "test",
			})
		}
	}

	for _, path := range addedSkippedTests(diffText) {
		findings = append(findings, validation.Finding{
			Source:            "test_critique",
			Severity:          validation.SeverityBlocker,
			Category:          "tests",
			Path:              path,
			Rationale:         "The diff adds a skipped test, which weakens validation coverage.",
			SuggestedFollowUp: "Remove the skip or document why the test must remain disabled.",
			RelatedValidator:  "test",
		})
	}

	changedTestsByDir := make(map[string]struct{}, len(changedTests))
	for _, path := range changedTests {
		changedTestsByDir[filepath.Dir(path)] = struct{}{}
	}
	for _, path := range changedProd {
		if _, ok := changedTestsByDir[filepath.Dir(path)]; ok {
			continue
		}
		findings = append(findings, validation.Finding{
			Source:            "test_critique",
			Severity:          validation.SeverityWarning,
			Category:          "tests",
			Path:              path,
			Rationale:         "Changed production code has no nearby test update in the same directory.",
			SuggestedFollowUp: "Confirm existing tests cover this change or add targeted coverage.",
			RelatedValidator:  "test",
		})
	}

	if strings.Contains(strings.ToLower(cfg.Task), "test") && len(changedTests) == 0 {
		findings = append(findings, validation.Finding{
			Source:            "critique",
			Severity:          validation.SeverityInfo,
			Category:          "tests",
			Rationale:         "The task mentions tests, but the diff does not modify test files.",
			SuggestedFollowUp: "Confirm whether test updates were intentionally unnecessary.",
			RelatedValidator:  "test",
		})
	}

	return findings, nil
}

func validatorByName(results []validation.ValidatorResult, name string) (validation.ValidatorResult, bool) {
	for _, result := range results {
		if result.Name == name {
			return result, true
		}
	}
	return validation.ValidatorResult{}, false
}

func gitDiffNameOnly(ex commandExecutor, cfg config) ([]string, error) {
	out, err := runCommand(ex, cfg.RepoDir, nil, "git", "diff", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

func gitDiffPatch(ex commandExecutor, cfg config) (string, error) {
	return runCommand(ex, cfg.RepoDir, nil, "git", "diff", "--unified=0", "HEAD")
}

func addedSkippedTests(diffText string) []string {
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	currentPath := ""
	for _, line := range strings.Split(diffText, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			currentPath = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			if !isTestPath(currentPath) {
				continue
			}
			text := strings.ToLower(strings.TrimSpace(line[1:]))
			if strings.Contains(text, "t.skip(") || strings.Contains(text, "t.skipnow(") || strings.Contains(text, ".skip(") {
				if _, ok := seen[currentPath]; ok {
					continue
				}
				seen[currentPath] = struct{}{}
				paths = append(paths, currentPath)
			}
		}
	}
	return paths
}

func isTestPath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	base := filepath.Base(path)
	return strings.HasSuffix(base, "_test.go") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		strings.Contains(path, "/test/") ||
		strings.Contains(path, "/tests/") ||
		strings.HasPrefix(path, "test/") ||
		strings.HasPrefix(path, "tests/")
}

func isProductionCodePath(path string) bool {
	if path == "" || isTestPath(path) {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".py", ".rb", ".java", ".kt", ".rs", ".c", ".cc", ".cpp", ".h", ".hpp", ".cs":
		return true
	default:
		return false
	}
}

func loadValidationSummaryLine(metaDir string) string {
	report, err := validation.ReadReport(filepath.Join(strings.TrimSpace(metaDir), validation.DefaultJSONFile))
	if err != nil {
		return ""
	}
	return validation.SummaryLine(report)
}
