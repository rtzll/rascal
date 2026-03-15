package planning

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rtzll/rascal/internal/runtrigger"
)

var genericPRInstructionPattern = regexp.MustCompile(`(?i)^address pr #\d+ (?:feedback|review feedback|inline review comment|unresolved review thread)$`)

func Compile(input Input) (Compiled, error) {
	normalized := normalizeInput(input)
	if normalized.Version == "" {
		normalized.Version = SchemaVersion
	}

	brief := RunBrief{
		Version: normalized.Version,
		Trigger: normalized.Trigger,
		Sources: sourceRefs(normalized.Sources),
	}

	objective, objectiveSources, objectiveDerivation := deriveObjective(normalized)
	brief.PrimaryObjective = Field{
		Text:       objective,
		SourceIDs:  objectiveSources,
		Derivation: objectiveDerivation,
	}

	background, backgroundSources := deriveBackground(normalized, objective)
	if background != "" {
		brief.BackgroundSummary = Field{
			Text:       background,
			SourceIDs:  backgroundSources,
			Derivation: "combined from remaining source context",
		}
	}

	var constraints []Item
	var acceptance []Item
	var validation []Item
	var followUp []Item
	var relevantFiles []PathRef
	for _, source := range normalized.Sources {
		extracted := extractSource(source.Text)
		constraints = append(constraints, itemsFromStrings(extracted.constraints, source.ID, "")...)
		acceptance = append(acceptance, itemsFromStrings(extracted.acceptance, source.ID, "")...)
		validation = append(validation, itemsFromStrings(extracted.validation, source.ID, "")...)
		followUp = append(followUp, itemsFromStrings(extracted.followUp, source.ID, "")...)
		relevantFiles = append(relevantFiles, pathRefsFromStrings(extracted.paths, source.ID)...)
		if source.Location != "" {
			relevantFiles = append(relevantFiles, pathRefsFromStrings([]string{source.Location}, source.ID)...)
		}
	}
	brief.Constraints = dedupeItems(constraints)
	brief.AcceptanceCriteria = dedupeItems(acceptance)
	brief.Validation = dedupeItems(validation)
	brief.FollowUp = dedupeItems(followUp)
	brief.RelevantFiles = dedupePathRefs(relevantFiles)

	brief.Ambiguities = buildAmbiguities(normalized, brief)
	brief.Assumptions = buildAssumptions(normalized, brief)

	return Compiled{
		Input: normalized,
		Brief: brief,
	}, nil
}

func Summaries(input Input) []SourceSummary {
	input = normalizeInput(input)
	out := make([]SourceSummary, 0, len(input.Sources))
	for _, source := range input.Sources {
		out = append(out, SourceSummary{
			ID:       source.ID,
			Kind:     source.Kind,
			Label:    source.Label,
			URL:      source.URL,
			Location: source.Location,
			Preview:  preview(source.Text, 180),
		})
	}
	return out
}

func normalizeInput(input Input) Input {
	input.Version = strings.TrimSpace(input.Version)
	input.Trigger = runtrigger.Normalize(input.Trigger.String())
	input.Repo = strings.TrimSpace(input.Repo)
	input.Instruction = strings.TrimSpace(input.Instruction)
	input.Context = strings.TrimSpace(input.Context)

	sources := make([]Source, 0, len(input.Sources)+2)
	for idx, source := range input.Sources {
		source.ID = sourceID(source.ID, idx+1)
		source.Label = strings.TrimSpace(source.Label)
		source.URL = strings.TrimSpace(source.URL)
		source.Author = strings.TrimSpace(source.Author)
		source.Location = strings.TrimSpace(source.Location)
		source.Text = strings.TrimSpace(source.Text)
		sources = append(sources, source)
	}
	if len(sources) == 0 && input.Instruction != "" {
		sources = append(sources, Source{
			ID:    sourceID("", 1),
			Kind:  fallbackSourceKind(input.Trigger),
			Label: "Primary task text",
			Text:  input.Instruction,
		})
	}
	if input.Context != "" && !containsSourceText(sources, input.Context) {
		sources = append(sources, Source{
			ID:    sourceID("", len(sources)+1),
			Kind:  SourceKindTriggerContext,
			Label: "Additional context",
			Text:  input.Context,
		})
	}
	input.Sources = sources
	return input
}

func sourceID(existing string, index int) string {
	if strings.TrimSpace(existing) != "" {
		return strings.TrimSpace(existing)
	}
	return fmt.Sprintf("source-%d", index)
}

func fallbackSourceKind(trigger runtrigger.Name) SourceKind {
	switch {
	case trigger.IsIssue():
		return SourceKindGitHubIssue
	case trigger.IsComment():
		return SourceKindTriggerContext
	default:
		return SourceKindManualPrompt
	}
}

func containsSourceText(sources []Source, text string) bool {
	normalized := normalizeWhitespace(text)
	for _, source := range sources {
		if normalizeWhitespace(source.Text) == normalized {
			return true
		}
	}
	return false
}

func deriveObjective(input Input) (string, []string, string) {
	if text := firstObjectiveCandidate(input); text != "" {
		for _, source := range input.Sources {
			if sourceText := firstMeaningfulLine(source.Text); sourceText != "" && sourceText == text {
				return text, []string{source.ID}, "selected from the most specific source text"
			}
		}
	}
	line := firstMeaningfulLine(input.Instruction)
	if line != "" {
		return line, sourceIDsForInstruction(input), "selected from normalized instruction text"
	}
	for _, source := range input.Sources {
		if line := firstMeaningfulLine(source.Text); line != "" {
			return line, []string{source.ID}, "selected from source text"
		}
	}
	return "", nil, ""
}

func firstObjectiveCandidate(input Input) string {
	if genericPRInstructionPattern.MatchString(input.Instruction) || input.Instruction == "" {
		for _, source := range input.Sources {
			if source.Kind == SourceKindTriggerContext || source.Kind == SourceKindReference {
				continue
			}
			if line := firstMeaningfulLine(source.Text); line != "" {
				return line
			}
		}
	}
	return ""
}

func sourceIDsForInstruction(input Input) []string {
	if len(input.Sources) == 0 {
		return nil
	}
	return []string{input.Sources[0].ID}
}

func firstMeaningfulLine(text string) string {
	for _, raw := range strings.Split(normalizeNewlines(text), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, ">") || markdownFencePattern.MatchString(line) {
			continue
		}
		line = normalizeWhitespace(strings.TrimPrefix(line, "#"))
		line = strings.Trim(line, "-* ")
		if line != "" {
			return line
		}
	}
	return ""
}

func deriveBackground(input Input, objective string) (string, []string) {
	var parts []string
	var ids []string

	rest := remainingInstruction(input.Instruction, objective)
	if rest != "" {
		parts = append(parts, rest)
		ids = append(ids, sourceIDsForInstruction(input)...)
	}
	for _, source := range input.Sources {
		extracted := extractSource(source.Text)
		for _, part := range extracted.background {
			if sameText(part, objective) || sameText(part, rest) {
				continue
			}
			parts = append(parts, part)
			ids = append(ids, source.ID)
		}
		if source.Kind == SourceKindTriggerContext && source.Text != "" && !sameText(source.Text, input.Context) {
			parts = append(parts, normalizeWhitespace(source.Text))
			ids = append(ids, source.ID)
		}
	}
	parts = uniqueStrings(parts)
	ids = uniqueStrings(ids)
	return strings.Join(parts, "\n\n"), ids
}

func remainingInstruction(instruction, objective string) string {
	instruction = normalizeNewlines(instruction)
	if strings.TrimSpace(instruction) == "" {
		return ""
	}
	lines := strings.Split(instruction, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start < len(lines) && sameText(lines[start], objective) {
		start++
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

func itemsFromStrings(values []string, sourceID, derivation string) []Item {
	if len(values) == 0 {
		return nil
	}
	out := make([]Item, 0, len(values))
	for _, value := range values {
		out = append(out, Item{
			Text:       value,
			SourceIDs:  []string{sourceID},
			Derivation: derivation,
		})
	}
	return out
}

func pathRefsFromStrings(values []string, sourceID string) []PathRef {
	if len(values) == 0 {
		return nil
	}
	out := make([]PathRef, 0, len(values))
	for _, value := range values {
		out = append(out, PathRef{
			Path:       value,
			SourceIDs:  []string{sourceID},
			Derivation: "detected from source text",
		})
	}
	return out
}

func buildAmbiguities(input Input, brief RunBrief) []Note {
	var notes []Note
	if brief.PrimaryObjective.Text == "" {
		notes = append(notes, Note{Text: "The trigger input did not provide a clear primary objective."})
	}
	if len(brief.AcceptanceCriteria) == 0 {
		notes = append(notes, Note{Text: "Acceptance criteria were not explicit in the trigger input."})
	}
	if len(brief.RelevantFiles) == 0 {
		notes = append(notes, Note{Text: "Target files or paths were not directly inferable from the trigger input."})
	}
	if input.Trigger.IsIssue() && !hasSourceKind(input.Sources, SourceKindGitHubIssue) {
		notes = append(notes, Note{Text: "GitHub issue content was not available, so the brief is based on the issue reference rather than the issue body."})
	}
	if len(brief.PrimaryObjective.Text) > 0 &&
		len(brief.AcceptanceCriteria) == 0 &&
		len(brief.Validation) == 0 &&
		len(brief.Constraints) == 0 &&
		len(strings.Fields(brief.PrimaryObjective.Text)) <= 4 {
		notes = append(notes, Note{Text: "The trigger input is terse, so execution should stay conservative and avoid expanding scope."})
	}
	return dedupeNotes(notes)
}

func buildAssumptions(input Input, brief RunBrief) []Note {
	var notes []Note
	if genericPRInstructionPattern.MatchString(input.Instruction) && len(brief.PrimaryObjective.Text) > 0 {
		notes = append(notes, Note{
			Text:       "Treat the supplied pull request feedback as the concrete change request and avoid unrelated edits.",
			SourceIDs:  brief.PrimaryObjective.SourceIDs,
			Derivation: "derived from generic PR feedback instruction plus source text",
		})
	}
	return dedupeNotes(notes)
}

func hasSourceKind(sources []Source, kind SourceKind) bool {
	for _, source := range sources {
		if source.Kind == kind && strings.TrimSpace(source.Text) != "" {
			return true
		}
	}
	return false
}

func sourceRefs(sources []Source) []SourceRef {
	if len(sources) == 0 {
		return nil
	}
	refs := make([]SourceRef, 0, len(sources))
	for _, source := range sources {
		refs = append(refs, SourceRef{
			ID:       source.ID,
			Kind:     source.Kind,
			Label:    source.Label,
			URL:      source.URL,
			Location: source.Location,
		})
	}
	return refs
}

func preview(text string, limit int) string {
	text = normalizeWhitespace(text)
	if len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func sameText(a, b string) bool {
	return normalizeWhitespace(a) == normalizeWhitespace(b)
}

func dedupeItems(values []Item) []Item {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]int, len(values))
	out := make([]Item, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(normalizeWhitespace(value.Text))
		if key == "" {
			continue
		}
		if idx, ok := seen[key]; ok {
			out[idx].SourceIDs = uniqueStrings(append(out[idx].SourceIDs, value.SourceIDs...))
			continue
		}
		value.Text = normalizeWhitespace(value.Text)
		value.SourceIDs = uniqueStrings(value.SourceIDs)
		seen[key] = len(out)
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dedupePathRefs(values []PathRef) []PathRef {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]int, len(values))
	out := make([]PathRef, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(normalizeWhitespace(value.Path))
		if key == "" {
			continue
		}
		if idx, ok := seen[key]; ok {
			out[idx].SourceIDs = uniqueStrings(append(out[idx].SourceIDs, value.SourceIDs...))
			continue
		}
		value.Path = normalizeWhitespace(value.Path)
		value.SourceIDs = uniqueStrings(value.SourceIDs)
		seen[key] = len(out)
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dedupeNotes(values []Note) []Note {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]int, len(values))
	out := make([]Note, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(normalizeWhitespace(value.Text))
		if key == "" {
			continue
		}
		if idx, ok := seen[key]; ok {
			out[idx].SourceIDs = uniqueStrings(append(out[idx].SourceIDs, value.SourceIDs...))
			continue
		}
		value.Text = normalizeWhitespace(value.Text)
		value.SourceIDs = uniqueStrings(value.SourceIDs)
		seen[key] = len(out)
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
