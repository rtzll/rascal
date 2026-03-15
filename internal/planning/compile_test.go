package planning

import (
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/runtrigger"
)

func TestCompileIssueBriefExtractsStructuredFields(t *testing.T) {
	input := Input{
		Trigger:     runtrigger.NameIssueLabel,
		Repo:        "owner/repo",
		Instruction: "Compile trigger input into a structured run brief\n\nSummary paragraph.",
		IssueNumber: 162,
		Sources: []Source{
			IssueSource(
				"Compile trigger input into a structured run brief",
				strings.TrimSpace(`
## Summary
Rascal should normalize trigger input before execution.

## Acceptance Criteria
- [ ] Persist run-brief.json
- [ ] Persist run-brief.md

## Constraints
- Do not invent requirements.

## Suggested Test Plan
- Run make test

## Implementation Notes
- Check internal/orchestrator/runs.go
- Update /rascal-meta/commit_message.txt

> quoted prior feedback that should not dominate

`+"```go\nfmt.Println(\"ignore code block\")\n```"+`
`),
				"https://github.com/owner/repo/issues/162",
			),
		},
	}

	compiled, err := Compile(input)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	if got := compiled.Brief.PrimaryObjective.Text; got != "Compile trigger input into a structured run brief" {
		t.Fatalf("primary objective = %q", got)
	}
	if len(compiled.Brief.AcceptanceCriteria) != 2 {
		t.Fatalf("acceptance criteria = %d, want 2", len(compiled.Brief.AcceptanceCriteria))
	}
	if len(compiled.Brief.Constraints) == 0 || !strings.Contains(compiled.Brief.Constraints[0].Text, "Do not invent requirements") {
		t.Fatalf("constraints = %+v", compiled.Brief.Constraints)
	}
	if len(compiled.Brief.Validation) == 0 || compiled.Brief.Validation[0].Text != "Run make test" {
		t.Fatalf("validation = %+v", compiled.Brief.Validation)
	}
	if !containsPath(compiled.Brief.RelevantFiles, "internal/orchestrator/runs.go") {
		t.Fatalf("relevant files missing internal/orchestrator/runs.go: %+v", compiled.Brief.RelevantFiles)
	}
	if !containsPath(compiled.Brief.RelevantFiles, "/rascal-meta/commit_message.txt") {
		t.Fatalf("relevant files missing /rascal-meta/commit_message.txt: %+v", compiled.Brief.RelevantFiles)
	}
}

func TestCompilePRReviewCommentPrefersFeedbackText(t *testing.T) {
	input := Input{
		Trigger:     runtrigger.NamePRReviewComment,
		Repo:        "owner/repo",
		Instruction: "Address PR #99 inline review comment",
		PRNumber:    99,
		Sources: []Source{
			PRReviewCommentSource(strings.TrimSpace(`
> old quoted feedback
Please rename helper in internal/orchestrator/runs.go.

Do not change API behavior.

`+"```go\nfmt.Println(\"ignore\")\n```"), "internal/orchestrator/runs.go:120", "reviewer"),
		},
	}

	compiled, err := Compile(input)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	if got := compiled.Brief.PrimaryObjective.Text; got != "Please rename helper in internal/orchestrator/runs.go." {
		t.Fatalf("primary objective = %q", got)
	}
	if len(compiled.Brief.Constraints) == 0 || !strings.Contains(compiled.Brief.Constraints[0].Text, "Do not change API behavior") {
		t.Fatalf("constraints = %+v", compiled.Brief.Constraints)
	}
	if !containsPath(compiled.Brief.RelevantFiles, "internal/orchestrator/runs.go") {
		t.Fatalf("relevant files = %+v", compiled.Brief.RelevantFiles)
	}
}

func TestCompileIssueReferenceRecordsAmbiguity(t *testing.T) {
	input := Input{
		Trigger:     runtrigger.NameIssueAPI,
		Repo:        "owner/repo",
		Instruction: "Work on issue #7 in owner/repo",
		IssueNumber: 7,
		Sources: []Source{
			ReferenceSource("Issue reference", "owner/repo#7"),
		},
	}

	compiled, err := Compile(input)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	if !containsNote(compiled.Brief.Ambiguities, "GitHub issue content was not available") {
		t.Fatalf("ambiguities = %+v", compiled.Brief.Ambiguities)
	}
}

func containsPath(paths []PathRef, want string) bool {
	for _, path := range paths {
		if path.Path == want {
			return true
		}
	}
	return false
}

func containsNote(notes []Note, want string) bool {
	for _, note := range notes {
		if strings.Contains(note.Text, want) {
			return true
		}
	}
	return false
}
