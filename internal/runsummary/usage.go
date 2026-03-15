package runsummary

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type TokenUsage struct {
	Provider              string
	Model                 string
	TotalTokens           int64
	InputTokens           *int64
	OutputTokens          *int64
	CachedInputTokens     *int64
	ReasoningOutputTokens *int64
	RawUsageJSON          string
}

type usageCandidate struct {
	TokenUsage
	priority int
}

type jsonObject map[string]json.RawMessage

func ExtractTokenUsage(agentOutput string) (TokenUsage, bool) {
	scanner := bufio.NewScanner(strings.NewReader(agentOutput))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		best     usageCandidate
		found    bool
		lastMeta TokenUsage
	)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var root jsonObject
		if err := decodeJSONObject([]byte(line), &root); err != nil {
			continue
		}

		if model := firstNonEmptyString(
			readString(root, "model"),
			readString(root, "model_name"),
		); model != "" {
			lastMeta.Model = model
		}
		if provider := firstNonEmptyString(
			readString(root, "provider"),
			readString(root, "provider_name"),
		); provider != "" {
			lastMeta.Provider = provider
		}

		candidate, ok := extractUsageCandidate(root)
		if !ok {
			continue
		}
		if candidate.Model == "" {
			candidate.Model = lastMeta.Model
		}
		if candidate.Provider == "" {
			candidate.Provider = lastMeta.Provider
		}

		if !found || candidate.priority > best.priority || candidate.priority == best.priority {
			best = candidate
			found = true
		}
	}
	if !found {
		return TokenUsage{}, false
	}
	return best.TokenUsage, true
}

// ExtractTotalTokens returns the last total token value recoverable from agent output.
func ExtractTotalTokens(agentOutput string) (int64, bool) {
	usage, ok := ExtractTokenUsage(agentOutput)
	if !ok {
		return 0, false
	}
	return usage.TotalTokens, true
}

func extractUsageCandidate(root jsonObject) (usageCandidate, bool) {
	rootPriority := 1
	if isFinalUsageEvent(root) {
		rootPriority = 3
	}

	if usageMap := readMap(root, "usage"); usageMap != nil {
		usage, ok := parseUsageMap(usageMap)
		if ok {
			usage.RawUsageJSON = marshalJSONCompact(usageMap)
			return usageCandidate{TokenUsage: usage, priority: rootPriority + 1}, true
		}
	}

	usage, ok := parseUsageMap(root)
	if !ok {
		return usageCandidate{}, false
	}
	usage.RawUsageJSON = marshalJSONCompact(root)
	return usageCandidate{TokenUsage: usage, priority: rootPriority}, true
}

func parseUsageMap(raw jsonObject) (TokenUsage, bool) {
	total := firstNonNilInt64(
		readInt64(raw, "accumulated_total_tokens"),
		readInt64(raw, "total_tokens"),
	)
	input := firstNonNilInt64(
		readInt64(raw, "accumulated_input_tokens"),
		readInt64(raw, "input_tokens"),
	)
	output := firstNonNilInt64(
		readInt64(raw, "accumulated_output_tokens"),
		readInt64(raw, "output_tokens"),
	)
	if total == nil && input != nil && output != nil {
		sum := *input + *output
		total = &sum
	}
	if total == nil && input == nil && output == nil {
		return TokenUsage{}, false
	}

	cachedInput := firstNonNilInt64(
		readInt64(raw, "cached_input_tokens"),
		readNestedInt64(raw, "input_tokens_details", "cached_tokens"),
		readNestedInt64(raw, "prompt_tokens_details", "cached_tokens"),
	)
	reasoningOutput := firstNonNilInt64(
		readInt64(raw, "reasoning_output_tokens"),
		readNestedInt64(raw, "output_tokens_details", "reasoning_tokens"),
		readNestedInt64(raw, "completion_tokens_details", "reasoning_tokens"),
	)

	out := TokenUsage{
		Provider:              firstNonEmptyString(readString(raw, "provider"), readString(raw, "provider_name")),
		Model:                 firstNonEmptyString(readString(raw, "model"), readString(raw, "model_name")),
		InputTokens:           input,
		OutputTokens:          output,
		CachedInputTokens:     cachedInput,
		ReasoningOutputTokens: reasoningOutput,
	}
	if total != nil {
		out.TotalTokens = *total
	}
	return out, true
}

func isFinalUsageEvent(raw jsonObject) bool {
	kind := strings.ToLower(firstNonEmptyString(
		readString(raw, "type"),
		readString(raw, "event"),
		readString(raw, "status"),
	))
	switch kind {
	case "complete", "completed", "turn.completed":
		return true
	default:
		return false
	}
}

func readMap(raw jsonObject, key string) jsonObject {
	childRaw, ok := raw[key]
	if !ok || len(childRaw) == 0 {
		return nil
	}
	var child jsonObject
	if err := decodeJSONObject(childRaw, &child); err != nil {
		return nil
	}
	return child
}

func readString(raw jsonObject, key string) string {
	valueRaw, ok := raw[key]
	if !ok || len(valueRaw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(valueRaw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func readNestedInt64(raw jsonObject, parentKey, childKey string) *int64 {
	parent := readMap(raw, parentKey)
	if parent == nil {
		return nil
	}
	return readInt64(parent, childKey)
}

func readInt64(raw jsonObject, key string) *int64 {
	valueRaw, ok := raw[key]
	if !ok || len(valueRaw) == 0 {
		return nil
	}
	text := strings.TrimSpace(string(valueRaw))
	if text == "" || text == "null" {
		return nil
	}

	if strings.Contains(text, ".") || strings.ContainsAny(text, "eE") {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil
		}
		n := int64(f)
		if float64(n) != f {
			return nil
		}
		return &n
	}

	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func firstNonNilInt64(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func marshalJSONCompact[T any](value T) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func decodeJSONObject(data []byte, dst *jsonObject) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode json object: %w", err)
	}
	return nil
}
