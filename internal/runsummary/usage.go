package runsummary

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const RecordedTokenUsageFile = "run_token_usage.json"

type TokenUsage struct {
	Provider              string `json:"provider,omitempty"`
	Model                 string `json:"model,omitempty"`
	TotalTokens           int64  `json:"total_tokens"`
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens,omitempty"`
	RawUsageJSON          string `json:"raw_usage_json,omitempty"`
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

func ExtractCodexSessionUsage(sessionJSONL string) (TokenUsage, bool, error) {
	scanner := bufio.NewScanner(strings.NewReader(sessionJSONL))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		best     TokenUsage
		found    bool
		firstErr error
	)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, `"token_count"`) {
			continue
		}

		usage, ok, err := extractCodexSessionUsageLine(line, "total_token_usage")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !ok {
			continue
		}
		best = usage
		found = true
	}
	if err := scanner.Err(); err != nil {
		return TokenUsage{}, false, fmt.Errorf("scan codex session usage: %w", err)
	}
	if found {
		return best, true, nil
	}
	if firstErr != nil {
		return TokenUsage{}, false, firstErr
	}
	return TokenUsage{}, false, nil
}

func ExtractCodexSessionUsageDelta(sessionJSONL string) (TokenUsage, bool, error) {
	scanner := bufio.NewScanner(strings.NewReader(sessionJSONL))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		total    TokenUsage
		found    bool
		firstErr error
	)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, `"token_count"`) {
			continue
		}

		usage, ok, err := extractCodexSessionUsageLine(line, "last_token_usage")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !ok {
			continue
		}
		total = addTokenUsage(total, usage)
		found = true
	}
	if err := scanner.Err(); err != nil {
		return TokenUsage{}, false, fmt.Errorf("scan codex session usage delta: %w", err)
	}
	if found {
		return total, true, nil
	}
	if firstErr != nil {
		return TokenUsage{}, false, firstErr
	}
	return TokenUsage{}, false, nil
}

func SubtractTokenUsage(total, baseline TokenUsage) (TokenUsage, bool) {
	if total.TotalTokens < baseline.TotalTokens {
		return TokenUsage{}, false
	}

	out := TokenUsage{
		Provider:     firstNonEmptyString(total.Provider, baseline.Provider),
		Model:        firstNonEmptyString(total.Model, baseline.Model),
		TotalTokens:  total.TotalTokens - baseline.TotalTokens,
		RawUsageJSON: total.RawUsageJSON,
	}

	if value, ok := subtractOptionalInt64(total.InputTokens, baseline.InputTokens); ok {
		out.InputTokens = value
	}
	if value, ok := subtractOptionalInt64(total.OutputTokens, baseline.OutputTokens); ok {
		out.OutputTokens = value
	}
	if value, ok := subtractOptionalInt64(total.CachedInputTokens, baseline.CachedInputTokens); ok {
		out.CachedInputTokens = value
	}
	if value, ok := subtractOptionalInt64(total.ReasoningOutputTokens, baseline.ReasoningOutputTokens); ok {
		out.ReasoningOutputTokens = value
	}

	if out.TotalTokens == 0 && out.InputTokens == nil && out.OutputTokens == nil &&
		out.CachedInputTokens == nil && out.ReasoningOutputTokens == nil {
		return TokenUsage{}, false
	}
	return out, true
}

func WriteRecordedTokenUsage(path string, usage TokenUsage) error {
	data, err := json.MarshalIndent(usage, "", "  ")
	if err != nil {
		return fmt.Errorf("encode recorded token usage: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write recorded token usage: %w", err)
	}
	return nil
}

func ReadRecordedTokenUsage(path string) (TokenUsage, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TokenUsage{}, false, nil
		}
		return TokenUsage{}, false, fmt.Errorf("read recorded token usage: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return TokenUsage{}, false, nil
	}

	var usage TokenUsage
	if err := json.Unmarshal(data, &usage); err != nil {
		return TokenUsage{}, false, fmt.Errorf("decode recorded token usage: %w", err)
	}
	if usage.TotalTokens <= 0 && usage.InputTokens == nil && usage.OutputTokens == nil &&
		usage.CachedInputTokens == nil && usage.ReasoningOutputTokens == nil {
		return TokenUsage{}, false, nil
	}
	return usage, true, nil
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

func extractCodexSessionUsageLine(line, usageKey string) (TokenUsage, bool, error) {
	var root jsonObject
	if err := decodeJSONObject([]byte(line), &root); err != nil {
		return TokenUsage{}, false, fmt.Errorf("decode codex session event: %w", err)
	}
	if !isCodexSessionUsageEvent(root) {
		return TokenUsage{}, false, nil
	}

	payload := readMap(root, "payload")
	info := readMap(payload, "info")
	if info == nil {
		return TokenUsage{}, false, nil
	}

	usageMap := readMap(info, usageKey)
	if usageMap == nil {
		return TokenUsage{}, false, nil
	}
	usage, ok := parseUsageMap(usageMap)
	if !ok {
		return TokenUsage{}, false, nil
	}
	usage.RawUsageJSON = marshalJSONCompact(usageMap)
	usage.Model = firstNonEmptyString(usage.Model, readString(root, "model"), readString(payload, "model"))
	usage.Provider = firstNonEmptyString(usage.Provider, readString(root, "provider"), readString(payload, "provider"))
	return usage, true, nil
}

func isCodexSessionUsageEvent(raw jsonObject) bool {
	if strings.ToLower(readString(raw, "type")) != "event_msg" {
		return false
	}
	payload := readMap(raw, "payload")
	if payload == nil {
		return false
	}
	return strings.EqualFold(readString(payload, "type"), "token_count")
}

func addTokenUsage(left, right TokenUsage) TokenUsage {
	out := TokenUsage{
		Provider:     firstNonEmptyString(right.Provider, left.Provider),
		Model:        firstNonEmptyString(right.Model, left.Model),
		TotalTokens:  left.TotalTokens + right.TotalTokens,
		RawUsageJSON: right.RawUsageJSON,
	}
	out.InputTokens = addOptionalInt64(left.InputTokens, right.InputTokens)
	out.OutputTokens = addOptionalInt64(left.OutputTokens, right.OutputTokens)
	out.CachedInputTokens = addOptionalInt64(left.CachedInputTokens, right.CachedInputTokens)
	out.ReasoningOutputTokens = addOptionalInt64(left.ReasoningOutputTokens, right.ReasoningOutputTokens)
	return out
}

func addOptionalInt64(left, right *int64) *int64 {
	if left == nil && right == nil {
		return nil
	}
	sum := int64(0)
	if left != nil {
		sum += *left
	}
	if right != nil {
		sum += *right
	}
	return &sum
}

func subtractOptionalInt64(total, baseline *int64) (*int64, bool) {
	if total == nil {
		return nil, false
	}
	if baseline == nil {
		value := *total
		return &value, true
	}
	if *total < *baseline {
		return nil, false
	}
	value := *total - *baseline
	return &value, true
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
