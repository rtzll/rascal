package planning

import "github.com/rtzll/rascal/internal/runtrigger"

const SchemaVersion = "v1"

type SourceKind string

const (
	SourceKindManualPrompt       SourceKind = "manual_prompt"
	SourceKindGitHubIssue        SourceKind = "github_issue"
	SourceKindGitHubPRComment    SourceKind = "github_pr_comment"
	SourceKindGitHubPRReview     SourceKind = "github_pr_review"
	SourceKindGitHubPRReviewNote SourceKind = "github_pr_review_comment"
	SourceKindGitHubPRThread     SourceKind = "github_pr_review_thread"
	SourceKindTriggerContext     SourceKind = "trigger_context"
	SourceKindReference          SourceKind = "reference"
)

type Input struct {
	Version     string          `json:"version"`
	Trigger     runtrigger.Name `json:"trigger"`
	Repo        string          `json:"repo,omitempty"`
	Instruction string          `json:"instruction,omitempty"`
	Context     string          `json:"context,omitempty"`
	IssueNumber int             `json:"issue_number,omitempty"`
	PRNumber    int             `json:"pr_number,omitempty"`
	Sources     []Source        `json:"sources,omitempty"`
}

type Source struct {
	ID       string     `json:"id,omitempty"`
	Kind     SourceKind `json:"kind"`
	Label    string     `json:"label,omitempty"`
	URL      string     `json:"url,omitempty"`
	Author   string     `json:"author,omitempty"`
	Location string     `json:"location,omitempty"`
	Text     string     `json:"text,omitempty"`
}

type RunBrief struct {
	Version            string          `json:"version"`
	Trigger            runtrigger.Name `json:"trigger"`
	PrimaryObjective   Field           `json:"primary_objective"`
	BackgroundSummary  Field           `json:"background_summary,omitempty"`
	Constraints        []Item          `json:"constraints,omitempty"`
	AcceptanceCriteria []Item          `json:"acceptance_criteria,omitempty"`
	RelevantFiles      []PathRef       `json:"relevant_files,omitempty"`
	Validation         []Item          `json:"validation,omitempty"`
	FollowUp           []Item          `json:"follow_up,omitempty"`
	Ambiguities        []Note          `json:"ambiguities,omitempty"`
	Assumptions        []Note          `json:"assumptions,omitempty"`
	Sources            []SourceRef     `json:"sources,omitempty"`
}

type Field struct {
	Text       string   `json:"text,omitempty"`
	SourceIDs  []string `json:"source_ids,omitempty"`
	Derivation string   `json:"derivation,omitempty"`
}

type Item struct {
	Text       string   `json:"text"`
	SourceIDs  []string `json:"source_ids,omitempty"`
	Derivation string   `json:"derivation,omitempty"`
}

type PathRef struct {
	Path       string   `json:"path"`
	SourceIDs  []string `json:"source_ids,omitempty"`
	Derivation string   `json:"derivation,omitempty"`
}

type Note struct {
	Text       string   `json:"text"`
	SourceIDs  []string `json:"source_ids,omitempty"`
	Derivation string   `json:"derivation,omitempty"`
}

type SourceRef struct {
	ID       string     `json:"id"`
	Kind     SourceKind `json:"kind"`
	Label    string     `json:"label,omitempty"`
	URL      string     `json:"url,omitempty"`
	Location string     `json:"location,omitempty"`
}

type SourceSummary struct {
	ID       string     `json:"id"`
	Kind     SourceKind `json:"kind"`
	Label    string     `json:"label,omitempty"`
	URL      string     `json:"url,omitempty"`
	Location string     `json:"location,omitempty"`
	Preview  string     `json:"preview,omitempty"`
}

type Compiled struct {
	Input Input    `json:"input"`
	Brief RunBrief `json:"brief"`
}
