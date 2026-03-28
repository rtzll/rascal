package cost

import (
	"fmt"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

type Action string

const (
	ActionAllow  Action = "allow"
	ActionWarn   Action = "warn"
	ActionDefer  Action = "defer"
	ActionReject Action = "reject"
)

func NormalizeAction(raw string) Action {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ActionWarn):
		return ActionWarn
	case string(ActionDefer):
		return ActionDefer
	case string(ActionReject):
		return ActionReject
	default:
		return ActionAllow
	}
}

type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeRepo    Scope = "repo"
	ScopeTrigger Scope = "trigger"
)

func NormalizeScope(raw string) Scope {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ScopeRepo):
		return ScopeRepo
	case "trigger-class", string(ScopeTrigger):
		return ScopeTrigger
	default:
		return ScopeGlobal
	}
}

type Policy struct {
	Name             string `json:"name,omitempty"`
	Scope            Scope  `json:"scope"`
	Match            string `json:"match,omitempty"`
	DailyTokenBudget int64  `json:"daily_token_budget"`
	SoftLimitTokens  int64  `json:"soft_limit_tokens,omitempty"`
	SoftAction       Action `json:"soft_action,omitempty"`
	HardAction       Action `json:"hard_action,omitempty"`
}

func (p Policy) Normalized() Policy {
	p.Name = strings.TrimSpace(p.Name)
	p.Scope = NormalizeScope(string(p.Scope))
	p.Match = strings.TrimSpace(p.Match)
	if p.Scope == ScopeRepo {
		p.Match = state.NormalizeRepo(p.Match)
	}
	p.SoftAction = NormalizeAction(string(p.SoftAction))
	p.HardAction = NormalizeAction(string(p.HardAction))
	if p.HardAction == ActionAllow {
		p.HardAction = ActionReject
	}
	return p
}

func (p Policy) Validate() error {
	p = p.Normalized()
	if p.DailyTokenBudget <= 0 {
		return fmt.Errorf("daily token budget must be greater than zero")
	}
	if p.Scope != ScopeGlobal && p.Match == "" {
		return fmt.Errorf("match is required for %s policy", p.Scope)
	}
	if p.SoftLimitTokens < 0 {
		return fmt.Errorf("soft limit tokens must be zero or positive")
	}
	if p.SoftLimitTokens > 0 && p.SoftLimitTokens > p.DailyTokenBudget {
		return fmt.Errorf("soft limit tokens cannot exceed daily token budget")
	}
	return nil
}

type RunContext struct {
	Repo             string
	Trigger          string
	ExecutionProfile state.ExecutionProfile
}

type Window struct {
	Start time.Time
	End   time.Time
}

func DailyUTCWindow(now time.Time) Window {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return Window{Start: start, End: start.Add(24 * time.Hour)}
}

type UsageTotals interface {
	TokensUsed(filter state.RunUsageFilter) (int64, error)
}

type Decision struct {
	Action         Action
	Policy         Policy
	UsedTokens     int64
	Window         Window
	Reason         string
	NextEligibleAt *time.Time
}

type Evaluator struct {
	policies []Policy
	usage    UsageTotals
}

func NewEvaluator(policies []Policy, usage UsageTotals) *Evaluator {
	normalized := make([]Policy, 0, len(policies))
	for _, policy := range policies {
		policy = policy.Normalized()
		if policy.DailyTokenBudget <= 0 {
			continue
		}
		normalized = append(normalized, policy)
	}
	return &Evaluator{policies: normalized, usage: usage}
}

func (e *Evaluator) Evaluate(now time.Time, run RunContext) (Decision, error) {
	window := DailyUTCWindow(now)
	best := Decision{Action: ActionAllow, Window: window}
	for _, policy := range e.policies {
		if !matchesPolicy(policy, run) {
			continue
		}
		used, err := e.usage.TokensUsed(state.RunUsageFilter{
			Repo:    policyRepo(policy, run),
			Trigger: policyTrigger(policy, run),
			Since:   window.Start,
			Until:   window.End,
		})
		if err != nil {
			return Decision{}, fmt.Errorf("load usage for %s policy: %w", policy.Scope, err)
		}
		action := ActionAllow
		threshold := int64(0)
		if policy.SoftLimitTokens > 0 && used >= policy.SoftLimitTokens {
			action = policy.SoftAction
			threshold = policy.SoftLimitTokens
		}
		if used >= policy.DailyTokenBudget {
			action = policy.HardAction
			threshold = policy.DailyTokenBudget
		}
		candidate := Decision{
			Action:     action,
			Policy:     policy,
			UsedTokens: used,
			Window:     window,
			Reason:     buildReason(policy, action, used, threshold, window),
		}
		if action == ActionDefer {
			next := window.End
			candidate.NextEligibleAt = &next
		}
		if actionRank(candidate.Action) > actionRank(best.Action) {
			best = candidate
		}
	}
	return best, nil
}

func matchesPolicy(policy Policy, run RunContext) bool {
	switch policy.Scope {
	case ScopeRepo:
		return state.NormalizeRepo(run.Repo) == policy.Match
	case ScopeTrigger:
		return strings.EqualFold(strings.TrimSpace(run.Trigger), policy.Match)
	default:
		return true
	}
}

func policyRepo(policy Policy, run RunContext) string {
	if policy.Scope == ScopeRepo {
		return policy.Match
	}
	return ""
}

func policyTrigger(policy Policy, run RunContext) string {
	if policy.Scope == ScopeTrigger {
		return policy.Match
	}
	return ""
}

func actionRank(action Action) int {
	switch action {
	case ActionReject:
		return 3
	case ActionDefer:
		return 2
	case ActionWarn:
		return 1
	default:
		return 0
	}
}

func buildReason(policy Policy, action Action, usedTokens, threshold int64, window Window) string {
	if action == ActionAllow {
		return ""
	}
	label := string(policy.Scope)
	if policy.Match != "" {
		label += "=" + policy.Match
	}
	return fmt.Sprintf("%s policy %q used %d/%d tokens for %s", action, label, usedTokens, threshold, window.Start.Format("2006-01-02"))
}
