package runtrigger

import "testing"

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    Name
		wantErr bool
	}{
		{name: "cli", in: " cli ", want: NameCLI},
		{name: "review thread", in: "PR_REVIEW_THREAD", want: NamePRReviewThread},
		{name: "invalid", in: "issue", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) error = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("Parse(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseOrDefault(t *testing.T) {
	t.Parallel()

	got, err := ParseOrDefault("", NameCLI)
	if err != nil {
		t.Fatalf("ParseOrDefault returned error: %v", err)
	}
	if got != NameCLI {
		t.Fatalf("ParseOrDefault(empty) = %q, want %q", got, NameCLI)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()

	if !NamePRReviewComment.IsComment() {
		t.Fatal("expected pr_review_comment to be comment-triggered")
	}
	if !NameIssueLabel.IsIssue() {
		t.Fatal("expected issue_label to be issue-triggered")
	}
	if !NameRetry.EnablesPROnlySession() {
		t.Fatal("expected retry to enable pr-only sessions")
	}
	if NameIssueLabel.EnablesPROnlySession() {
		t.Fatal("did not expect issue_label to enable pr-only sessions")
	}
}
