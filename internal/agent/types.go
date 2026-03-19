package agent

type ModelProvider string

const (
	ModelProviderCodex     ModelProvider = "codex"
	ModelProviderGemini    ModelProvider = "gemini"
	ModelProviderAnthropic ModelProvider = "anthropic"
)
