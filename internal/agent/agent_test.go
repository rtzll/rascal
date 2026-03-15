package agent

import (
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/runtrigger"
)

func TestNormalizeBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want Backend
	}{
		{name: "default empty", in: "", want: BackendGoose},
		{name: "default unknown", in: "other", want: BackendGoose},
		{name: "codex explicit", in: " codex ", want: BackendCodex},
		{name: "goose explicit", in: "GOOSE", want: BackendGoose},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := NormalizeBackend(tt.in); got != tt.want {
				t.Fatalf("NormalizeBackend(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    Backend
		wantErr bool
	}{
		{name: "default empty", in: "", want: BackendGoose},
		{name: "codex explicit", in: " codex ", want: BackendCodex},
		{name: "goose explicit", in: "GOOSE", want: BackendGoose},
		{name: "invalid", in: "other", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseBackend(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseBackend(%q) error = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBackend(%q) returned error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseBackend(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeSessionMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want SessionMode
	}{
		{name: "default empty", in: "", want: SessionModeOff},
		{name: "default unknown", in: "sometimes", want: SessionModeOff},
		{name: "pr only", in: " PR-ONLY ", want: SessionModePROnly},
		{name: "all", in: "all", want: SessionModeAll},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := NormalizeSessionMode(tt.in); got != tt.want {
				t.Fatalf("NormalizeSessionMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSessionMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    SessionMode
		wantErr bool
	}{
		{name: "default empty", in: "", want: SessionModeOff},
		{name: "off explicit", in: " off ", want: SessionModeOff},
		{name: "pr only", in: " PR-ONLY ", want: SessionModePROnly},
		{name: "all", in: "all", want: SessionModeAll},
		{name: "invalid", in: "sometimes", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseSessionMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseSessionMode(%q) error = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSessionMode(%q) returned error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseSessionMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSessionEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    SessionMode
		trigger runtrigger.Name
		want    bool
	}{
		{name: "off mode", mode: SessionModeOff, trigger: runtrigger.NamePRComment, want: false},
		{name: "all mode", mode: SessionModeAll, trigger: runtrigger.NameIssueLabel, want: true},
		{name: "pr only supported trigger", mode: SessionModePROnly, trigger: runtrigger.NamePRReviewComment, want: true},
		{name: "pr only retry trigger", mode: SessionModePROnly, trigger: runtrigger.NameRetry, want: true},
		{name: "pr only unsupported trigger", mode: SessionModePROnly, trigger: runtrigger.NameIssueLabel, want: false},
		{name: "unknown mode defaults off", mode: SessionMode("custom"), trigger: runtrigger.NamePRComment, want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := SessionEnabled(tt.mode, tt.trigger); got != tt.want {
				t.Fatalf("SessionEnabled(%q, %q) = %t, want %t", tt.mode, tt.trigger, got, tt.want)
			}
		})
	}
}

func TestSessionTaskKeyStableAndSanitized(t *testing.T) {
	t.Parallel()

	keyA := SessionTaskKey(" Owner/Repo ", " issue#123 ")
	keyB := SessionTaskKey("Owner/Repo", "issue#123")
	keyC := SessionTaskKey("Owner/Repo", "issue#124")

	if keyA != keyB {
		t.Fatalf("expected trimmed inputs to produce a stable key, got %q vs %q", keyA, keyB)
	}
	if keyA == keyC {
		t.Fatalf("expected distinct task IDs to produce different keys, got %q", keyA)
	}
	if !strings.HasPrefix(keyA, "owner-repo-issue-123-") {
		t.Fatalf("expected sanitized prefix, got %q", keyA)
	}
	if len(keyA) > 56 {
		t.Fatalf("expected bounded key length <= 56, got %d", len(keyA))
	}
	for _, ch := range keyA {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			continue
		}
		t.Fatalf("unexpected key character %q in %q", ch, keyA)
	}
}

func TestSessionTaskKeyFallsBackWhenSlugIsEmpty(t *testing.T) {
	t.Parallel()

	key := SessionTaskKey("!!!", "***")
	if !strings.HasPrefix(key, "task-") {
		t.Fatalf("expected empty slug fallback, got %q", key)
	}
}
