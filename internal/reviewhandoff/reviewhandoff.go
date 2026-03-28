package reviewhandoff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	ArtifactJSONFile = "review-handoff.json"
	ArtifactMDFile   = "review-handoff.md"

	PRSectionStartMarker = "<!-- rascal:review-handoff:start -->"
	PRSectionEndMarker   = "<!-- rascal:review-handoff:end -->"
)

var (
	whitespacePattern  = regexp.MustCompile(`\s+`)
	reviewerEmailRegex = regexp.MustCompile(`(?i)(?:\d+\+)?([a-z0-9][a-z0-9-]*)@users\.noreply\.github\.com`)
	reviewerNameRegex  = regexp.MustCompile(`^@?[a-z0-9][a-z0-9-]*$`)
	reviewerTeamRegex  = regexp.MustCompile(`^@?[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9._-]*$`)
)

type ChangedFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added,omitempty"`
	Deleted int    `json:"deleted,omitempty"`
}

type HistoryTouch struct {
	Path     string
	Reviewer string
}

type Input struct {
	BaseRef           string
	HeadRef           string
	ChangedFiles      []ChangedFile
	Codeowners        string
	History           []HistoryTouch
	ExcludedReviewers []string
}

type Report struct {
	SuggestedReviewers []ReviewerSuggestion `json:"suggested_reviewers,omitempty"`
	ReviewerSummary    string               `json:"reviewer_summary"`
	Risk               Risk                 `json:"risk"`
	ChangedPaths       ChangedPathSummary   `json:"changed_paths"`
	NotableSignals     []string             `json:"notable_signals,omitempty"`
}

type ReviewerSuggestion struct {
	Reviewer   string   `json:"reviewer"`
	Confidence string   `json:"confidence"`
	Reasons    []string `json:"reasons"`
}

type Risk struct {
	Level   string   `json:"level"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}

type ChangedPathSummary struct {
	BaseRef               string   `json:"base_ref,omitempty"`
	HeadRef               string   `json:"head_ref,omitempty"`
	FilesChanged          int      `json:"files_changed"`
	DirectoriesChanged    int      `json:"directories_changed"`
	Areas                 []string `json:"areas,omitempty"`
	SamplePaths           []string `json:"sample_paths,omitempty"`
	TestFilesChanged      bool     `json:"test_files_changed"`
	ProductionCodeChanged bool     `json:"production_code_changed"`
}

type reviewerCandidate struct {
	reviewer        string
	confidenceRank  int
	codeownersPaths map[string]struct{}
	historyPaths    map[string]struct{}
	historyTouches  int
}

type codeownersRule struct {
	owners []string
	regex  *regexp.Regexp
}

func Analyze(input Input) Report {
	changed := normalizeChangedFiles(input.ChangedFiles)
	summary := buildChangedPathSummary(input.BaseRef, input.HeadRef, changed)
	risk, signals := classifyRisk(changed, summary)
	reviewers, reviewerSummary := suggestReviewers(changed, input.Codeowners, input.History, input.ExcludedReviewers)
	return Report{
		SuggestedReviewers: reviewers,
		ReviewerSummary:    reviewerSummary,
		Risk:               risk,
		ChangedPaths:       summary,
		NotableSignals:     signals,
	}
}

func ArtifactPaths(dir string) (jsonPath, markdownPath string) {
	root := strings.TrimSpace(dir)
	return filepath.Join(root, ArtifactJSONFile), filepath.Join(root, ArtifactMDFile)
}

func WriteArtifacts(dir string, report Report) error {
	jsonPath, markdownPath := ArtifactPaths(dir)
	if err := os.MkdirAll(strings.TrimSpace(dir), 0o755); err != nil {
		return fmt.Errorf("create review handoff directory: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode review handoff json: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return fmt.Errorf("write review handoff json: %w", err)
	}
	if err := os.WriteFile(markdownPath, []byte(RenderMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write review handoff markdown: %w", err)
	}
	return nil
}

func ReadReport(dir string) (Report, bool, error) {
	jsonPath, _ := ArtifactPaths(dir)
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Report{}, false, nil
		}
		return Report{}, false, fmt.Errorf("read review handoff json: %w", err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, false, fmt.Errorf("decode review handoff json: %w", err)
	}
	return report, true, nil
}

func RenderMarkdown(report Report) string {
	lines := []string{
		"# Review Handoff",
		"",
		fmt.Sprintf("- Risk: `%s` (score %d)", report.Risk.Level, report.Risk.Score),
		fmt.Sprintf("- Reviewer routing: %s", report.ReviewerSummary),
		fmt.Sprintf("- Changed paths: %d files across %d directories", report.ChangedPaths.FilesChanged, report.ChangedPaths.DirectoriesChanged),
	}
	if len(report.ChangedPaths.Areas) > 0 {
		lines = append(lines, fmt.Sprintf("- Areas: %s", strings.Join(backtickJoin(report.ChangedPaths.Areas), ", ")))
	}
	lines = append(lines, "")
	lines = append(lines, "## Suggested Reviewers", "")
	if len(report.SuggestedReviewers) == 0 {
		lines = append(lines, "- None")
	} else {
		for _, reviewer := range report.SuggestedReviewers {
			lines = append(lines, fmt.Sprintf("- %s (%s): %s", reviewer.Reviewer, reviewer.Confidence, strings.Join(reviewer.Reasons, "; ")))
		}
	}
	lines = append(lines, "", "## Risk Reasons", "")
	for _, reason := range report.Risk.Reasons {
		lines = append(lines, "- "+reason)
	}
	lines = append(lines, "", "## Signals", "")
	for _, signal := range report.NotableSignals {
		lines = append(lines, "- "+signal)
	}
	if len(report.ChangedPaths.SamplePaths) > 0 {
		lines = append(lines, "", "## Sample Paths", "")
		for _, path := range report.ChangedPaths.SamplePaths {
			lines = append(lines, "- `"+path+"`")
		}
	}
	return strings.Join(lines, "\n")
}

func RenderPRSection(report Report) string {
	reviewerSummary := report.ReviewerSummary
	if len(report.SuggestedReviewers) > 0 {
		names := make([]string, 0, len(report.SuggestedReviewers))
		for _, reviewer := range report.SuggestedReviewers {
			names = append(names, reviewer.Reviewer)
		}
		reviewerSummary = strings.Join(names, ", ")
	}

	signals := report.NotableSignals
	if len(signals) > 3 {
		signals = signals[:3]
	}
	lines := []string{
		PRSectionStartMarker,
		"## Review Handoff",
		fmt.Sprintf("- Risk: `%s` (score %d)", report.Risk.Level, report.Risk.Score),
		fmt.Sprintf("- Suggested reviewers: %s", reviewerSummary),
		fmt.Sprintf("- Changed paths: %d files across %d directories", report.ChangedPaths.FilesChanged, report.ChangedPaths.DirectoriesChanged),
	}
	if len(report.ChangedPaths.Areas) > 0 {
		lines = append(lines, fmt.Sprintf("- Areas: %s", strings.Join(backtickJoin(report.ChangedPaths.Areas), ", ")))
	}
	if len(signals) > 0 {
		lines = append(lines, fmt.Sprintf("- Signals: %s", strings.Join(signals, "; ")))
	}
	lines = append(lines, PRSectionEndMarker)
	return strings.Join(lines, "\n")
}

func UpsertPRSection(body string, report Report) string {
	return UpsertManagedSection(body, RenderPRSection(report))
}

func UpsertManagedSection(body, section string) string {
	body = strings.TrimSpace(body)
	section = strings.TrimSpace(section)
	if section == "" {
		return body
	}

	start := strings.Index(body, PRSectionStartMarker)
	end := strings.Index(body, PRSectionEndMarker)
	if start >= 0 && end >= start {
		end += len(PRSectionEndMarker)
		updated := strings.TrimSpace(body[:start])
		tail := strings.TrimSpace(body[end:])
		switch {
		case updated == "" && tail == "":
			return section
		case updated == "":
			return strings.TrimSpace(section + "\n\n" + tail)
		case tail == "":
			return strings.TrimSpace(updated + "\n\n" + section)
		default:
			return strings.TrimSpace(updated + "\n\n" + section + "\n\n" + tail)
		}
	}

	footerMarker := "\n\n---\n\n"
	if idx := strings.Index(body, footerMarker); idx >= 0 {
		head := strings.TrimSpace(body[:idx])
		footer := strings.TrimSpace(body[idx+len(footerMarker):])
		if head == "" {
			return strings.TrimSpace(section + footerMarker + footer)
		}
		return strings.TrimSpace(head + "\n\n" + section + footerMarker + footer)
	}
	if body == "" {
		return section
	}
	return strings.TrimSpace(body + "\n\n" + section)
}

func normalizeChangedFiles(files []ChangedFile) []ChangedFile {
	seen := make(map[string]ChangedFile)
	for _, file := range files {
		path := cleanPath(file.Path)
		if path == "" {
			continue
		}
		current := seen[path]
		current.Path = path
		current.Added += max(file.Added, 0)
		current.Deleted += max(file.Deleted, 0)
		seen[path] = current
	}
	out := make([]ChangedFile, 0, len(seen))
	for _, file := range seen {
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func buildChangedPathSummary(baseRef, headRef string, changed []ChangedFile) ChangedPathSummary {
	dirs := make(map[string]struct{})
	areas := make(map[string]struct{})
	samplePaths := make([]string, 0, len(changed))
	testFilesChanged := false
	productionCodeChanged := false
	for _, file := range changed {
		dir := filepath.ToSlash(filepath.Dir(file.Path))
		if dir == "." {
			dir = "."
		}
		dirs[dir] = struct{}{}
		areas[topLevelArea(file.Path)] = struct{}{}
		if len(samplePaths) < 8 {
			samplePaths = append(samplePaths, file.Path)
		}
		if isTestPath(file.Path) {
			testFilesChanged = true
		}
		if isProductionCodePath(file.Path) {
			productionCodeChanged = true
		}
	}
	areaList := make([]string, 0, len(areas))
	for area := range areas {
		areaList = append(areaList, area)
	}
	sort.Strings(areaList)
	return ChangedPathSummary{
		BaseRef:               strings.TrimSpace(baseRef),
		HeadRef:               strings.TrimSpace(headRef),
		FilesChanged:          len(changed),
		DirectoriesChanged:    len(dirs),
		Areas:                 areaList,
		SamplePaths:           samplePaths,
		TestFilesChanged:      testFilesChanged,
		ProductionCodeChanged: productionCodeChanged,
	}
}

func classifyRisk(changed []ChangedFile, summary ChangedPathSummary) (Risk, []string) {
	score := 0
	reasons := make([]string, 0, 6)
	signals := make([]string, 0, 6)

	addReason := func(points int, reason string) {
		if points > 0 {
			score += points
		}
		reasons = append(reasons, reason)
	}

	filesChanged := summary.FilesChanged
	switch {
	case filesChanged >= 12:
		addReason(2, fmt.Sprintf("Large diff touching %d files.", filesChanged))
	case filesChanged >= 5:
		addReason(1, fmt.Sprintf("Diff touches %d files.", filesChanged))
	default:
		addReason(0, fmt.Sprintf("Diff touches %d files.", filesChanged))
	}

	switch {
	case summary.DirectoriesChanged >= 5:
		addReason(2, fmt.Sprintf("Cross-cutting change across %d directories.", summary.DirectoriesChanged))
	case summary.DirectoriesChanged >= 3:
		addReason(1, fmt.Sprintf("Change spans %d directories.", summary.DirectoriesChanged))
	}

	configPaths := collectMatchingPaths(changed, isConfigOrRuntimePath, 3)
	if len(configPaths) > 0 {
		addReason(2, fmt.Sprintf("Config/deploy/runtime files changed: %s.", strings.Join(backtickJoin(configPaths), ", ")))
		signals = append(signals, "Config/deploy/runtime files changed")
	}

	sensitivePaths := collectMatchingPaths(changed, isSensitivePath, 3)
	if len(sensitivePaths) > 0 {
		addReason(2, fmt.Sprintf("Infra or security-sensitive paths changed: %s.", strings.Join(backtickJoin(sensitivePaths), ", ")))
		signals = append(signals, "Infra/security-sensitive paths changed")
	}

	if summary.ProductionCodeChanged && !summary.TestFilesChanged {
		addReason(2, "Production code changed without test updates.")
		signals = append(signals, "Tests not changed")
	} else if summary.TestFilesChanged {
		signals = append(signals, "Tests changed")
	}

	if len(summary.Areas) >= 3 {
		signals = append(signals, fmt.Sprintf("Cross-cutting diff across %s", strings.Join(backtickJoin(summary.Areas[:min(len(summary.Areas), 3)]), ", ")))
	}
	if summary.ProductionCodeChanged {
		signals = append(signals, "Production code changed")
	}

	level := "low"
	switch {
	case score >= 5:
		level = "high"
	case score >= 2:
		level = "medium"
	}

	if len(reasons) == 0 {
		reasons = append(reasons, "No elevated risk signals detected.")
	}
	return Risk{
		Level:   level,
		Score:   score,
		Reasons: reasons,
	}, uniqueStrings(signals)
}

func suggestReviewers(changed []ChangedFile, codeowners string, history []HistoryTouch, excluded []string) ([]ReviewerSuggestion, string) {
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, reviewer := range excluded {
		if normalized := normalizeReviewer(reviewer); normalized != "" {
			excludedSet[normalized] = struct{}{}
		}
	}

	candidates := make(map[string]*reviewerCandidate)
	if strings.TrimSpace(codeowners) != "" {
		rules := parseCODEOWNERS(codeowners)
		for _, file := range changed {
			for _, reviewer := range matchCODEOWNERS(rules, file.Path) {
				normalized := normalizeReviewer(reviewer)
				if normalized == "" {
					continue
				}
				if _, skip := excludedSet[normalized]; skip {
					continue
				}
				candidate := ensureCandidate(candidates, normalized)
				candidate.confidenceRank = max(candidate.confidenceRank, 2)
				candidate.codeownersPaths[file.Path] = struct{}{}
			}
		}
	}

	for _, touch := range history {
		path := cleanPath(touch.Path)
		if path == "" {
			continue
		}
		normalized := normalizeReviewer(touch.Reviewer)
		if normalized == "" {
			continue
		}
		if _, skip := excludedSet[normalized]; skip {
			continue
		}
		candidate := ensureCandidate(candidates, normalized)
		candidate.confidenceRank = max(candidate.confidenceRank, 1)
		candidate.historyPaths[path] = struct{}{}
		candidate.historyTouches++
	}

	ordered := make([]*reviewerCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if len(candidate.codeownersPaths) == 0 && len(candidate.historyPaths) == 0 {
			continue
		}
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left := ordered[i]
		right := ordered[j]
		if left.confidenceRank != right.confidenceRank {
			return left.confidenceRank > right.confidenceRank
		}
		if len(left.codeownersPaths) != len(right.codeownersPaths) {
			return len(left.codeownersPaths) > len(right.codeownersPaths)
		}
		if len(left.historyPaths) != len(right.historyPaths) {
			return len(left.historyPaths) > len(right.historyPaths)
		}
		if left.historyTouches != right.historyTouches {
			return left.historyTouches > right.historyTouches
		}
		return left.reviewer < right.reviewer
	})

	if len(ordered) == 0 {
		return nil, "No high-confidence reviewer suggestion from CODEOWNERS or Git history."
	}
	if len(ordered) > 3 {
		ordered = ordered[:3]
	}

	out := make([]ReviewerSuggestion, 0, len(ordered))
	for _, candidate := range ordered {
		reasons := make([]string, 0, 2)
		confidence := "medium"
		if len(candidate.codeownersPaths) > 0 {
			reasons = append(reasons, fmt.Sprintf("CODEOWNERS matched %d changed path(s)", len(candidate.codeownersPaths)))
			confidence = "high"
		}
		if len(candidate.historyPaths) > 0 {
			reasons = append(reasons, fmt.Sprintf("recent git history touched %d changed file(s)", len(candidate.historyPaths)))
		}
		out = append(out, ReviewerSuggestion{
			Reviewer:   candidate.reviewer,
			Confidence: confidence,
			Reasons:    reasons,
		})
	}

	return out, fmt.Sprintf("%d reviewer suggestion(s) from deterministic repo-local signals.", len(out))
}

func ensureCandidate(candidates map[string]*reviewerCandidate, reviewer string) *reviewerCandidate {
	if current, ok := candidates[reviewer]; ok {
		return current
	}
	current := &reviewerCandidate{
		reviewer:        reviewer,
		codeownersPaths: make(map[string]struct{}),
		historyPaths:    make(map[string]struct{}),
	}
	candidates[reviewer] = current
	return current
}

func parseCODEOWNERS(body string) []codeownersRule {
	lines := strings.Split(body, "\n")
	out := make([]codeownersRule, 0)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		owners := make([]string, 0, len(fields)-1)
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "#") {
				break
			}
			owners = append(owners, field)
		}
		if len(owners) == 0 {
			continue
		}
		regex, err := compileCODEOWNERSPattern(fields[0])
		if err != nil {
			continue
		}
		out = append(out, codeownersRule{owners: owners, regex: regex})
	}
	return out
}

func matchCODEOWNERS(rules []codeownersRule, path string) []string {
	path = cleanPath(path)
	if path == "" {
		return nil
	}
	var owners []string
	for _, rule := range rules {
		if rule.regex.MatchString(path) {
			owners = rule.owners
		}
	}
	return owners
}

func compileCODEOWNERSPattern(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	rootAnchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	var builder strings.Builder
	if rootAnchored {
		builder.WriteString("^")
	} else {
		builder.WriteString("(^|.*/)")
	}
	for i := 0; i < len(pattern); {
		switch {
		case i+1 < len(pattern) && pattern[i:i+2] == "**":
			builder.WriteString(".*")
			i += 2
		case pattern[i] == '*':
			builder.WriteString(`[^/]*`)
			i++
		case pattern[i] == '?':
			builder.WriteString(`[^/]`)
			i++
		default:
			builder.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	builder.WriteString("$")
	compiled, err := regexp.Compile(builder.String())
	if err != nil {
		return nil, fmt.Errorf("compile CODEOWNERS pattern %q: %w", pattern, err)
	}
	return compiled, nil
}

func normalizeReviewer(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if matches := reviewerEmailRegex.FindStringSubmatch(strings.ToLower(raw)); len(matches) == 2 {
		return "@" + matches[1]
	}
	if i := strings.Index(raw, "<"); i >= 0 {
		if j := strings.Index(raw[i:], ">"); j > 0 {
			if normalized := normalizeReviewer(raw[i+1 : i+j]); normalized != "" {
				return normalized
			}
		}
	}
	raw = strings.TrimPrefix(raw, "@")
	raw = strings.ToLower(raw)
	if reviewerTeamRegex.MatchString(raw) || reviewerNameRegex.MatchString(raw) {
		return "@" + raw
	}
	return ""
}

func cleanPath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	path = strings.TrimPrefix(path, "./")
	return strings.Trim(path, "/")
}

func topLevelArea(path string) string {
	path = cleanPath(path)
	if path == "" {
		return "."
	}
	parts := strings.Split(path, "/")
	return parts[0]
}

func isTestPath(path string) bool {
	path = cleanPath(path)
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, "_test.go") {
		return true
	}
	parts := strings.Split(lower, "/")
	for _, part := range parts {
		if part == "test" || part == "tests" || part == "__tests__" {
			return true
		}
	}
	return false
}

func isProductionCodePath(path string) bool {
	path = cleanPath(path)
	if path == "" || isTestPath(path) {
		return false
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasPrefix(lower, "docs/"):
		return false
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".rst"):
		return false
	default:
		return true
	}
}

func isConfigOrRuntimePath(path string) bool {
	lower := strings.ToLower(cleanPath(path))
	switch {
	case strings.HasPrefix(lower, ".github/workflows/"):
		return true
	case strings.HasPrefix(lower, "deploy/"), strings.HasPrefix(lower, "runner/"):
		return true
	case strings.Contains(lower, "dockerfile"), strings.HasSuffix(lower, ".service"), strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"), strings.HasSuffix(lower, ".toml"), strings.HasSuffix(lower, ".json"), strings.HasSuffix(lower, ".sql"):
		return true
	default:
		return false
	}
}

func isSensitivePath(path string) bool {
	lower := strings.ToLower(cleanPath(path))
	switch {
	case strings.HasPrefix(lower, "internal/credentials/"):
		return true
	case strings.HasPrefix(lower, "internal/deploy/"):
		return true
	case strings.HasPrefix(lower, "internal/remote/"):
		return true
	case strings.HasPrefix(lower, "internal/runner/"):
		return true
	case strings.HasPrefix(lower, "cmd/rascald/"):
		return true
	default:
		return false
	}
}

func collectMatchingPaths(changed []ChangedFile, match func(string) bool, limit int) []string {
	out := make([]string, 0, limit)
	for _, file := range changed {
		if !match(file.Path) {
			continue
		}
		out = append(out, file.Path)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func backtickJoin(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, "`"+value+"`")
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = whitespacePattern.ReplaceAllString(strings.TrimSpace(value), " ")
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
