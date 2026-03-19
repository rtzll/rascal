package agent

func HarnessModelProvider(harness AgentHarness) ModelProvider {
	switch NormalizeAgentHarness(string(harness)) {
	case AgentHarnessGooseCodex:
		return GooseCodexModelProvider()
	case AgentHarnessCodex:
		return CodexModelProvider()
	case AgentHarnessClaude:
		return ClaudeModelProvider()
	case AgentHarnessGooseClaude:
		return GooseClaudeModelProvider()
	default:
		return ModelProviderCodex
	}
}
