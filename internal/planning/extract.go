package planning

import (
	"regexp"
	"strings"
)

var (
	markdownHeadingPattern  = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	markdownFencePattern    = regexp.MustCompile("^(```|~~~)")
	markdownCheckboxPattern = regexp.MustCompile(`^[-*]\s+\[(?: |x|X)\]\s+(.+)$`)
	markdownBulletPattern   = regexp.MustCompile(`^(?:[-*]|\d+\.)\s+(.+)$`)
	explicitNoPattern       = regexp.MustCompile(`(?i)\b(?:do not|don't|must not|should not|avoid)\b`)
	pathPattern             = regexp.MustCompile(`(?m)(?:^|[\s(` + "`" + `])((?:/)?[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)+(?:\.[A-Za-z0-9._-]+)?(?::\d+(?:-\d+)?)?)`)
)

type extractedSource struct {
	background  []string
	constraints []string
	acceptance  []string
	validation  []string
	followUp    []string
	paths       []string
}

func extractSource(text string) extractedSource {
	cleanText := normalizeNewlines(text)
	lines := strings.Split(cleanText, "\n")

	var out extractedSource
	inFence := false
	section := ""
	var paragraph []string
	generalBackgroundUsed := 0

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		text := normalizeWhitespace(strings.Join(paragraph, " "))
		paragraph = nil
		if text == "" {
			return
		}
		switch section {
		case "acceptance":
			out.acceptance = append(out.acceptance, text)
		case "constraints":
			out.constraints = append(out.constraints, text)
		case "validation":
			out.validation = append(out.validation, text)
		case "followup":
			out.followUp = append(out.followUp, text)
		case "background":
			out.background = append(out.background, text)
		default:
			if explicitNoPattern.MatchString(text) {
				out.constraints = append(out.constraints, text)
				return
			}
			if generalBackgroundUsed < 2 {
				out.background = append(out.background, text)
				generalBackgroundUsed++
			}
		}
	}

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)

		if markdownFencePattern.MatchString(trimmed) {
			flushParagraph()
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if trimmed == "" {
			flushParagraph()
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushParagraph()
			continue
		}
		if matches := markdownHeadingPattern.FindStringSubmatch(trimmed); matches != nil {
			flushParagraph()
			section = classifyHeading(matches[1])
			continue
		}
		if matches := markdownCheckboxPattern.FindStringSubmatch(trimmed); matches != nil {
			flushParagraph()
			item := cleanListItem(matches[1])
			if item == "" {
				continue
			}
			switch section {
			case "constraints":
				out.constraints = append(out.constraints, item)
			case "validation":
				out.validation = append(out.validation, item)
			case "followup":
				out.followUp = append(out.followUp, item)
			default:
				out.acceptance = append(out.acceptance, item)
			}
			continue
		}
		if matches := markdownBulletPattern.FindStringSubmatch(trimmed); matches != nil {
			flushParagraph()
			item := cleanListItem(matches[1])
			if item == "" {
				continue
			}
			switch section {
			case "acceptance":
				out.acceptance = append(out.acceptance, item)
			case "constraints":
				out.constraints = append(out.constraints, item)
			case "validation":
				out.validation = append(out.validation, item)
			case "followup":
				out.followUp = append(out.followUp, item)
			case "background":
				out.background = append(out.background, item)
			default:
				if explicitNoPattern.MatchString(item) {
					out.constraints = append(out.constraints, item)
				}
			}
			continue
		}
		paragraph = append(paragraph, trimmed)
	}
	flushParagraph()

	for _, match := range pathPattern.FindAllStringSubmatch(stripQuotedAndFenced(cleanText), -1) {
		candidate := strings.TrimSpace(strings.Trim(match[1], ".,:;()[]{}"))
		if candidate == "" || strings.Contains(candidate, "://") {
			continue
		}
		out.paths = append(out.paths, candidate)
	}

	out.background = uniqueStrings(out.background)
	out.constraints = uniqueStrings(out.constraints)
	out.acceptance = uniqueStrings(out.acceptance)
	out.validation = uniqueStrings(out.validation)
	out.followUp = uniqueStrings(out.followUp)
	out.paths = uniqueStrings(out.paths)
	return out
}

func classifyHeading(heading string) string {
	normalized := strings.ToLower(normalizeWhitespace(heading))
	switch {
	case strings.Contains(normalized, "acceptance"):
		return "acceptance"
	case strings.Contains(normalized, "constraint"):
		return "constraints"
	case strings.Contains(normalized, "non-goal"), strings.Contains(normalized, "non goal"):
		return "constraints"
	case strings.Contains(normalized, "validation"):
		return "validation"
	case strings.Contains(normalized, "test plan"):
		return "validation"
	case strings.Contains(normalized, "follow-up"), strings.Contains(normalized, "follow up"):
		return "followup"
	case strings.Contains(normalized, "background"),
		strings.Contains(normalized, "summary"),
		strings.Contains(normalized, "context"),
		strings.Contains(normalized, "outcome"),
		strings.Contains(normalized, "scope"),
		strings.Contains(normalized, "goal"),
		strings.Contains(normalized, "implementation note"):
		return "background"
	default:
		return ""
	}
}

func stripQuotedAndFenced(text string) string {
	lines := strings.Split(normalizeNewlines(text), "\n")
	inFence := false
	var kept []string
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if markdownFencePattern.MatchString(trimmed) {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(trimmed, ">") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func cleanListItem(text string) string {
	text = normalizeWhitespace(text)
	text = strings.Trim(text, ".,; ")
	return text
}

func normalizeWhitespace(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func normalizeNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(normalizeWhitespace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalizeWhitespace(value))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
