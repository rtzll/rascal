package orchestrator

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

func (s *Server) CreateAndQueueRun(req RunRequest) (state.Run, error) {
	if s.isDraining() {
		return state.Run{}, errServerDraining
	}
	req.Repo = state.NormalizeRepo(req.Repo)
	req.Instruction = strings.TrimSpace(req.Instruction)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.BaseBranch = strings.TrimSpace(req.BaseBranch)
	req.HeadBranch = strings.TrimSpace(req.HeadBranch)
	req.Context = strings.TrimSpace(req.Context)
	req.CreatedByUserID = strings.TrimSpace(req.CreatedByUserID)
	if req.Repo == "" || req.Instruction == "" {
		return state.Run{}, fmt.Errorf("repo and task are required")
	}
	if req.CreatedByUserID == "" {
		req.CreatedByUserID = "system"
	}
	if req.Trigger == "" {
		req.Trigger = runtrigger.NameCLI
	} else {
		req.Trigger = runtrigger.Normalize(req.Trigger.String())
		if !req.Trigger.IsKnown() {
			return state.Run{}, fmt.Errorf("unknown workflow trigger %q", req.Trigger)
		}
	}
	if req.PRStatus == "" {
		if req.PRNumber > 0 {
			req.PRStatus = state.PRStatusOpen
		} else {
			req.PRStatus = state.PRStatusNone
		}
	}
	debugEnabled := true
	if req.Debug != nil {
		debugEnabled = *req.Debug
	}

	runID, err := state.NewRunID()
	if err != nil {
		return state.Run{}, fmt.Errorf("create run ID: %w", err)
	}
	if req.TaskID == "" {
		req.TaskID = runID
	}
	if s.Store.IsTaskCompleted(req.TaskID) {
		return state.Run{}, errTaskCompleted
	}
	effectiveRuntime := s.Config.AgentRuntime
	if req.AgentRuntime != nil {
		effectiveRuntime = runtime.NormalizeRuntime(string(*req.AgentRuntime))
	}

	if existingTask, ok := s.Store.GetTask(req.TaskID); ok && existingTask.AgentRuntime != effectiveRuntime {
		if err := s.Store.DeleteTaskSession(req.TaskID); err != nil {
			return state.Run{}, fmt.Errorf("clear stale task session for backend migration: %w", err)
		}
	}

	lastRun, hasLastRun := s.Store.LastRunForTask(req.TaskID)
	if req.BaseBranch == "" {
		if hasLastRun && lastRun.BaseBranch != "" {
			req.BaseBranch = lastRun.BaseBranch
		} else {
			req.BaseBranch = "main"
		}
	}
	if req.HeadBranch == "" {
		if hasLastRun && (req.Trigger == runtrigger.NamePRComment || req.Trigger == runtrigger.NamePRReview) && lastRun.HeadBranch != "" {
			req.HeadBranch = lastRun.HeadBranch
		} else {
			req.HeadBranch = BuildHeadBranch(req.TaskID, req.Instruction, runID)
		}
	}

	runDir := filepath.Join(s.Config.DataDir, "runs", runID)

	_, err = s.Store.UpsertTask(state.UpsertTaskInput{
		ID:           req.TaskID,
		Repo:         req.Repo,
		AgentRuntime: effectiveRuntime,
		IssueNumber:  req.IssueNumber,
		PRNumber:     req.PRNumber,
	})
	if err != nil {
		return state.Run{}, fmt.Errorf("upsert task: %w", err)
	}
	if err := s.Store.SetTaskCreatedByUser(req.TaskID, req.CreatedByUserID); err != nil {
		return state.Run{}, fmt.Errorf("set task requester: %w", err)
	}

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:           runID,
		TaskID:       req.TaskID,
		Repo:         req.Repo,
		Instruction:  req.Instruction,
		AgentRuntime: effectiveRuntime,
		BaseBranch:   req.BaseBranch,
		HeadBranch:   req.HeadBranch,
		Trigger:      req.Trigger,
		RunDir:       runDir,
		IssueNumber:  req.IssueNumber,
		PRNumber:     req.PRNumber,
		PRStatus:     req.PRStatus,
		Context:      req.Context,
		Debug:        boolPtr(debugEnabled),
	})
	if err != nil {
		return state.Run{}, fmt.Errorf("persist run: %w", err)
	}
	if err := s.Store.SetRunCreatedByUser(run.ID, req.CreatedByUserID); err != nil {
		return state.Run{}, fmt.Errorf("set run requester: %w", err)
	}

	if err := s.WriteRunFiles(run, req.ResponseTarget); err != nil {
		if _, transErr := s.SM.Transition(run.ID, state.StatusFailed, WithError(err.Error())); transErr != nil {
			log.Printf("run %s fail on write run files failed: %v", run.ID, transErr)
		}
		return state.Run{}, fmt.Errorf("prepare run files: %w", err)
	}
	if err := s.WriteRunResponseTarget(run, req.ResponseTarget); err != nil {
		if _, transErr := s.SM.Transition(run.ID, state.StatusFailed, WithError(err.Error())); transErr != nil {
			log.Printf("run %s fail on write response target failed: %v", run.ID, transErr)
		}
		return state.Run{}, fmt.Errorf("prepare run response target: %w", err)
	}
	s.ScheduleRuns(run.TaskID)
	return run, nil
}

func (s *Server) WriteRunFiles(run state.Run, target *RunResponseTarget) (err error) {
	if err := os.MkdirAll(filepath.Join(run.RunDir, "codex"), 0o755); err != nil {
		return fmt.Errorf("create codex run directory: %w", err)
	}

	ctxPayload := buildInstructionContext(run, target)
	ctxData, err := json.MarshalIndent(ctxPayload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "context.json"), ctxData, 0o644); err != nil {
		return fmt.Errorf("write run context file: %w", err)
	}

	instructions := InstructionText(run, target)
	if err := os.WriteFile(filepath.Join(run.RunDir, "instructions.md"), []byte(instructions), 0o644); err != nil {
		return fmt.Errorf("write run instructions: %w", err)
	}
	persistentInstructions := PersistentInstructionText(run)
	if err := os.WriteFile(filepath.Join(run.RunDir, "persistent_instructions.md"), []byte(persistentInstructions), 0o644); err != nil {
		return fmt.Errorf("write persistent run instructions: %w", err)
	}

	logLine := fmt.Sprintf("[%s] queued run=%s task=%s trigger=%s\n", time.Now().UTC().Format(time.RFC3339), run.ID, run.TaskID, run.Trigger)
	f, err := os.OpenFile(filepath.Join(run.RunDir, "runner.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open runner log: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close runner log: %w", closeErr)
		}
	}()
	_, err = f.WriteString(logLine)
	if err != nil {
		return fmt.Errorf("write runner log entry: %w", err)
	}
	return nil
}

type RunContextFile struct {
	RunID                 string               `json:"run_id"`
	TaskID                string               `json:"task_id"`
	Repo                  string               `json:"repo"`
	Instruction           string               `json:"instruction"`
	Trigger               string               `json:"trigger"`
	BaseBranch            string               `json:"base_branch,omitempty"`
	HeadBranch            string               `json:"head_branch,omitempty"`
	IssueNumber           int                  `json:"issue_number"`
	PRNumber              int                  `json:"pr_number"`
	Context               string               `json:"context"`
	Debug                 bool                 `json:"debug"`
	GitHubRepliesExpected bool                 `json:"github_replies_expected"`
	Capabilities          RunContextCapability `json:"capabilities"`
}

type RunContextCapability struct {
	Publish       RunPublishCapability `json:"publish"`
	PullRequest   RunPRCapability      `json:"pull_request"`
	GitHubComment RunCommentCapability `json:"github_comment"`
}

type RunPublishCapability struct {
	Available  bool   `json:"available"`
	Command    string `json:"command"`
	Remote     string `json:"remote,omitempty"`
	AllowedRef string `json:"allowed_ref,omitempty"`
}

type RunPRCapability struct {
	Available     bool   `json:"available"`
	Command       string `json:"command"`
	Repo          string `json:"repo,omitempty"`
	BaseBranch    string `json:"base_branch,omitempty"`
	HeadBranch    string `json:"head_branch,omitempty"`
	ExistingPR    int    `json:"existing_pr,omitempty"`
	DefaultLabel  string `json:"default_label,omitempty"`
	GitHubReplies bool   `json:"github_replies"`
}

type RunCommentCapability struct {
	Available        bool   `json:"available"`
	Command          string `json:"command"`
	Repo             string `json:"repo,omitempty"`
	IssueNumber      int    `json:"issue_number,omitempty"`
	ReviewThreadID   int64  `json:"review_thread_id,omitempty"`
	ReplyExpected    bool   `json:"reply_expected,omitempty"`
	RequestedTrigger string `json:"requested_trigger,omitempty"`
}

func (s *Server) WriteRunResponseTarget(run state.Run, target *RunResponseTarget) error {
	if target == nil {
		return nil
	}
	out := RunResponseTarget{
		Repo:           strings.TrimSpace(target.Repo),
		IssueNumber:    target.IssueNumber,
		RequestedBy:    strings.TrimSpace(target.RequestedBy),
		Trigger:        runtrigger.Normalize(target.Trigger.String()),
		ReviewThreadID: target.ReviewThreadID,
	}
	if out.Repo == "" {
		out.Repo = strings.TrimSpace(run.Repo)
	}
	if out.IssueNumber <= 0 {
		out.IssueNumber = run.PRNumber
	}
	if out.Trigger == "" {
		out.Trigger = runtrigger.Normalize(run.Trigger.String())
	}
	if out.Repo == "" || out.IssueNumber <= 0 {
		return nil
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run response target: %w", err)
	}
	path := filepath.Join(run.RunDir, RunResponseTargetFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write run response target: %w", err)
	}
	return nil
}

func InstructionText(run state.Run, target *RunResponseTarget) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, `# Rascal Run Instructions

Run ID: %s
Task ID: %s
Repository: %s
`, run.ID, run.TaskID, run.Repo)
	if run.IssueNumber > 0 {
		_, _ = fmt.Fprintf(&b, "Issue: #%d\n", run.IssueNumber)
	}
	if run.PRNumber > 0 {
		_, _ = fmt.Fprintf(&b, "Pull Request: #%d\n", run.PRNumber)
	}
	b.WriteString(`
## Task

`)
	b.WriteString(run.Instruction)
	b.WriteString(`

`)
	b.WriteString(`## Execution Context

- Trigger: ` + "`" + strings.TrimSpace(run.Trigger.String()) + "`" + `
- The repository is already cloned and checked out.
`)
	if strings.TrimSpace(run.BaseBranch) != "" {
		b.WriteString(`- Base branch: ` + "`" + strings.TrimSpace(run.BaseBranch) + "`" + `
`)
	}
	if strings.TrimSpace(run.HeadBranch) != "" {
		_, _ = fmt.Fprintf(&b, "- Head branch: `%s`\n", strings.TrimSpace(run.HeadBranch))
		_, _ = fmt.Fprintf(&b, "- Allowed publish ref: `origin/%s`\n", strings.TrimSpace(run.HeadBranch))
	}
	if expectsGitHubReply(run, target) {
		b.WriteString(`- GitHub replies expected: ` + "`yes`" + `
`)
	} else {
		b.WriteString(`- GitHub replies expected: ` + "`no`" + `
`)
	}
	b.WriteString(`
## Capabilities

- Local repo manipulation is normal agent work: edit files, run tests, inspect git state, commit, amend, rebase, and resolve conflicts locally.
`)
	if hasPublishCapability(run) {
		_, _ = fmt.Fprintf(&b, "- `publish`: run `rascal-runner capability publish` to update `origin/%s`.\n", strings.TrimSpace(run.HeadBranch))
		b.WriteString("- If you rewrite history, use `rascal-runner capability publish --force-with-lease`.\n")
	} else {
		b.WriteString("- `publish`: unavailable for this run.\n")
	}
	if hasPRCapability(run) {
		b.WriteString("- `pr`: run `rascal-runner capability pr --title ... --body-file ...` to create or update the pull request for `" + strings.TrimSpace(run.HeadBranch) + "` against `" + strings.TrimSpace(run.BaseBranch) + "`.\n")
	} else {
		b.WriteString("- `pr`: unavailable for this run.\n")
	}
	if repo, issueNumber, ok := capabilityCommentTarget(run, target); ok {
		b.WriteString("- `comment`: run `rascal-runner capability comment --body-file ...` to post on `" + repo + "#" + strconv.Itoa(issueNumber) + "`.\n")
	} else {
		b.WriteString("- `comment`: unavailable for this run.\n")
	}
	b.WriteString("- Rascal records final metadata after the run, but it does not commit, publish, create PRs, or reply on GitHub for you.\n")
	b.WriteString(`
## Constraints

- Do not ask for interactive input.
- Do not require MCP tools.
- Keep changes minimal and scoped to the requested task.
- Run ` + "`make lint`" + ` and ` + "`make test`" + ` before finishing if those targets exist.
- If one of those commands does not exist or cannot run, explain exactly why and run the closest equivalent checks instead.
- If you make changes, write /rascal-meta/commit_message.txt using a conventional commit title on the first line.
- Optionally add a commit body after a blank line in /rascal-meta/commit_message.txt.
`)
	if strings.TrimSpace(run.Context) != "" {
		b.WriteString(`
## Additional Context

`)
		b.WriteString(run.Context)
		b.WriteString(`
`)
	}
	return b.String()
}

func PersistentInstructionText(run state.Run) string {
	_ = run

	return `# Rascal Persistent Instructions

- Do not ask for interactive input.
- Do not require MCP tools.
- Keep changes minimal and scoped to the requested task.
- Do not overwrite, revert, or discard user changes you did not make unless the task explicitly requires it.
- Use the repository's existing patterns and conventions.
- Prefer the repository's documented workflow over inventing a new one.
- If the repository provides verification commands, run the relevant ones before finishing.
- Run ` + "`make lint`" + ` and ` + "`make test`" + ` before finishing if those targets exist.
- If one of those commands does not exist or cannot run, explain exactly why and run the closest equivalent checks instead.
- If you make changes, write /rascal-meta/commit_message.txt using a conventional commit title on the first line.
- Optionally add a commit body after a blank line in /rascal-meta/commit_message.txt.
- If working with GitHub branches or pull requests, only push to the designated Rascal branch for this run.
- If you must rewrite published history, prefer ` + "`git push --force-with-lease`" + ` over ` + "`git push --force`" + `.
- Before finishing, ensure the working tree is clean unless the task explicitly requires uncommitted output.
`
}

func buildInstructionContext(run state.Run, target *RunResponseTarget) RunContextFile {
	ctx := RunContextFile{
		RunID:                 run.ID,
		TaskID:                run.TaskID,
		Repo:                  run.Repo,
		Instruction:           run.Instruction,
		Trigger:               run.Trigger.String(),
		BaseBranch:            run.BaseBranch,
		HeadBranch:            run.HeadBranch,
		IssueNumber:           run.IssueNumber,
		PRNumber:              run.PRNumber,
		Context:               run.Context,
		Debug:                 run.Debug,
		GitHubRepliesExpected: expectsGitHubReply(run, target),
		Capabilities: RunContextCapability{
			Publish: RunPublishCapability{
				Available:  hasPublishCapability(run),
				Command:    "rascal-runner capability publish",
				Remote:     "origin",
				AllowedRef: run.HeadBranch,
			},
			PullRequest: RunPRCapability{
				Available:     hasPRCapability(run),
				Command:       "rascal-runner capability pr --title ... --body-file ...",
				Repo:          run.Repo,
				BaseBranch:    run.BaseBranch,
				HeadBranch:    run.HeadBranch,
				ExistingPR:    run.PRNumber,
				DefaultLabel:  "rascal",
				GitHubReplies: expectsGitHubReply(run, target),
			},
			GitHubComment: RunCommentCapability{
				Available: false,
				Command:   "rascal-runner capability comment --body-file ...",
			},
		},
	}
	if repo, issueNumber, ok := capabilityCommentTarget(run, target); ok {
		ctx.Capabilities.GitHubComment = RunCommentCapability{
			Available:        true,
			Command:          "rascal-runner capability comment --body-file ...",
			Repo:             repo,
			IssueNumber:      issueNumber,
			ReviewThreadID:   responseTargetReviewThreadID(target),
			ReplyExpected:    expectsGitHubReply(run, target),
			RequestedTrigger: responseTargetTrigger(target),
		}
	}
	return ctx
}

func hasPublishCapability(run state.Run) bool {
	return strings.TrimSpace(run.HeadBranch) != ""
}

func hasPRCapability(run state.Run) bool {
	return strings.TrimSpace(run.BaseBranch) != "" && strings.TrimSpace(run.HeadBranch) != ""
}

func capabilityCommentTarget(run state.Run, target *RunResponseTarget) (string, int, bool) {
	if target != nil {
		repo := strings.TrimSpace(target.Repo)
		if repo == "" {
			repo = strings.TrimSpace(run.Repo)
		}
		if repo != "" && target.IssueNumber > 0 {
			return repo, target.IssueNumber, true
		}
	}
	if strings.TrimSpace(run.Repo) == "" {
		return "", 0, false
	}
	if run.PRNumber > 0 {
		return strings.TrimSpace(run.Repo), run.PRNumber, true
	}
	if run.IssueNumber > 0 {
		return strings.TrimSpace(run.Repo), run.IssueNumber, true
	}
	return "", 0, false
}

func expectsGitHubReply(run state.Run, target *RunResponseTarget) bool {
	if target == nil {
		return runtrigger.Normalize(run.Trigger.String()).IsComment()
	}
	if target.IssueNumber > 0 || target.ReviewThreadID > 0 {
		switch runtrigger.Normalize(run.Trigger.String()) {
		case runtrigger.NamePRComment, runtrigger.NamePRReview, runtrigger.NamePRReviewComment, runtrigger.NamePRReviewThread, runtrigger.NameIssueEdited:
			return true
		}
	}
	return false
}

func responseTargetReviewThreadID(target *RunResponseTarget) int64 {
	if target == nil {
		return 0
	}
	return target.ReviewThreadID
}

func responseTargetTrigger(target *RunResponseTarget) string {
	if target == nil {
		return ""
	}
	return strings.TrimSpace(target.Trigger.String())
}

func BuildHeadBranch(taskID, task, runID string) string {
	source := strings.ToLower(strings.TrimSpace(taskID))
	if source == "" || strings.HasPrefix(source, "run_") || strings.HasPrefix(source, "task_") {
		lines := strings.Split(strings.TrimSpace(task), "\n")
		for _, line := range lines {
			line = strings.ToLower(strings.TrimSpace(line))
			if line != "" {
				source = line
				break
			}
		}
	}
	if source == "" {
		source = "task"
	}

	var cleaned strings.Builder
	for _, r := range source {
		switch {
		case r >= 'a' && r <= 'z':
			cleaned.WriteRune(r)
		case r >= '0' && r <= '9':
			cleaned.WriteRune(r)
		case r == '-' || r == '_' || r == '/':
			cleaned.WriteRune(r)
		default:
			cleaned.WriteByte('-')
		}
	}
	taskPart := strings.Trim(cleaned.String(), "-/_")
	if taskPart == "" {
		taskPart = "task"
	}
	if len(taskPart) > 48 {
		taskPart = taskPart[:48]
		taskPart = strings.Trim(taskPart, "-/_")
	}
	runSuffix := strings.TrimSpace(strings.TrimPrefix(runID, "run_"))
	if runSuffix == "" {
		runSuffix = strings.TrimSpace(runID)
	}
	if runSuffix == "" {
		runSuffix = "run"
	}
	if len(runSuffix) > 10 {
		runSuffix = runSuffix[:10]
	}
	return fmt.Sprintf("rascal/%s-%s", taskPart, runSuffix)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func boolPtr(v bool) *bool {
	return &v
}
