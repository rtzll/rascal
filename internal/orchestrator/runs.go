package orchestrator

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	req.PublishScope = state.NormalizePublishScope(req.PublishScope)
	req.PublishBranches = normalizePublishBranches(req.PublishBranches)
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
	switch req.PublishScope {
	case state.PublishScopeBranchScoped:
		req.PublishBranches = []string{req.HeadBranch}
	case state.PublishScopeTaskScoped:
		req.PublishBranches = appendIfMissing(req.PublishBranches, req.HeadBranch)
	default:
		req.PublishBranches = nil
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
		ID:              runID,
		TaskID:          req.TaskID,
		Repo:            req.Repo,
		Instruction:     req.Instruction,
		AgentRuntime:    effectiveRuntime,
		BaseBranch:      req.BaseBranch,
		HeadBranch:      req.HeadBranch,
		Trigger:         req.Trigger,
		RunDir:          runDir,
		IssueNumber:     req.IssueNumber,
		PRNumber:        req.PRNumber,
		PRStatus:        req.PRStatus,
		PublishScope:    req.PublishScope,
		PublishBranches: req.PublishBranches,
		Context:         req.Context,
		Debug:           boolPtr(debugEnabled),
	})
	if err != nil {
		return state.Run{}, fmt.Errorf("persist run: %w", err)
	}
	if err := s.Store.SetRunCreatedByUser(run.ID, req.CreatedByUserID); err != nil {
		return state.Run{}, fmt.Errorf("set run requester: %w", err)
	}

	if err := s.WriteRunFiles(run); err != nil {
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

func (s *Server) WriteRunFiles(run state.Run) (err error) {
	if err := os.MkdirAll(filepath.Join(run.RunDir, "codex"), 0o755); err != nil {
		return fmt.Errorf("create codex run directory: %w", err)
	}

	ctxPayload := RunContextFile{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Instruction: run.Instruction,
		Trigger:     run.Trigger.String(),
		IssueNumber: run.IssueNumber,
		PRNumber:    run.PRNumber,
		Context:     run.Context,
		Debug:       run.Debug,
	}
	ctxData, err := json.MarshalIndent(ctxPayload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "context.json"), ctxData, 0o644); err != nil {
		return fmt.Errorf("write run context file: %w", err)
	}

	instructions := InstructionText(run)
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
	RunID       string `json:"run_id"`
	TaskID      string `json:"task_id"`
	Repo        string `json:"repo"`
	Instruction string `json:"instruction"`
	Trigger     string `json:"trigger"`
	IssueNumber int    `json:"issue_number"`
	PRNumber    int    `json:"pr_number"`
	Context     string `json:"context"`
	Debug       bool   `json:"debug"`
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

func InstructionText(run state.Run) string {
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
	if shouldIncludeGitContext(run) {
		b.WriteString(`## Git Context

- Remote: ` + "`origin`" + `
- Base branch: ` + "`" + strings.TrimSpace(run.BaseBranch) + "`" + `
- Head branch: ` + "`" + strings.TrimSpace(run.HeadBranch) + "`" + `
- Publish scope: ` + "`" + string(state.NormalizePublishScope(run.PublishScope)) + "`" + `
- Allowed publish branches: ` + formatPublishBranchesForInstruction(run.PublishBranches) + `
- The repository is already cloned and checked out.
- You may inspect and mutate local git state freely.
- Git commit identity is preconfigured for you.
- Rascal will not auto-commit, auto-push, or auto-create a PR after the agent finishes.
- Publish branch updates with ` + "`rascal-publish push --branch " + strings.TrimSpace(run.HeadBranch) + "`" + `.
- If you rewrite history, publish with ` + "`rascal-publish push --force-with-lease --branch " + strings.TrimSpace(run.HeadBranch) + "`" + `.
- Do not attempt to publish outside the declared scope.
- Before finishing, ensure the working tree is clean and publish any branch updates you want Rascal to retain.
`)
		b.WriteString(`
`)
	}
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

func shouldIncludeGitContext(run state.Run) bool {
	return strings.TrimSpace(run.BaseBranch) != "" && strings.TrimSpace(run.HeadBranch) != ""
}

func normalizePublishBranches(branches []string) []string {
	if len(branches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(branches))
	out := make([]string, 0, len(branches))
	for _, branch := range branches {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}
		if _, ok := seen[branch]; ok {
			continue
		}
		seen[branch] = struct{}{}
		out = append(out, branch)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func appendIfMissing(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return normalizePublishBranches(values)
	}
	for _, existing := range values {
		if strings.TrimSpace(existing) == value {
			return normalizePublishBranches(values)
		}
	}
	return append(normalizePublishBranches(values), value)
}

func formatPublishBranchesForInstruction(branches []string) string {
	branches = normalizePublishBranches(branches)
	if len(branches) == 0 {
		return "`(none)`"
	}
	quoted := make([]string, 0, len(branches))
	for _, branch := range branches {
		quoted = append(quoted, "`"+branch+"`")
	}
	return strings.Join(quoted, ", ")
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
