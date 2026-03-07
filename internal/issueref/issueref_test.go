package issueref

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantRepo string
		wantNum  int
		wantErr  string
	}{
		{
			name:     "valid",
			input:    "owner/repo#123",
			wantRepo: "owner/repo",
			wantNum:  123,
		},
		{
			name:     "valid trims and normalizes case",
			input:    " Owner/Repo # 42 ",
			wantRepo: "owner/repo",
			wantNum:  42,
		},
		{
			name:    "missing hash",
			input:   "owner/repo",
			wantErr: "expected OWNER/REPO#123",
		},
		{
			name:    "extra hash",
			input:   "owner/repo#1#2",
			wantErr: "expected OWNER/REPO#123",
		},
		{
			name:    "invalid repo",
			input:   "owner/repo/extra#1",
			wantErr: "repo must be OWNER/REPO",
		},
		{
			name:    "empty repo",
			input:   "#1",
			wantErr: "repo must be OWNER/REPO",
		},
		{
			name:    "non numeric issue",
			input:   "owner/repo#abc",
			wantErr: "issue number must be a positive integer",
		},
		{
			name:    "trailing chars in issue",
			input:   "owner/repo#12abc",
			wantErr: "issue number must be a positive integer",
		},
		{
			name:    "zero issue",
			input:   "owner/repo#0",
			wantErr: "issue number must be a positive integer",
		},
		{
			name:    "negative issue",
			input:   "owner/repo#-1",
			wantErr: "issue number must be a positive integer",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Parse(%q) expected error containing %q", tt.input, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Parse(%q) error %q, want substring %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tt.input, err)
			}
			if got.Repo != tt.wantRepo || got.Number != tt.wantNum {
				t.Fatalf("Parse(%q) = %#v, want repo=%q number=%d", tt.input, got, tt.wantRepo, tt.wantNum)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()

		got, err := Normalize(" Owner/Repo ", 7)
		if err != nil {
			t.Fatalf("Normalize returned error: %v", err)
		}
		if got.Repo != "owner/repo" || got.Number != 7 {
			t.Fatalf("Normalize = %#v, want repo owner/repo and number 7", got)
		}
		if got.String() != "owner/repo#7" {
			t.Fatalf("String() = %q, want owner/repo#7", got.String())
		}
	})

	t.Run("invalid repo", func(t *testing.T) {
		t.Parallel()

		_, err := Normalize("owner/repo/extra", 7)
		if err == nil || !strings.Contains(err.Error(), "repo must be OWNER/REPO") {
			t.Fatalf("expected repo validation error, got: %v", err)
		}
	})

	t.Run("invalid issue number", func(t *testing.T) {
		t.Parallel()

		_, err := Normalize("owner/repo", 0)
		if err == nil || !strings.Contains(err.Error(), "issue number must be a positive integer") {
			t.Fatalf("expected issue number validation error, got: %v", err)
		}
	})
}
