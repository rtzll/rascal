package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/defaults"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/logs"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runsummary"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

func (s *Server) cleanupAgentSessionsBestEffort() {
	ttlDays := s.Config.EffectiveTaskSessionTTLDays()
	if ttlDays <= 0 {
		return
	}
	root := strings.TrimSpace(s.Config.EffectiveTaskSessionRoot())
	if root == "" {
		root = filepath.Join(s.Config.DataDir, defaults.AgentSessionDirName)
	}
	removed, err := CleanupStaleAgentSessionDirs(root, ttlDays, time.Now().UTC())
	if err != nil {
		log.Printf("agent session cleanup warning: root=%s ttl_days=%d error=%v", root, ttlDays, err)
		return
	}
	if removed > 0 {
		log.Printf("agent session cleanup: root=%s ttl_days=%d removed=%d", root, ttlDays, removed)
	}
}

func CleanupStaleAgentSessionDirs(root string, ttlDays int, now time.Time) (int, error) {
	if ttlDays <= 0 {
		return 0, nil
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read agent session directory %s: %w", root, err)
	}
	cutoff := now.AddDate(0, 0, -ttlDays)
	removed := 0
	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("stat agent session entry %s: %w", entry.Name(), infoErr)
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if rmErr := os.RemoveAll(path); rmErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("remove stale agent session dir %s: %w", path, rmErr)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

func resolveRunAgentLogPath(runDir string) (string, error) {
	primary := filepath.Join(strings.TrimSpace(runDir), agentLogFile)
	if info, err := os.Stat(primary); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("agent log path is a directory: %s", primary)
		}
		return primary, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat agent log path %s: %w", primary, err)
	}

	legacy := filepath.Join(strings.TrimSpace(runDir), legacyAgentLogFile)
	if info, err := os.Stat(legacy); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("legacy agent log path is a directory: %s", legacy)
		}
		return legacy, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat legacy agent log path %s: %w", legacy, err)
	}

	return primary, os.ErrNotExist
}

func tailRunAgentLog(runDir string, lines int) ([]string, string) {
	path, err := resolveRunAgentLogPath(runDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "(" + agentLogFile + " not found)"
		}
		return nil, "(" + agentLogFile + " unavailable)"
	}

	agentLines, err := logs.Tail(path, lines)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "(" + agentLogFile + " not found)"
		}
		return nil, "(" + agentLogFile + " unavailable)"
	}
	return agentLines, ""
}

func LoadRunResponseTarget(runDir string) (RunResponseTarget, bool, error) {
	path := filepath.Join(strings.TrimSpace(runDir), RunResponseTargetFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RunResponseTarget{}, false, nil
		}
		return RunResponseTarget{}, false, fmt.Errorf("read run response target: %w", err)
	}
	var target RunResponseTarget
	if err := json.Unmarshal(data, &target); err != nil {
		return RunResponseTarget{}, false, fmt.Errorf("decode run response target: %w", err)
	}
	target.Repo = strings.TrimSpace(target.Repo)
	target.RequestedBy = strings.TrimSpace(target.RequestedBy)
	target.Trigger = runtrigger.Normalize(target.Trigger.String())
	return target, true, nil
}

func RunCommentMarkerPath(runDir, markerFile string) string {
	return filepath.Join(strings.TrimSpace(runDir), markerFile)
}

func RunStartCommentMarkerPath(runDir string) string {
	return RunCommentMarkerPath(runDir, runStartCommentMarkerFile)
}

func RunCompletionCommentMarkerPath(runDir string) string {
	return RunCommentMarkerPath(runDir, runCompletionCommentMarkerFile)
}

func RunFailureCommentMarkerPath(runDir string) string {
	return RunCommentMarkerPath(runDir, runFailureCommentMarkerFile)
}

func RunCommentMarkerExists(runDir, markerFile, markerKind string) (bool, error) {
	path := RunCommentMarkerPath(runDir, markerFile)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s marker: %w", markerKind, err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("%s marker path is a directory: %s", markerKind, path)
	}
	return true, nil
}

func runStartCommentMarkerExists(runDir string) (bool, error) {
	return RunCommentMarkerExists(runDir, runStartCommentMarkerFile, "start comment")
}

func runCompletionCommentMarkerExists(runDir string) (bool, error) {
	return RunCommentMarkerExists(runDir, runCompletionCommentMarkerFile, "completion comment")
}

func runFailureCommentMarkerExists(runDir string) (bool, error) {
	return RunCommentMarkerExists(runDir, runFailureCommentMarkerFile, "failure comment")
}

func writeRunCommentMarker(run state.Run, repo string, issueNumber int, markerFile, markerKind string) error {
	marker := RunCommentMarker{
		RunID:       run.ID,
		Repo:        strings.TrimSpace(repo),
		IssueNumber: issueNumber,
		PostedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s marker: %w", markerKind, err)
	}
	path := RunCommentMarkerPath(run.RunDir, markerFile)
	if err := writeFileAtomically(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s marker: %w", markerKind, err)
	}
	return nil
}

func writeRunStartCommentMarker(run state.Run, repo string, issueNumber int) error {
	return writeRunCommentMarker(run, repo, issueNumber, runStartCommentMarkerFile, "start comment")
}

func writeRunCompletionCommentMarker(run state.Run, repo string, issueNumber int) error {
	return writeRunCommentMarker(run, repo, issueNumber, runCompletionCommentMarkerFile, "completion comment")
}

func writeRunFailureCommentMarker(run state.Run, repo string, issueNumber int) error {
	return writeRunCommentMarker(run, repo, issueNumber, runFailureCommentMarkerFile, "failure comment")
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tempFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tempPath := tempFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("remove temp file %s: %v", tempPath, err)
			}
		}
	}()
	if _, err := tempFile.Write(data); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return fmt.Errorf("write temp file: %w (close temp file: %v)", err, closeErr)
		}
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := tempFile.Chmod(mode); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return fmt.Errorf("chmod temp file: %w (close temp file: %v)", err, closeErr)
		}
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	removeTemp = false
	return nil
}

func requesterForRun(run state.Run, target RunResponseTarget, requesterUserID string) string {
	requestedBy := strings.TrimSpace(target.RequestedBy)
	if requestedBy != "" {
		return requestedBy
	}
	requesterUserID = strings.TrimSpace(requesterUserID)
	if requesterUserID == "" || requesterUserID == "system" {
		return ""
	}
	return requesterUserID
}

func (s *Server) PostRunStartCommentBestEffort(run state.Run, sessionMode runtime.SessionMode, sessionResume bool) {
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
	}
	if !ok {
		target = RunResponseTarget{}
	}
	if markerExists, err := runStartCommentMarkerExists(run.RunDir); err != nil {
		log.Printf("failed to check start comment marker for run %s: %v", run.ID, err)
		return
	} else if markerExists {
		return
	}

	repo, issueNumber := resolveRunCommentTarget(run, target)
	if repo == "" || issueNumber <= 0 {
		return
	}

	runCredentialInfo, _ := s.Store.GetRunCredentialInfo(run.ID)
	body := buildRunStartComment(run, target, requesterForRun(run, target, runCredentialInfo.CreatedByUserID), sessionMode, sessionResume)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post start comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := writeRunStartCommentMarker(run, repo, issueNumber); err != nil {
		log.Printf("failed to persist start comment marker for run %s: %v", run.ID, err)
	}
}

func (s *Server) PostRunCompletionCommentBestEffort(run state.Run) {
	if !isCommentTriggeredRun(run.Trigger) {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
		return
	}
	if !ok {
		return
	}
	if markerExists, err := runCompletionCommentMarkerExists(run.RunDir); err != nil {
		log.Printf("failed to check completion comment marker for run %s: %v", run.ID, err)
		return
	} else if markerExists {
		return
	}
	// TODO: This per-run JSON marker deduplicates within a shared run directory.
	// Revisit a SQLite-backed guard if we need cross-instance/global dedupe guarantees.

	repo := strings.TrimSpace(target.Repo)
	if repo == "" {
		repo = strings.TrimSpace(run.Repo)
	}
	issueNumber := target.IssueNumber
	if issueNumber <= 0 {
		issueNumber = run.PRNumber
	}
	if repo == "" || issueNumber <= 0 {
		return
	}

	var totalTokens *int64
	if usage, ok := s.Store.GetRunTokenUsage(run.ID); ok && usage.TotalTokens > 0 {
		totalTokens = &usage.TotalTokens
	}

	body, err := buildRunCompletionComment(run, target, repo, totalTokens)
	if err != nil {
		log.Printf("failed to build completion comment for %s: %v", run.ID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post completion comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := writeRunCompletionCommentMarker(run, repo, issueNumber); err != nil {
		log.Printf("failed to persist completion comment marker for run %s: %v", run.ID, err)
	}
}

func (s *Server) PostRunFailureCommentBestEffort(run state.Run) {
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
	}
	if !ok {
		target = RunResponseTarget{}
	}
	if markerExists, err := runFailureCommentMarkerExists(run.RunDir); err != nil {
		log.Printf("failed to check failure comment marker for run %s: %v", run.ID, err)
		return
	} else if markerExists {
		return
	}

	repo, issueNumber := resolveRunCommentTarget(run, target)
	if repo == "" || issueNumber <= 0 {
		return
	}

	body, err := buildRunFailureComment(run, target)
	if err != nil {
		log.Printf("failed to build failure comment for %s: %v", run.ID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post failure comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := writeRunFailureCommentMarker(run, repo, issueNumber); err != nil {
		log.Printf("failed to persist failure comment marker for run %s: %v", run.ID, err)
	}
}

func (s *Server) notifyRunTerminalGitHubBestEffort(run state.Run) {
	switch run.Status {
	case state.StatusSucceeded:
		s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionRocket)
		s.PostRunCompletionCommentBestEffort(run)
	case state.StatusReview:
		s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionHooray)
		s.PostRunCompletionCommentBestEffort(run)
	case state.StatusFailed:
		s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionConfused)
		s.PostRunFailureCommentBestEffort(run)
	case state.StatusCanceled:
		s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionMinusOne)
	}
}

func resolveRunCommentTarget(run state.Run, target RunResponseTarget) (string, int) {
	repo := strings.TrimSpace(target.Repo)
	if repo == "" {
		repo = strings.TrimSpace(run.Repo)
	}
	issueNumber := target.IssueNumber
	if issueNumber <= 0 {
		if isCommentTriggeredRun(run.Trigger) && run.PRNumber > 0 {
			issueNumber = run.PRNumber
		} else {
			issueNumber = run.IssueNumber
		}
	}
	return repo, issueNumber
}

func buildRunStartComment(run state.Run, target RunResponseTarget, requestedBy string, sessionMode runtime.SessionMode, sessionResume bool) string {
	var queueDelaySeconds *int64
	if run.StartedAt != nil {
		delay := int64(run.StartedAt.UTC().Sub(run.CreatedAt.UTC()).Seconds())
		if delay < 0 {
			delay = 0
		}
		queueDelaySeconds = &delay
	}

	return ghapi.RenderStartComment(runStartCommentBodyMarker, runsummary.StartCommentInput{
		RunID:             run.ID,
		RequestedBy:       requestedBy,
		Trigger:           runtrigger.Normalize(firstNonEmpty(target.Trigger.String(), run.Trigger.String())),
		AgentRuntime:      run.AgentRuntime,
		RunnerCommit:      loadRunBuildCommit(run.RunDir),
		BaseBranch:        run.BaseBranch,
		HeadBranch:        run.HeadBranch,
		SessionMode:       string(sessionMode),
		SessionResume:     sessionResume,
		Debug:             run.Debug,
		Instruction:       run.Instruction,
		Context:           run.Context,
		QueueDelaySeconds: queueDelaySeconds,
	})
}

func loadRunBuildCommit(runDir string) string {
	if strings.TrimSpace(runDir) == "" {
		return ""
	}
	metaPath := filepath.Join(runDir, "meta.json")
	deadline := time.Now().UTC().Add(250 * time.Millisecond)
	for {
		meta, err := runner.ReadMeta(metaPath)
		if err == nil {
			return strings.TrimSpace(meta.BuildCommit)
		}
		if !time.Now().UTC().Before(deadline) {
			return ""
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func buildRunCompletionComment(run state.Run, target RunResponseTarget, repo string, totalTokens *int64) (string, error) {
	agentOutput := "(no agent output captured)"
	agentPath, err := resolveRunAgentLogPath(run.RunDir)
	if err == nil {
		if data, readErr := os.ReadFile(agentPath); readErr == nil {
			if strings.TrimSpace(string(data)) != "" {
				agentOutput = string(data)
			}
		} else if !os.IsNotExist(readErr) {
			return "", fmt.Errorf("read agent log: %w", readErr)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve agent log: %w", err)
	}

	commitMessageData, err := os.ReadFile(filepath.Join(run.RunDir, "commit_message.txt"))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read commit message: %w", err)
	}
	body, err := ghapi.RenderCompletionComment(runCompletionCommentBodyMarker, runsummary.CompletionCommentInput{
		RunID:           run.ID,
		Repo:            repo,
		RequestedBy:     target.RequestedBy,
		HeadSHA:         run.HeadSHA,
		IssueNumber:     run.IssueNumber,
		GooseOutput:     agentOutput,
		CommitMessage:   commitMessageData,
		DurationSeconds: runsummary.RunDurationSeconds(run.CreatedAt, run.StartedAt, run.CompletedAt),
		TotalTokens:     totalTokens,
	})
	if err != nil {
		return "", fmt.Errorf("render completion comment: %w", err)
	}
	return body, nil
}

func loadRunTokenUsage(run state.Run) (state.RunTokenUsage, bool, error) {
	agentPath, err := resolveRunAgentLogPath(run.RunDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state.RunTokenUsage{}, false, nil
		}
		return state.RunTokenUsage{}, false, fmt.Errorf("resolve agent log: %w", err)
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return state.RunTokenUsage{}, false, fmt.Errorf("read agent log: %w", err)
	}
	usage, ok := runsummary.ExtractTokenUsage(string(data))
	if !ok {
		return state.RunTokenUsage{}, false, nil
	}

	return state.RunTokenUsage{
		RunID:                 run.ID,
		AgentRuntime:          run.AgentRuntime,
		Provider:              usage.Provider,
		Model:                 usage.Model,
		TotalTokens:           usage.TotalTokens,
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		ReasoningOutputTokens: usage.ReasoningOutputTokens,
		RawUsageJSON:          usage.RawUsageJSON,
		CapturedAt:            time.Now().UTC(),
	}, true, nil
}

func buildRunFailureComment(run state.Run, target RunResponseTarget) (string, error) {
	agentOutput := ""
	agentLogLabel := "Agent log"
	agentPath, err := resolveRunAgentLogPath(run.RunDir)
	if err == nil {
		if data, readErr := os.ReadFile(agentPath); readErr == nil {
			agentOutput = string(data)
			if filepath.Base(agentPath) == legacyAgentLogFile {
				agentLogLabel = "Goose log"
			}
		} else if !os.IsNotExist(readErr) {
			return "", fmt.Errorf("read agent log: %w", readErr)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve agent log: %w", err)
	}

	summary := summarizeRunFailure(run, agentOutput)
	header := summary.Headline
	if requestedBy := strings.TrimSpace(target.RequestedBy); requestedBy != "" {
		header = fmt.Sprintf("@%s %s", requestedBy, header)
	}

	return ghapi.RenderFailureComment(
		runFailureCommentBodyMarker,
		header,
		summary.RetryAt,
		summary.Reason,
		buildRunFailureDetails(run.Error, agentOutput, agentLogLabel),
	), nil
}

func summarizeRunFailure(run state.Run, agentOutput string) RunFailureSummary {
	corpusParts := make([]string, 0, 2)
	if reason := strings.TrimSpace(run.Error); reason != "" {
		corpusParts = append(corpusParts, reason)
	}
	if output := strings.TrimSpace(agentOutput); output != "" {
		corpusParts = append(corpusParts, output)
	}
	corpus := strings.Join(corpusParts, "\n")
	if usageLimitPattern.MatchString(corpus) {
		summary := RunFailureSummary{
			Headline: fmt.Sprintf("Rascal run `%s` failed because Goose hit the Codex usage limit.", run.ID),
		}
		if matches := retryAtPattern.FindStringSubmatch(corpus); len(matches) == 2 {
			summary.RetryAt = strings.TrimSpace(matches[1])
		}
		return summary
	}

	reason := compactFailureReason(run.Error)
	if reason == "" {
		reason = "The runner exited without a more specific error message."
	}
	return RunFailureSummary{
		Headline: fmt.Sprintf("Rascal run `%s` failed.", run.ID),
		Reason:   reason,
	}
}

func compactFailureReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	reason = strings.Join(strings.Fields(reason), " ")
	const maxReasonLen = 280
	if len(reason) <= maxReasonLen {
		return reason
	}
	return strings.TrimSpace(reason[:maxReasonLen-3]) + "..."
}

func buildRunFailureDetails(runError, agentOutput, agentLogLabel string) string {
	parts := make([]string, 0, 2)
	if reason := strings.TrimSpace(runError); reason != "" {
		parts = append(parts, "Run error:\n"+reason)
	}
	if output := strings.TrimSpace(agentOutput); output != "" {
		parts = append(parts, agentLogLabel+":\n"+output)
	}
	return strings.Join(parts, "\n\n")
}

func detectUsageLimitPause(run state.Run, errText string) (time.Time, string, bool) {
	corpusParts := make([]string, 0, 2)
	if reason := strings.TrimSpace(errText); reason != "" {
		corpusParts = append(corpusParts, reason)
	}
	if output, loadErr := loadRunAgentOutput(run.RunDir); loadErr == nil && strings.TrimSpace(output) != "" {
		corpusParts = append(corpusParts, output)
	} else if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		log.Printf("run %s read agent output for usage-limit detection failed: %v", run.ID, loadErr)
	}

	corpus := strings.Join(corpusParts, "\n")
	if !usageLimitPattern.MatchString(corpus) {
		return time.Time{}, "", false
	}

	retryAt, reason := ParseUsageLimitRetryAt(corpus, time.Now().UTC())
	if retryAt.IsZero() {
		retryAt = time.Now().UTC().Add(defaultUsageLimitPause)
		if reason == "" {
			reason = fmt.Sprintf("usage limit without retry timestamp; applying default pause of %s", defaultUsageLimitPause)
		}
	}
	return retryAt, reason, true
}

func loadRunAgentOutput(runDir string) (string, error) {
	runDir = strings.TrimSpace(runDir)
	outputPath := filepath.Join(runDir, agentOutputFile)
	if data, err := os.ReadFile(outputPath); err == nil {
		return string(data), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read agent output %s: %w", outputPath, err)
	}

	legacyPath := filepath.Join(runDir, legacyAgentLogFile)
	if data, err := os.ReadFile(legacyPath); err == nil {
		return string(data), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read legacy agent log %s: %w", legacyPath, err)
	}

	return "", os.ErrNotExist
}

func ParseUsageLimitRetryAt(corpus string, now time.Time) (time.Time, string) {
	matches := retryAtPattern.FindStringSubmatch(corpus)
	if len(matches) == 2 {
		raw := sanitizeRetryHint(matches[1])
		if raw != "" {
			if retryAt, ok := parseAbsoluteRetryTime(raw, now); ok {
				return retryAt, fmt.Sprintf("provider requested retry at %s", raw)
			}
		}
	}

	matches = retryInPattern.FindStringSubmatch(corpus)
	if len(matches) == 2 {
		raw := sanitizeRetryHint(matches[1])
		if raw != "" {
			if delay, ok := parseRetryDelay(raw); ok {
				if now.IsZero() {
					now = time.Now().UTC()
				}
				return now.Add(delay), fmt.Sprintf("provider requested retry in %s", raw)
			}
		}
	}

	return time.Time{}, ""
}

func sanitizeRetryHint(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, " .,:;)")
	raw = strings.TrimPrefix(raw, "(")
	raw = strings.Join(strings.Fields(raw), " ")
	return ordinalDayPattern.ReplaceAllString(raw, "$1")
}

func parseAbsoluteRetryTime(raw string, now time.Time) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}

	if retryAt, err := time.Parse(time.RFC3339, raw); err == nil {
		return normalizeFutureRetryTime(retryAt, raw, now)
	}
	if retryAt, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return normalizeFutureRetryTime(retryAt, raw, now)
	}

	layouts := []string{
		"Jan 2, 2006 3:04 PM",
		"January 2, 2006 3:04 PM",
		"Jan 2 2006 3:04 PM",
		"January 2 2006 3:04 PM",
		"Jan 2, 2006 15:04",
		"January 2, 2006 15:04",
		"Jan 2 2006 15:04",
		"January 2 2006 15:04",
	}
	for _, loc := range []*time.Location{time.Local, time.UTC} {
		for _, layout := range layouts {
			retryAt, err := time.ParseInLocation(layout, raw, loc)
			if err != nil {
				continue
			}
			return normalizeFutureRetryTime(retryAt, raw, now)
		}
	}

	zonedLayouts := []string{
		"Jan 2, 2006 3:04 PM MST",
		"January 2, 2006 3:04 PM MST",
		"Jan 2 2006 3:04 PM MST",
		"January 2 2006 3:04 PM MST",
		"Jan 2, 2006 15:04 MST",
		"January 2, 2006 15:04 MST",
		"Jan 2 2006 15:04 MST",
		"January 2 2006 15:04 MST",
		"Jan 2, 2006 3:04 PM -0700",
		"January 2, 2006 3:04 PM -0700",
		"Jan 2 2006 3:04 PM -0700",
		"January 2 2006 3:04 PM -0700",
		"Jan 2, 2006 15:04 -0700",
		"January 2, 2006 15:04 -0700",
		"Jan 2 2006 15:04 -0700",
		"January 2 2006 15:04 -0700",
	}
	for _, layout := range zonedLayouts {
		retryAt, err := time.Parse(layout, raw)
		if err != nil {
			continue
		}
		return normalizeFutureRetryTime(retryAt, raw, now)
	}

	return time.Time{}, false
}

func normalizeFutureRetryTime(retryAt time.Time, raw string, now time.Time) (time.Time, bool) {
	if retryAt.IsZero() {
		return time.Time{}, false
	}
	retryAt = retryAt.UTC()
	if !now.IsZero() && !retryAt.After(now) {
		return now.Add(minimumUsageLimitPause), true
	}
	return retryAt, true
}

func parseRetryDelay(raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(strings.ReplaceAll(strings.ToLower(raw), " ", "")); err == nil && d > 0 {
		return d, true
	}

	matches := durationComponentPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return 0, false
	}

	var total time.Duration
	for _, match := range matches {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			return 0, false
		}
		switch unit := strings.ToLower(match[2]); {
		case strings.HasPrefix(unit, "d"):
			total += time.Duration(value) * 24 * time.Hour
		case strings.HasPrefix(unit, "h"):
			total += time.Duration(value) * time.Hour
		case strings.HasPrefix(unit, "m"):
			total += time.Duration(value) * time.Minute
		case strings.HasPrefix(unit, "s"):
			total += time.Duration(value) * time.Second
		default:
			return 0, false
		}
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

func (s *Server) activeSchedulerPause() (time.Time, string, bool) {
	pauseUntil, reason, ok, err := s.Store.ActiveSchedulerPause(schedulerPauseScope, time.Now().UTC())
	if err != nil {
		log.Printf("load active worker pause failed: %v", err)
		return time.Time{}, "", false
	}
	return pauseUntil, reason, ok
}

func (s *Server) pauseWorkersUntil(until time.Time, reason string) time.Time {
	if until.IsZero() {
		until = time.Now().UTC().Add(defaultUsageLimitPause)
	}
	effective, err := s.Store.PauseScheduler(schedulerPauseScope, reason, until)
	if err != nil {
		log.Printf("persist worker pause until %s failed: %v", until.Format(time.RFC3339), err)
		effective = until.UTC()
	}
	s.ensureResumeTimer(effective)
	return effective
}

func (s *Server) ensureResumeTimer(until time.Time) {
	if until.IsZero() {
		return
	}
	until = until.UTC()
	delay := time.Until(until)
	if delay < 0 {
		delay = 0
	}

	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return
	}
	if !s.resumeAt.IsZero() && s.resumeAt.Equal(until) {
		s.mu.Unlock()
		return
	}
	if s.resumeTimer != nil {
		s.resumeTimer.Stop()
	}
	s.resumeAt = until
	s.resumeTimer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if !s.resumeAt.Equal(until) {
			s.mu.Unlock()
			return
		}
		s.resumeAt = time.Time{}
		s.resumeTimer = nil
		draining := s.draining
		s.mu.Unlock()
		if draining {
			return
		}
		s.ScheduleRuns("")
	})
	s.mu.Unlock()
}

func (s *Server) addIssueReactionBestEffort(repo string, issueNumber int, reaction string) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.AddIssueReaction(ctx, repo, issueNumber, reaction); err != nil {
		log.Printf("failed to add %q reaction for %s#%d: %v", reaction, repo, issueNumber, err)
	}
}

func (s *Server) removeIssueReactionsBestEffort(repo string, issueNumber int) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.RemoveIssueReactions(ctx, repo, issueNumber); err != nil {
		log.Printf("failed to remove reactions for %s#%d: %v", repo, issueNumber, err)
	}
}

func (s *Server) addIssueCommentReactionBestEffort(repo string, commentID int64, reaction string) {
	if commentID <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.AddIssueCommentReaction(ctx, repo, commentID, reaction); err != nil {
		log.Printf("failed to add %q reaction for issue comment %d in %s: %v", reaction, commentID, repo, err)
	}
}

func (s *Server) addPullRequestReviewReactionBestEffort(repo string, pullNumber int, reviewID int64, reaction string) {
	if reviewID <= 0 || pullNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.AddPullRequestReviewReaction(ctx, repo, pullNumber, reviewID, reaction); err != nil {
		log.Printf("failed to add %q reaction for PR review %d on %s#%d: %v", reaction, reviewID, repo, pullNumber, err)
	}
}

func (s *Server) addPullRequestReviewCommentReactionBestEffort(repo string, commentID int64, reaction string) {
	if commentID <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.AddPullRequestReviewCommentReaction(ctx, repo, commentID, reaction); err != nil {
		log.Printf("failed to add %q reaction for PR review comment %d in %s: %v", reaction, commentID, repo, err)
	}
}
