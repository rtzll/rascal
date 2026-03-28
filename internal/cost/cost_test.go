package cost

import (
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

type stubUsageTotals struct {
	totals map[string]int64
}

func (s stubUsageTotals) TokensUsed(filter state.RunUsageFilter) (int64, error) {
	if filter.Repo != "" {
		return s.totals["repo:"+filter.Repo], nil
	}
	if filter.Trigger != "" {
		return s.totals["trigger:"+filter.Trigger], nil
	}
	return s.totals["global"], nil
}

func TestEvaluatorChoosesHighestSeverityPolicy(t *testing.T) {
	evaluator := NewEvaluator([]Policy{
		{Scope: ScopeGlobal, DailyTokenBudget: 1_000, SoftLimitTokens: 800, SoftAction: ActionWarn, HardAction: ActionReject},
		{Scope: ScopeRepo, Match: "owner/repo", DailyTokenBudget: 200, HardAction: ActionDefer},
	}, stubUsageTotals{
		totals: map[string]int64{
			"global":          850,
			"repo:owner/repo": 250,
		},
	})

	decision, err := evaluator.Evaluate(time.Date(2026, 3, 14, 13, 0, 0, 0, time.UTC), RunContext{
		Repo:             "owner/repo",
		Trigger:          "issue_label",
		ExecutionProfile: state.ExecutionProfileDefault,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision.Action != ActionDefer {
		t.Fatalf("decision.Action = %q, want defer", decision.Action)
	}
	if decision.NextEligibleAt == nil || !decision.NextEligibleAt.Equal(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("next eligible = %v, want 2026-03-15T00:00:00Z", decision.NextEligibleAt)
	}
}

func TestEvaluatorRejectsAtHardLimit(t *testing.T) {
	evaluator := NewEvaluator([]Policy{
		{Scope: ScopeGlobal, DailyTokenBudget: 100, HardAction: ActionReject},
	}, stubUsageTotals{totals: map[string]int64{"global": 100}})

	decision, err := evaluator.Evaluate(time.Date(2026, 3, 14, 1, 0, 0, 0, time.UTC), RunContext{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision.Action != ActionReject {
		t.Fatalf("decision.Action = %q, want reject", decision.Action)
	}
}
