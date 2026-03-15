package runtime

type ModelProvider string

const (
	ModelProviderCodex     ModelProvider = "codex"
	ModelProviderGemini    ModelProvider = "gemini"
	ModelProviderAnthropic ModelProvider = "anthropic"
	ModelProviderPi        ModelProvider = "pi"
)
