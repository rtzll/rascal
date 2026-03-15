package agent

// AgentHarness is the tool wrapper the worker invokes for a run.
type AgentHarness = Backend

const (
	AgentHarnessGoose = BackendGoose
	AgentHarnessCodex = BackendCodex
)

type ModelProvider string

const (
	ModelProviderCodex  ModelProvider = "codex"
	ModelProviderGemini ModelProvider = "gemini"
)

func ParseAgentHarness(raw string) (AgentHarness, error) {
	return ParseBackend(raw)
}

func NormalizeAgentHarness(raw string) AgentHarness {
	return NormalizeBackend(raw)
}
