package planning

import (
	"fmt"
	"strings"
)

type RenderOptions struct {
	IncludeProvenance bool
	Title             string
}

func RenderMarkdown(brief RunBrief, opts RenderOptions) string {
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "Run Brief"
	}

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "# %s\n\n", title)
	_, _ = fmt.Fprintf(&b, "- Version: `%s`\n", firstNonEmpty(brief.Version, SchemaVersion))
	_, _ = fmt.Fprintf(&b, "- Trigger: `%s`\n", brief.Trigger)

	writeFieldSection(&b, "Primary Objective", brief.PrimaryObjective, opts.IncludeProvenance)
	writeFieldSection(&b, "Background Summary", brief.BackgroundSummary, opts.IncludeProvenance)
	writeItemSection(&b, "Constraints", brief.Constraints, opts.IncludeProvenance)
	writeItemSection(&b, "Acceptance Criteria", brief.AcceptanceCriteria, opts.IncludeProvenance)
	writePathSection(&b, "Relevant Files", brief.RelevantFiles, opts.IncludeProvenance)
	writeItemSection(&b, "Validation", brief.Validation, opts.IncludeProvenance)
	writeItemSection(&b, "Follow-Up", brief.FollowUp, opts.IncludeProvenance)
	writeNoteSection(&b, "Ambiguities", brief.Ambiguities, opts.IncludeProvenance)
	writeNoteSection(&b, "Assumptions", brief.Assumptions, opts.IncludeProvenance)

	if len(brief.Sources) > 0 {
		b.WriteString("\n## Sources\n\n")
		for _, source := range brief.Sources {
			line := fmt.Sprintf("- `%s`: `%s`", source.ID, source.Kind)
			if source.Label != "" {
				line += " - " + source.Label
			}
			if source.Location != "" {
				line += fmt.Sprintf(" (%s)", source.Location)
			}
			if source.URL != "" {
				line += fmt.Sprintf(" [%s](%s)", source.URL, source.URL)
			}
			b.WriteString(line + "\n")
		}
	}

	return strings.TrimSpace(b.String()) + "\n"
}

func RenderSourceSummary(input Input) string {
	summaries := Summaries(input)
	if len(summaries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Raw Source Summary\n\n")
	for _, summary := range summaries {
		line := fmt.Sprintf("- `%s`: `%s`", summary.ID, summary.Kind)
		if summary.Label != "" {
			line += " - " + summary.Label
		}
		if summary.Location != "" {
			line += fmt.Sprintf(" (%s)", summary.Location)
		}
		if summary.Preview != "" {
			line += fmt.Sprintf("\n  %s", summary.Preview)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func writeFieldSection(b *strings.Builder, title string, field Field, includeProvenance bool) {
	if strings.TrimSpace(field.Text) == "" {
		return
	}
	_, _ = fmt.Fprintf(b, "\n## %s\n\n%s", title, strings.TrimSpace(field.Text))
	if includeProvenance {
		writeProvenance(b, field.SourceIDs, field.Derivation)
	}
	b.WriteString("\n")
}

func writeItemSection(b *strings.Builder, title string, items []Item, includeProvenance bool) {
	if len(items) == 0 {
		return
	}
	_, _ = fmt.Fprintf(b, "\n## %s\n\n", title)
	for _, item := range items {
		line := "- " + strings.TrimSpace(item.Text)
		if includeProvenance {
			line += provenanceSuffix(item.SourceIDs, item.Derivation)
		}
		b.WriteString(line + "\n")
	}
}

func writePathSection(b *strings.Builder, title string, items []PathRef, includeProvenance bool) {
	if len(items) == 0 {
		return
	}
	_, _ = fmt.Fprintf(b, "\n## %s\n\n", title)
	for _, item := range items {
		line := "- `" + strings.TrimSpace(item.Path) + "`"
		if includeProvenance {
			line += provenanceSuffix(item.SourceIDs, item.Derivation)
		}
		b.WriteString(line + "\n")
	}
}

func writeNoteSection(b *strings.Builder, title string, items []Note, includeProvenance bool) {
	if len(items) == 0 {
		return
	}
	_, _ = fmt.Fprintf(b, "\n## %s\n\n", title)
	for _, item := range items {
		line := "- " + strings.TrimSpace(item.Text)
		if includeProvenance {
			line += provenanceSuffix(item.SourceIDs, item.Derivation)
		}
		b.WriteString(line + "\n")
	}
}

func writeProvenance(b *strings.Builder, sourceIDs []string, derivation string) {
	suffix := provenanceSuffix(sourceIDs, derivation)
	if suffix == "" {
		return
	}
	b.WriteString("\n" + strings.TrimSpace(strings.TrimPrefix(suffix, " ")) + "\n")
}

func provenanceSuffix(sourceIDs []string, derivation string) string {
	var details []string
	if len(sourceIDs) > 0 {
		details = append(details, "source: "+strings.Join(sourceIDs, ", "))
	}
	if strings.TrimSpace(derivation) != "" {
		details = append(details, strings.TrimSpace(derivation))
	}
	if len(details) == 0 {
		return ""
	}
	return " _(" + strings.Join(details, "; ") + ")_"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
