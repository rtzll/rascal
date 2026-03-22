package runtime

import (
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/runtrigger"
)

func TestNormalizeRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want Runtime
	}{
		{name: "default empty", in: "", want: RuntimeGooseCodex},
		{name: "default unknown", in: "other", want: RuntimeGooseCodex},
		{name: "codex explicit", in: " codex ", want: RuntimeCodex},
		{name: "goose-codex explicit", in: "GOOSE-CODEX", want: RuntimeGooseCodex},
		{name: "goose alias", in: "GOOSE", want: RuntimeGooseCodex},
		{name: "claude explicit", in: " claude ", want: RuntimeClaude},
		{name: "goose-claude explicit", in: " goose-claude ", want: RuntimeGooseClaude},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := NormalizeRuntime(tt.in); got != tt.want {
				t.Fatalf("NormalizeRuntime(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    Runtime
		wantErr bool
	}{
		{name: "default empty", in: "", want: RuntimeGooseCodex},
		{name: "codex explicit", in: " codex ", want: RuntimeCodex},
		{name: "goose-codex explicit", in: "GOOSE-CODEX", want: RuntimeGooseCodex},
		{name: "goose alias", in: "GOOSE", want: RuntimeGooseCodex},
		{name: "claude explicit", in: " claude ", want: RuntimeClaude},
		{name: "goose-claude explicit", in: " goose-claude ", want: RuntimeGooseClaude},
		{name: "invalid", in: "other", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseRuntime(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseRuntime(%q) error = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRuntime(%q) returned error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseRuntime(%q) = %q, want %q", tt.in, got, tt.want)
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

func TestRuntimeHarness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		runtime Runtime
		want    Harness
	}{
		{name: "goose-codex is goose", runtime: RuntimeGooseCodex, want: HarnessGoose},
		{name: "goose-claude is goose", runtime: RuntimeGooseClaude, want: HarnessGoose},
		{name: "codex is direct", runtime: RuntimeCodex, want: HarnessDirect},
		{name: "claude is direct", runtime: RuntimeClaude, want: HarnessDirect},
		{name: "empty defaults to goose", runtime: Runtime(""), want: HarnessGoose},
		{name: "unknown defaults to goose", runtime: Runtime("other"), want: HarnessGoose},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.runtime.Harness(); got != tt.want {
				t.Fatalf("Runtime(%q).Harness() = %q, want %q", tt.runtime, got, tt.want)
			}
		})
	}
}

func TestRuntimeProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		runtime Runtime
		want    ModelProvider
	}{
		{name: "codex maps to codex", runtime: RuntimeCodex, want: ModelProviderCodex},
		{name: "goose-codex maps to codex", runtime: RuntimeGooseCodex, want: ModelProviderCodex},
		{name: "claude maps to anthropic", runtime: RuntimeClaude, want: ModelProviderAnthropic},
		{name: "goose-claude maps to anthropic", runtime: RuntimeGooseClaude, want: ModelProviderAnthropic},
		{name: "empty maps to codex", runtime: Runtime(""), want: ModelProviderCodex},
		{name: "unknown maps to codex", runtime: Runtime("other"), want: ModelProviderCodex},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.runtime.Provider(); got != tt.want {
				t.Fatalf("Runtime(%q).Provider() = %q, want %q", tt.runtime, got, tt.want)
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
