package orchestrator

import (
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/credentials"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/state"
)

func TestExecuteRunPostsTerminalFailureFeedbackForCredentialLeaseFailure(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	gh := &recordingGitHubClient{}
	s.GitHub = gh
	s.Notifier = NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, s.Store, gh)
	s.Config.GitHubToken = "token"
	s.Broker = credentials.NewBroker(nil, nil, nil, 0)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_credential_failure_feedback",
		TaskID:      "owner/repo#42",
		Repo:        "owner/repo",
		Instruction: "Investigate issue #42",
		BaseBranch:  "main",
		HeadBranch:  "rascal/issue-42",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 42,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	s.ExecuteRun(run.ID)

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	if !strings.Contains(updated.Error, "acquire credential lease: no credentials available") {
		t.Fatalf("unexpected error: %q", updated.Error)
	}
	if calls := launcher.Calls(); calls != 0 {
		t.Fatalf("expected launcher not to start, got calls=%d", calls)
	}

	reactions := gh.reactions()
	if len(reactions) != 2 {
		t.Fatalf("expected eyes and confused reactions, got %+v", reactions)
	}
	if reactions[0].issueNumber != 42 || reactions[0].content != ghapi.ReactionEyes {
		t.Fatalf("expected first reaction to be eyes for issue 42, got %+v", reactions[0])
	}
	if reactions[1].issueNumber != 42 || reactions[1].content != ghapi.ReactionConfused {
		t.Fatalf("expected second reaction to be confused for issue 42, got %+v", reactions[1])
	}

	comments := gh.comments()
	if len(comments) != 1 {
		t.Fatalf("expected one failure comment, got %d", len(comments))
	}
	if comments[0].repo != "owner/repo" || comments[0].issueNumber != 42 {
		t.Fatalf("unexpected failure comment target: %+v", comments[0])
	}
	if !strings.Contains(comments[0].body, "Reason: acquire credential lease: no credentials available") {
		t.Fatalf("expected failure reason in comment, got body:\n%s", comments[0].body)
	}
}
