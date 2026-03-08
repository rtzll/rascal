package main

import (
	"fmt"
	"strings"
)

const (
	reviewSeverityMustFix   = "must_fix"
	reviewSeverityShouldFix = "should_fix"
	reviewSeverityNit       = "nit"
)

type reviewFinding struct {
	Severity        string `json:"severity"`
	Path            string `json:"path,omitempty"`
	Rationale       string `json:"rationale"`
	SuggestedChange string `json:"suggested_change"`
}

type reviewerOutput struct {
	Summary  string          `json:"summary,omitempty"`
	Findings []reviewFinding `json:"findings"`
}

type reviewLoopConfig struct {
	Enabled                     bool
	MaxInitialReviewerPasses    int
	MaxAuthorFixPasses          int
	MaxVerificationReviewerPass int
}

func (c reviewLoopConfig) normalized() reviewLoopConfig {
	out := c
	if out.MaxInitialReviewerPasses < 0 {
		out.MaxInitialReviewerPasses = 0
	}
	if out.MaxAuthorFixPasses < 0 {
		out.MaxAuthorFixPasses = 0
	}
	if out.MaxVerificationReviewerPass < 0 {
		out.MaxVerificationReviewerPass = 0
	}
	return out
}

type reviewPassPhase string

const (
	reviewPassInitial      reviewPassPhase = "initial"
	reviewPassVerification reviewPassPhase = "verification"
)

type reviewPassRecord struct {
	PassNumber   int             `json:"pass_number"`
	Phase        reviewPassPhase `json:"phase"`
	FindingCount int             `json:"finding_count"`
	Summary      string          `json:"summary,omitempty"`
	Findings     []reviewFinding `json:"findings"`
}

type reviewLoopResult struct {
	Config              reviewLoopConfig   `json:"config"`
	Ran                 bool               `json:"ran"`
	ReviewerPasses      int                `json:"reviewer_passes"`
	AuthorFixPasses     int                `json:"author_fix_passes"`
	FixesApplied        bool               `json:"fixes_applied"`
	UnresolvedFindings  bool               `json:"unresolved_findings"`
	BudgetExhausted     bool               `json:"budget_exhausted"`
	VerificationSkipped bool               `json:"verification_skipped"`
	FinalFindingCount   int                `json:"final_finding_count"`
	FinalFindings       []reviewFinding    `json:"final_findings"`
	Passes              []reviewPassRecord `json:"passes"`
}

func (r reviewLoopResult) exitedCleanly() bool {
	return !r.UnresolvedFindings
}

type reviewLoopHooks struct {
	RunReviewerPass  func(passNumber int, phase reviewPassPhase, priorFindings []reviewFinding) (reviewerOutput, error)
	RunAuthorFixPass func(passNumber int, findings []reviewFinding) error
	VerifyAfterFix   func(passNumber int) error
}

type reviewLoopExecutor struct {
	cfg reviewLoopConfig
}

func (e reviewLoopExecutor) Execute(hooks reviewLoopHooks) (reviewLoopResult, error) {
	cfg := e.cfg.normalized()
	result := reviewLoopResult{Config: cfg}
	if !cfg.Enabled {
		return result, nil
	}
	if hooks.RunReviewerPass == nil {
		return result, fmt.Errorf("review loop reviewer hook is required")
	}
	result.Ran = true

	reviewerPassNum := 0
	latest := []reviewFinding{}
	for i := 0; i < cfg.MaxInitialReviewerPasses; i++ {
		reviewerPassNum++
		out, err := hooks.RunReviewerPass(reviewerPassNum, reviewPassInitial, latest)
		if err != nil {
			return result, fmt.Errorf("reviewer pass %d failed: %w", reviewerPassNum, err)
		}
		latest = normalizeFindings(out.Findings)
		result.Passes = append(result.Passes, reviewPassRecord{
			PassNumber:   reviewerPassNum,
			Phase:        reviewPassInitial,
			FindingCount: len(latest),
			Summary:      strings.TrimSpace(out.Summary),
			Findings:     cloneFindings(latest),
		})
		if findingsNeedFix(latest) {
			break
		}
	}

	if !findingsNeedFix(latest) {
		result.ReviewerPasses = len(result.Passes)
		result.FinalFindings = cloneFindings(latest)
		result.FinalFindingCount = len(latest)
		return result, nil
	}

	if hooks.RunAuthorFixPass == nil || cfg.MaxAuthorFixPasses == 0 {
		result.UnresolvedFindings = true
		result.BudgetExhausted = true
		result.ReviewerPasses = len(result.Passes)
		result.FinalFindings = cloneFindings(latest)
		result.FinalFindingCount = len(latest)
		return result, nil
	}

	remainingVerification := cfg.MaxVerificationReviewerPass
	for fixPass := 1; fixPass <= cfg.MaxAuthorFixPasses; fixPass++ {
		if err := hooks.RunAuthorFixPass(fixPass, latest); err != nil {
			return result, fmt.Errorf("author fix pass %d failed: %w", fixPass, err)
		}
		result.AuthorFixPasses++
		result.FixesApplied = true

		if hooks.VerifyAfterFix != nil {
			if err := hooks.VerifyAfterFix(fixPass); err != nil {
				return result, fmt.Errorf("post-fix deterministic checks failed: %w", err)
			}
		}

		if remainingVerification == 0 {
			result.VerificationSkipped = true
			latest = nil
			break
		}

		remainingVerification--
		reviewerPassNum++
		out, err := hooks.RunReviewerPass(reviewerPassNum, reviewPassVerification, latest)
		if err != nil {
			return result, fmt.Errorf("reviewer verification pass %d failed: %w", reviewerPassNum, err)
		}
		latest = normalizeFindings(out.Findings)
		result.Passes = append(result.Passes, reviewPassRecord{
			PassNumber:   reviewerPassNum,
			Phase:        reviewPassVerification,
			FindingCount: len(latest),
			Summary:      strings.TrimSpace(out.Summary),
			Findings:     cloneFindings(latest),
		})
		if !findingsNeedFix(latest) {
			break
		}
	}

	if findingsNeedFix(latest) {
		result.UnresolvedFindings = true
		result.BudgetExhausted = true
	}
	result.ReviewerPasses = len(result.Passes)
	result.FinalFindings = cloneFindings(latest)
	result.FinalFindingCount = len(latest)
	return result, nil
}

func cloneFindings(findings []reviewFinding) []reviewFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]reviewFinding, 0, len(findings))
	out = append(out, findings...)
	return out
}

func findingsNeedFix(findings []reviewFinding) bool {
	for _, finding := range findings {
		switch normalizeSeverity(finding.Severity) {
		case reviewSeverityMustFix, reviewSeverityShouldFix:
			return true
		}
	}
	return false
}

func normalizeFindings(findings []reviewFinding) []reviewFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]reviewFinding, 0, len(findings))
	for _, finding := range findings {
		out = append(out, reviewFinding{
			Severity:        normalizeSeverity(finding.Severity),
			Path:            strings.TrimSpace(finding.Path),
			Rationale:       strings.TrimSpace(finding.Rationale),
			SuggestedChange: strings.TrimSpace(finding.SuggestedChange),
		})
	}
	return out
}

func normalizeSeverity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case reviewSeverityMustFix:
		return reviewSeverityMustFix
	case reviewSeverityShouldFix:
		return reviewSeverityShouldFix
	default:
		return reviewSeverityNit
	}
}
