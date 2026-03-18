package agent

func HarnessModelProvider(harness AgentHarness) ModelProvider {
	switch NormalizeAgentHarness(string(harness)) {
	case AgentHarnessGoose:
		return GooseModelProvider()
	case AgentHarnessCodex:
		return CodexModelProvider()
	case AgentHarnessClaude:
		return ClaudeModelProvider()
	default:
		return ModelProviderCodex
	}
}
