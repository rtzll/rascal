package agent

// AgentHarness is the tool wrapper the worker invokes for a run.
type AgentHarness = Runtime

// AgentRuntime is the selected agent runtime for a run.
type AgentRuntime = Runtime

const (
	AgentHarnessGoose       = RuntimeGoose
	AgentHarnessCodex       = RuntimeCodex
	AgentHarnessClaude      = RuntimeClaude
	AgentHarnessGooseClaude = RuntimeGooseClaude

	AgentRuntimeGoose       = RuntimeGoose
	AgentRuntimeCodex       = RuntimeCodex
	AgentRuntimeClaude      = RuntimeClaude
	AgentRuntimeGooseClaude = RuntimeGooseClaude
)

type ModelProvider string

const (
	ModelProviderCodex     ModelProvider = "codex"
	ModelProviderGemini    ModelProvider = "gemini"
	ModelProviderAnthropic ModelProvider = "anthropic"
)

func ParseAgentHarness(raw string) (AgentHarness, error) {
	return ParseRuntime(raw)
}

func NormalizeAgentHarness(raw string) AgentHarness {
	return NormalizeRuntime(raw)
}
