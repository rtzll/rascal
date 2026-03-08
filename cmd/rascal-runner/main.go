package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runsummary"
)

const (
	defaultMetaDir                 = "/rascal-meta"
	defaultWorkRoot                = "/work"
	defaultRepoDirName             = "repo"
	defaultAgentLogFile            = "agent.ndjson"
	defaultMetaFile                = "meta.json"
	defaultInstructionsFile        = "instructions.md"
	defaultCommitMsgFile           = "commit_message.txt"
	defaultAgentOutputFile         = "agent_output.txt"
	defaultPRBodyFile              = "pr_body.md"
	defaultPRLabel                 = "rascal"
	defaultCodexAuthFile           = "auth.json"
	defaultCodexSessionDir         = "sessions"
	defaultDeterministicChecksFile = "deterministic-checks.json"
	defaultReviewLoopSummaryFile   = "review-loop.json"
	defaultReviewFindingsFile      = "review-findings.json"
	defaultReviewSummaryFile       = "review-summary.md"
)

var (
	convCommitPattern = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9._/-]+\))?(!)?:[[:space:]].+`)
	prURLPattern      = regexp.MustCompile(`https://github\.com/[^[:space:]]+/pull/[0-9]+`)

	buildVersion = "dev"
	buildCommit  = "unknown"
	buildTime    = "unknown"
)

type config struct {
	RunID       string
	TaskID      string
	Task        string
	Repo        string
	BaseBranch  string
	HeadBranch  string
	IssueNumber int
	PRNumber    int
	Trigger     string
	Context     string
	GitHubToken string

	MetaDir          string
	WorkRoot         string
	RepoDir          string
	GooseLogPath     string
	MetaPath         string
	InstructionsPath string
	CommitMsgPath    string
	AgentOutputPath  string
	PRBodyPath       string

	GooseDebug   bool
	AgentBackend agent.Backend

	GoosePathRoot      string
	CodexHome          string
	AgentSessionMode   agent.SessionMode
	AgentSessionResume bool
	AgentSessionKey    string
	BackendSessionID   string

	GooseSessionMode   string
	GooseSessionResume bool
	GooseSessionKey    string
	GooseSessionName   string

	ReviewLoop                 reviewLoopConfig
	DeterministicCheckCommands []string

	DeterministicChecksPath string
	ReviewLoopSummaryPath   string
	ReviewFindingsPath      string
	ReviewSummaryPath       string
}

type prView struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

type gooseSessionInfo struct {
	Name string `json:"name"`
}

type deterministicCheckResult struct {
	Name         string `json:"name"`
	Command      string `json:"command"`
	Required     bool   `json:"required"`
	Passed       bool   `json:"passed"`
	Output       string `json:"output,omitempty"`
	Error        string `json:"error,omitempty"`
	TerminalFail bool   `json:"terminal_fail,omitempty"`
}

type deterministicCheckRun struct {
	Stage  string                     `json:"stage"`
	RanAt  string                     `json:"ran_at"`
	Passed bool                       `json:"passed"`
	Checks []deterministicCheckResult `json:"checks"`
}

type deterministicChecksReport struct {
	Runs []deterministicCheckRun `json:"runs"`
}

func (r deterministicChecksReport) latest() deterministicCheckRun {
	if len(r.Runs) == 0 {
		return deterministicCheckRun{}
	}
	return r.Runs[len(r.Runs)-1]
}

type commandExecutor interface {
	LookPath(name string) error
	CombinedOutput(dir string, extraEnv []string, name string, args ...string) (string, error)
	Run(dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error
	RunWithInput(dir string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

type osExecutor struct{}

func (osExecutor) LookPath(name string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("look up %s: %w", name, err)
	}
	return nil
}

func (osExecutor) CombinedOutput(dir string, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
		}
		return text, fmt.Errorf("%s %s failed: %w (%s)", name, strings.Join(args, " "), err, text)
	}
	return text, nil
}

func (osExecutor) Run(dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error {
	return osExecutor{}.RunWithInput(dir, extraEnv, nil, stdout, stderr, name, args...)
}

func (osExecutor) RunWithInput(dir string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func main() {
	log.SetFlags(0)
	log.Printf("[%s] starting rascal-runner %s", nowUTC(), buildInfoSummary())
	if err := run(); err != nil {
		log.Printf("[%s] run failed: %v", nowUTC(), err)
		os.Exit(1)
	}
}

func run() error {
	return runWithExecutor(osExecutor{})
}

func runWithExecutor(ex commandExecutor) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	started := time.Now().UTC()

	meta := runner.Meta{
		RunID:      cfg.RunID,
		TaskID:     cfg.TaskID,
		Repo:       cfg.Repo,
		BaseBranch: cfg.BaseBranch,
		HeadBranch: cfg.HeadBranch,
		ExitCode:   1,
	}
	defer func() {
		if err := runner.WriteMeta(cfg.MetaPath, meta); err != nil {
			log.Printf("[%s] failed to write meta: %v", nowUTC(), err)
		}
	}()

	fail := func(err error) error {
		meta.Error = err.Error()
		return err
	}

	if err := runStage("prepare_workspace", func() error {
		if err := os.MkdirAll(filepath.Join(cfg.MetaDir, "goose"), 0o755); err != nil {
			return fmt.Errorf("create goose dir: %w", err)
		}
		if err := os.MkdirAll(cfg.GoosePathRoot, 0o755); err != nil {
			return fmt.Errorf("create goose path root: %w", err)
		}
		if err := os.MkdirAll(filepath.Join(cfg.MetaDir, "codex"), 0o755); err != nil {
			return fmt.Errorf("create codex dir: %w", err)
		}
		if err := os.MkdirAll(cfg.CodexHome, 0o755); err != nil {
			return fmt.Errorf("create codex home: %w", err)
		}
		if err := os.MkdirAll(cfg.WorkRoot, 0o755); err != nil {
			return fmt.Errorf("create work dir: %w", err)
		}
		return nil
	}); err != nil {
		return fail(err)
	}

	if err := runStage("prepare_instructions", func() error {
		return ensureInstructions(cfg)
	}); err != nil {
		return fail(err)
	}
	log.Printf("[%s] run started run_id=%s repo=%s", nowUTC(), cfg.RunID, cfg.Repo)

	if err := runStage("validate_commands", func() error {
		return validateCommands(ex, cfg)
	}); err != nil {
		return fail(err)
	}

	var authorName string
	var authorEmail string
	if err := runStage("resolve_identity", func() error {
		var err error
		authorName, authorEmail, err = resolveGitIdentity(ex)
		if err != nil {
			return fmt.Errorf("resolve git identity: %w", err)
		}
		return nil
	}); err != nil {
		return fail(err)
	}
	log.Printf("[%s] using commit identity: %s <%s>", nowUTC(), authorName, authorEmail)

	if err := runStage("checkout_repo", func() error {
		return checkoutRepo(ex, cfg)
	}); err != nil {
		return fail(err)
	}

	var agentOutput string
	latestAuthorLogPath := cfg.GooseLogPath
	latestAuthorOutputPath := cfg.AgentOutputPath
	if err := runStage("run_agent", func() error {
		var err error
		var agentSessionID string
		agentOutput, agentSessionID, err = runAgent(ex, cfg)
		if strings.TrimSpace(agentSessionID) != "" {
			meta.AgentSessionID = strings.TrimSpace(agentSessionID)
		}
		return err
	}); err != nil {
		return fail(err)
	}

	checksReport := deterministicChecksReport{}
	if err := runStage("deterministic_checks", func() error {
		result, err := runDeterministicChecks(ex, cfg, "post_author")
		if err != nil {
			checksReport.Runs = append(checksReport.Runs, result)
			if writeErr := writeJSONFile(cfg.DeterministicChecksPath, checksReport); writeErr != nil {
				return fmt.Errorf("%w (artifact write failed: %v)", err, writeErr)
			}
			return err
		}
		checksReport.Runs = append(checksReport.Runs, result)
		return writeJSONFile(cfg.DeterministicChecksPath, checksReport)
	}); err != nil {
		return fail(err)
	}

	reviewResult := reviewLoopResult{Config: cfg.ReviewLoop.normalized()}
	if cfg.ReviewLoop.Enabled {
		if err := runStage("review_loop", func() error {
			result, finalAgentOutput, finalLogPath, finalOutputPath, err := runReviewLoop(ex, cfg, checksReport)
			if err != nil {
				return err
			}
			reviewResult = result
			if strings.TrimSpace(finalAgentOutput) != "" {
				agentOutput = finalAgentOutput
			}
			if strings.TrimSpace(finalLogPath) != "" {
				latestAuthorLogPath = finalLogPath
			}
			if strings.TrimSpace(finalOutputPath) != "" {
				latestAuthorOutputPath = finalOutputPath
			}
			return nil
		}); err != nil {
			return fail(err)
		}
	} else {
		reviewResult = reviewLoopResult{
			Config: cfg.ReviewLoop.normalized(),
			Ran:    false,
		}
		if err := writeReviewArtifacts(cfg, reviewResult); err != nil {
			return fail(fmt.Errorf("write skipped review artifacts: %w", err))
		}
	}

	if err := promoteAuthorOutputs(latestAuthorLogPath, latestAuthorOutputPath, cfg); err != nil {
		return fail(fmt.Errorf("promote final author output: %w", err))
	}
	meta.ReviewLoopRan = reviewResult.Ran
	meta.ReviewReviewerPasses = reviewResult.ReviewerPasses
	meta.ReviewAuthorFixPasses = reviewResult.AuthorFixPasses
	meta.ReviewFixesApplied = reviewResult.FixesApplied
	meta.ReviewFindingCount = reviewResult.FinalFindingCount
	meta.ReviewUnresolvedFindings = reviewResult.UnresolvedFindings

	commitTitle := fmt.Sprintf("chore(rascal): %s", taskSubject(cfg.Task, cfg.TaskID))
	commitBody := ""
	if err := runStage("prepare_commit", func() error {
		if title, body, msgErr := loadAgentCommitMessage(cfg.CommitMsgPath); msgErr != nil {
			return fmt.Errorf("load agent commit message: %w", msgErr)
		} else {
			commitBody = body
			if title != "" {
				if isConventionalTitle(title) {
					commitTitle = title
				} else {
					log.Printf("[%s] agent commit title is not conventional; using fallback title", nowUTC())
				}
			}
		}

		statusOut, err := runCommand(ex, cfg.RepoDir, nil, "git", "status", "--porcelain")
		if err != nil {
			return fmt.Errorf("git status --porcelain: %w", err)
		}
		if strings.TrimSpace(statusOut) != "" {
			if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "add", "-A"); err != nil {
				return fmt.Errorf("git add -A: %w", err)
			}
			finalBody := strings.TrimSpace(commitBody)
			if finalBody != "" {
				finalBody += "\n\n"
			}
			finalBody += "Run: " + cfg.RunID

			commitEnv := []string{
				"GIT_AUTHOR_NAME=" + authorName,
				"GIT_AUTHOR_EMAIL=" + authorEmail,
				"GIT_COMMITTER_NAME=" + authorName,
				"GIT_COMMITTER_EMAIL=" + authorEmail,
			}
			if _, err := runCommand(ex, cfg.RepoDir, commitEnv, "git", "commit", "-m", commitTitle, "-m", finalBody); err != nil {
				return fmt.Errorf("git commit: %w", err)
			}
		}
		return nil
	}); err != nil {
		return fail(err)
	}

	if err := runStage("push_branch", func() error {
		log.Printf("[%s] pushing branch", nowUTC())
		if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "push", "-u", "origin", cfg.HeadBranch); err != nil {
			return fmt.Errorf("git push failed: %w", err)
		}
		return nil
	}); err != nil {
		return fail(err)
	}

	var view prView
	var found bool
	if err := runStage("load_pr", func() error {
		var err error
		view, found, err = loadPRView(ex, cfg)
		if err != nil {
			return fmt.Errorf("load pull request view: %w", err)
		}
		return nil
	}); err != nil {
		return fail(err)
	}

	if !found {
		if err := runStage("pr_create", func() error {
			log.Printf("[%s] creating pull request", nowUTC())
			closesSection := ""
			if cfg.IssueNumber > 0 {
				closesSection = fmt.Sprintf("\n\nCloses #%d", cfg.IssueNumber)
			}
			runDuration := runsummary.FormatDuration(int64(time.Since(started).Seconds()))
			body := runsummary.BuildPRBody(cfg.RunID, commitBody, agentOutput, runDuration, closesSection)
			if err := os.WriteFile(cfg.PRBodyPath, []byte(body), 0o644); err != nil {
				return fmt.Errorf("write pr body: %w", err)
			}

			out, err := runCommand(ex, cfg.RepoDir, nil, "gh", "pr", "create",
				"--repo", cfg.Repo,
				"--base", cfg.BaseBranch,
				"--head", cfg.HeadBranch,
				"--label", defaultPRLabel,
				"--title", commitTitle,
				"--body-file", cfg.PRBodyPath,
			)
			if err != nil {
				return fmt.Errorf("gh pr create failed: %w", err)
			}

			if latest, ok, err := loadPRView(ex, cfg); err == nil && ok {
				view = latest
				found = true
			} else {
				if m := prURLPattern.FindString(out); m != "" {
					view.URL = m
					if i := strings.LastIndex(m, "/"); i >= 0 && i+1 < len(m) {
						if n, convErr := strconv.Atoi(m[i+1:]); convErr == nil {
							view.Number = n
						}
					}
				}
			}
			return nil
		}); err != nil {
			return fail(err)
		}
	}

	if err := runStage("finalize_meta", func() error {
		headSHA, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("git rev-parse HEAD: %w", err)
		}
		meta.HeadSHA = strings.TrimSpace(headSHA)
		meta.PRNumber = view.Number
		meta.PRURL = strings.TrimSpace(view.URL)
		return nil
	}); err != nil {
		return fail(err)
	}

	meta.ExitCode = 0
	meta.Error = ""
	log.Printf("[%s] run completed exit_code=0", nowUTC())
	return nil
}

func loadConfig() (config, error) {
	runID, err := requiredEnv("RASCAL_RUN_ID")
	if err != nil {
		return config{}, err
	}
	taskID, err := requiredEnv("RASCAL_TASK_ID")
	if err != nil {
		return config{}, err
	}
	repo, err := requiredEnv("RASCAL_REPO")
	if err != nil {
		return config{}, err
	}
	ghToken, err := requiredEnv("GH_TOKEN")
	if err != nil {
		return config{}, err
	}

	baseBranch := strings.TrimSpace(os.Getenv("RASCAL_BASE_BRANCH"))
	if baseBranch == "" {
		baseBranch = "main"
	}
	headBranch := strings.TrimSpace(os.Getenv("RASCAL_HEAD_BRANCH"))
	if headBranch == "" {
		headBranch = "rascal/" + runID
	}
	issueNumber := 0
	if raw := strings.TrimSpace(os.Getenv("RASCAL_ISSUE_NUMBER")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid RASCAL_ISSUE_NUMBER: %w", err)
		}
		issueNumber = n
	}
	prNumber := 0
	if raw := strings.TrimSpace(os.Getenv("RASCAL_PR_NUMBER")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid RASCAL_PR_NUMBER: %w", err)
		}
		prNumber = n
	}

	trigger := strings.TrimSpace(os.Getenv("RASCAL_TRIGGER"))
	if trigger == "" {
		trigger = "cli"
	}

	metaDir := firstNonEmptyValue(strings.TrimSpace(os.Getenv("RASCAL_META_DIR")), defaultMetaDir)
	workRoot := firstNonEmptyValue(strings.TrimSpace(os.Getenv("RASCAL_WORK_ROOT")), defaultWorkRoot)
	repoDir := strings.TrimSpace(os.Getenv("RASCAL_REPO_DIR"))
	if repoDir == "" {
		repoDir = filepath.Join(workRoot, defaultRepoDirName)
	}

	debug := true
	if raw := strings.TrimSpace(os.Getenv("RASCAL_GOOSE_DEBUG")); raw != "" {
		switch strings.ToLower(raw) {
		case "0", "false", "no", "off":
			debug = false
		default:
			debug = true
		}
	}

	agentBackend := agent.NormalizeBackend(os.Getenv("RASCAL_AGENT_BACKEND"))
	agentSessionMode := agent.NormalizeSessionMode(firstSetEnvValue("RASCAL_GOOSE_SESSION_MODE", "RASCAL_AGENT_SESSION_MODE"))
	agentSessionResume := parseBoolEnv(firstSetEnvValue("RASCAL_GOOSE_SESSION_RESUME", "RASCAL_AGENT_SESSION_RESUME"), false)
	if agentSessionMode == agent.SessionModeOff {
		agentSessionResume = false
	}
	agentSessionKey := firstSetEnvValue("RASCAL_GOOSE_SESSION_KEY", "RASCAL_AGENT_SESSION_KEY")
	backendSessionID := firstSetEnvValue("RASCAL_GOOSE_SESSION_NAME", "RASCAL_AGENT_SESSION_ID")
	if agentSessionResume {
		if agentSessionKey == "" {
			agentSessionKey = runner.GooseSessionTaskKey(repo, taskID)
		}
		if backendSessionID == "" && agentBackend == agent.BackendGoose {
			backendSessionID = runner.GooseSessionName(repo, taskID)
		}
	}
	goosePathRoot := firstNonEmptyValue(strings.TrimSpace(os.Getenv("GOOSE_PATH_ROOT")), filepath.Join(metaDir, "goose"))
	codexHome := firstNonEmptyValue(strings.TrimSpace(os.Getenv("CODEX_HOME")), filepath.Join(metaDir, "codex"))
	reviewEnabled := parseBoolEnv(os.Getenv("RASCAL_REVIEW_LOOP_ENABLED"), false)
	reviewMaxInitialPasses := parseNonNegativeIntEnv(os.Getenv("RASCAL_REVIEW_MAX_INITIAL_PASSES"), 1)
	reviewMaxFixPasses := parseNonNegativeIntEnv(os.Getenv("RASCAL_REVIEW_MAX_FIX_PASSES"), 1)
	reviewMaxVerificationPasses := parseNonNegativeIntEnv(os.Getenv("RASCAL_REVIEW_MAX_VERIFICATION_PASSES"), 1)
	checkCommands := parseCommandListEnv(os.Getenv("RASCAL_DETERMINISTIC_CHECK_COMMANDS"))

	return config{
		RunID:              runID,
		TaskID:             taskID,
		Task:               strings.TrimSpace(os.Getenv("RASCAL_TASK")),
		Repo:               repo,
		BaseBranch:         baseBranch,
		HeadBranch:         headBranch,
		IssueNumber:        issueNumber,
		PRNumber:           prNumber,
		Trigger:            trigger,
		Context:            strings.TrimSpace(os.Getenv("RASCAL_CONTEXT")),
		GitHubToken:        ghToken,
		MetaDir:            metaDir,
		WorkRoot:           workRoot,
		RepoDir:            repoDir,
		GooseLogPath:       filepath.Join(metaDir, defaultAgentLogFile),
		MetaPath:           filepath.Join(metaDir, defaultMetaFile),
		InstructionsPath:   filepath.Join(metaDir, defaultInstructionsFile),
		CommitMsgPath:      filepath.Join(metaDir, defaultCommitMsgFile),
		AgentOutputPath:    filepath.Join(metaDir, defaultAgentOutputFile),
		PRBodyPath:         filepath.Join(metaDir, defaultPRBodyFile),
		GooseDebug:         debug,
		AgentBackend:       agentBackend,
		GoosePathRoot:      goosePathRoot,
		CodexHome:          codexHome,
		AgentSessionMode:   agentSessionMode,
		AgentSessionResume: agentSessionResume,
		AgentSessionKey:    agentSessionKey,
		BackendSessionID:   backendSessionID,
		GooseSessionMode:   string(agentSessionMode),
		GooseSessionResume: agentSessionResume,
		GooseSessionKey:    agentSessionKey,
		GooseSessionName:   backendSessionID,
		ReviewLoop: reviewLoopConfig{
			Enabled:                     reviewEnabled,
			MaxInitialReviewerPasses:    reviewMaxInitialPasses,
			MaxAuthorFixPasses:          reviewMaxFixPasses,
			MaxVerificationReviewerPass: reviewMaxVerificationPasses,
		},
		DeterministicCheckCommands: checkCommands,
		DeterministicChecksPath:    filepath.Join(metaDir, defaultDeterministicChecksFile),
		ReviewLoopSummaryPath:      filepath.Join(metaDir, defaultReviewLoopSummaryFile),
		ReviewFindingsPath:         filepath.Join(metaDir, defaultReviewFindingsFile),
		ReviewSummaryPath:          filepath.Join(metaDir, defaultReviewSummaryFile),
	}, nil
}

func firstNonEmptyValue(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstSetEnvValue(keys ...string) string {
	for _, key := range keys {
		if raw, ok := os.LookupEnv(key); ok {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

func parseBoolEnv(raw string, fallback bool) bool {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func parseNonNegativeIntEnv(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func parseCommandListEnv(raw string) []string {
	raw = strings.ReplaceAll(raw, ";;", "\n")
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func configuredAgentBackend(cfg config) agent.Backend {
	return agent.NormalizeBackend(string(cfg.AgentBackend))
}

func configuredSessionMode(cfg config) agent.SessionMode {
	if cfg.AgentSessionMode != "" {
		return agent.NormalizeSessionMode(string(cfg.AgentSessionMode))
	}
	return agent.NormalizeSessionMode(cfg.GooseSessionMode)
}

func configuredSessionResume(cfg config) bool {
	if cfg.AgentSessionResume {
		return true
	}
	return cfg.GooseSessionResume
}

func configuredSessionKey(cfg config) string {
	if strings.TrimSpace(cfg.AgentSessionKey) != "" {
		return strings.TrimSpace(cfg.AgentSessionKey)
	}
	return strings.TrimSpace(cfg.GooseSessionKey)
}

func configuredBackendSessionID(cfg config) string {
	if strings.TrimSpace(cfg.BackendSessionID) != "" {
		return strings.TrimSpace(cfg.BackendSessionID)
	}
	return strings.TrimSpace(cfg.GooseSessionName)
}

func ensureInstructions(cfg config) error {
	if _, err := os.Stat(cfg.InstructionsPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat instructions: %w", err)
	}
	body := fmt.Sprintf(`# Rascal Instructions

Task ID: %s
Trigger: %s

Follow the repository instructions and implement the requested task.
Keep changes minimal, run tests, and summarize what changed.
`, cfg.TaskID, cfg.Trigger)
	if err := os.WriteFile(cfg.InstructionsPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write default instructions: %w", err)
	}
	return nil
}

func requiredEnv(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func requireCommands(ex commandExecutor, names ...string) error {
	for _, name := range names {
		if err := ex.LookPath(name); err != nil {
			return fmt.Errorf("required command missing: %s", name)
		}
	}
	return nil
}

func validateCommands(ex commandExecutor, cfg config) error {
	names := []string{"git", "gh"}
	switch configuredAgentBackend(cfg) {
	case agent.BackendCodex:
		names = append(names, "codex")
	default:
		names = append(names, "goose")
	}
	return requireCommands(ex, names...)
}

func resolveGitIdentity(ex commandExecutor) (string, string, error) {
	out, err := runCommand(ex, "", nil, "gh", "api", "user")
	if err != nil {
		return "", "", fmt.Errorf("query GitHub user: %w", err)
	}
	var payload struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return "", "", fmt.Errorf("decode GitHub user response: %w", err)
	}
	login := strings.TrimSpace(payload.Login)
	if login == "" {
		return "", "", fmt.Errorf("failed to parse GitHub login from token owner")
	}
	return login, login + "@users.noreply.github.com", nil
}

func checkoutRepo(ex commandExecutor, cfg config) error {
	repoURL := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", cfg.GitHubToken, cfg.Repo)
	if _, err := os.Stat(filepath.Join(cfg.RepoDir, ".git")); err == nil {
		log.Printf("[%s] repo already present, refreshing", nowUTC())
		if _, err := runCommand(ex, "", nil, "git", "-C", cfg.RepoDir, "fetch", "--all", "--prune"); err != nil {
			return fmt.Errorf("refresh existing checkout: %w", err)
		}
	} else {
		log.Printf("[%s] cloning %s", nowUTC(), cfg.Repo)
		if _, err := runCommand(ex, "", nil, "git", "clone", repoURL, cfg.RepoDir); err != nil {
			return fmt.Errorf("clone repository: %w", err)
		}
	}

	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "fetch", "origin", cfg.BaseBranch, cfg.HeadBranch); err != nil {
		log.Printf("[%s] git fetch warning: %v", nowUTC(), err)
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", cfg.BaseBranch); err != nil {
		if _, createErr := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.BaseBranch, "origin/"+cfg.BaseBranch); createErr != nil {
			return fmt.Errorf("checkout base branch %s: %w", cfg.BaseBranch, createErr)
		}
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "pull", "--ff-only", "origin", cfg.BaseBranch); err != nil {
		log.Printf("[%s] git pull warning: %v", nowUTC(), err)
	}

	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "--verify", cfg.HeadBranch); err == nil {
		_, err = runCommand(ex, cfg.RepoDir, nil, "git", "checkout", cfg.HeadBranch)
		if err != nil {
			return fmt.Errorf("checkout head branch %s: %w", cfg.HeadBranch, err)
		}
		return nil
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "ls-remote", "--exit-code", "--heads", "origin", cfg.HeadBranch); err == nil {
		_, err = runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch, "origin/"+cfg.HeadBranch)
		if err != nil {
			return fmt.Errorf("checkout remote head branch %s: %w", cfg.HeadBranch, err)
		}
		return nil
	}
	_, err := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch)
	if err != nil {
		return fmt.Errorf("create head branch %s: %w", cfg.HeadBranch, err)
	}
	return nil
}

type deterministicCheckSpec struct {
	Name     string
	Command  string
	Required bool
}

func buildDeterministicCheckSpecs(cfg config) []deterministicCheckSpec {
	specs := make([]deterministicCheckSpec, 0, len(cfg.DeterministicCheckCommands)+1)
	if _, err := os.Stat(filepath.Join(cfg.RepoDir, "Makefile")); err == nil {
		specs = append(specs, deterministicCheckSpec{
			Name:     "make_test_preview",
			Command:  "make -n test",
			Required: false,
		})
	}
	for i, cmd := range cfg.DeterministicCheckCommands {
		specs = append(specs, deterministicCheckSpec{
			Name:     fmt.Sprintf("custom_check_%d", i+1),
			Command:  strings.TrimSpace(cmd),
			Required: true,
		})
	}
	return specs
}

func runDeterministicChecks(ex commandExecutor, cfg config, stage string) (deterministicCheckRun, error) {
	specs := buildDeterministicCheckSpecs(cfg)
	run := deterministicCheckRun{
		Stage:  strings.TrimSpace(stage),
		RanAt:  nowUTC(),
		Passed: true,
		Checks: make([]deterministicCheckResult, 0, len(specs)),
	}
	if len(specs) == 0 {
		return run, nil
	}

	var firstTerminalErr error
	for _, spec := range specs {
		res := deterministicCheckResult{
			Name:     strings.TrimSpace(spec.Name),
			Command:  strings.TrimSpace(spec.Command),
			Required: spec.Required,
		}
		out, err := ex.CombinedOutput(cfg.RepoDir, nil, "bash", "-lc", res.Command)
		res.Output = strings.TrimSpace(out)
		if err != nil {
			res.Passed = false
			res.Error = strings.TrimSpace(err.Error())
			if res.Required {
				res.TerminalFail = true
				run.Passed = false
				if firstTerminalErr == nil {
					detail := firstNonEmptyValue(res.Output, res.Error)
					firstTerminalErr = fmt.Errorf("%s failed: %s", res.Name, detail)
				}
			}
		} else {
			res.Passed = true
		}
		run.Checks = append(run.Checks, res)
	}
	if firstTerminalErr != nil {
		return run, firstTerminalErr
	}
	return run, nil
}

func runReviewLoop(ex commandExecutor, cfg config, checksReport deterministicChecksReport) (result reviewLoopResult, finalAgentOutput, finalLogPath, finalOutputPath string, err error) {
	finalLogPath = cfg.GooseLogPath
	finalOutputPath = cfg.AgentOutputPath

	latestChecks := checksReport.latest()
	reviewer := func(passNumber int, phase reviewPassPhase, priorFindings []reviewFinding) (reviewerOutput, error) {
		findingsPath := filepath.Join(cfg.MetaDir, fmt.Sprintf("review-findings.pass-%d.json", passNumber))
		summaryPath := filepath.Join(cfg.MetaDir, fmt.Sprintf("review-summary.pass-%d.md", passNumber))
		instructionsPath := filepath.Join(cfg.MetaDir, fmt.Sprintf("review-instructions.pass-%d.md", passNumber))
		logPath := filepath.Join(cfg.MetaDir, fmt.Sprintf("review-agent.pass-%d.ndjson", passNumber))
		outputPath := filepath.Join(cfg.MetaDir, fmt.Sprintf("review-agent.pass-%d.txt", passNumber))

		if err := removeFileIfExists(findingsPath); err != nil {
			return reviewerOutput{}, err
		}
		if err := removeFileIfExists(summaryPath); err != nil {
			return reviewerOutput{}, err
		}
		snapshot := collectGitSnapshot(ex, cfg)
		prompt := buildReviewerInstructions(cfg, passNumber, phase, findingsPath, summaryPath, latestChecks, snapshot, priorFindings)
		if err := os.WriteFile(instructionsPath, []byte(prompt), 0o644); err != nil {
			return reviewerOutput{}, fmt.Errorf("write reviewer instructions: %w", err)
		}
		roleCfg := configForSecondaryPass(cfg, instructionsPath, logPath, outputPath)
		if _, _, err := runAgent(ex, roleCfg); err != nil {
			return reviewerOutput{}, err
		}
		out, err := loadReviewerOutput(findingsPath)
		if err != nil {
			return reviewerOutput{}, err
		}
		if data, readErr := os.ReadFile(summaryPath); readErr == nil {
			out.Summary = firstNonEmptyValue(strings.TrimSpace(string(data)), out.Summary)
		}
		return out, nil
	}

	authorFix := func(passNumber int, findings []reviewFinding) error {
		instructionsPath := filepath.Join(cfg.MetaDir, fmt.Sprintf("author-fix-instructions.pass-%d.md", passNumber))
		logPath := filepath.Join(cfg.MetaDir, fmt.Sprintf("author-fix.pass-%d.ndjson", passNumber))
		outputPath := filepath.Join(cfg.MetaDir, fmt.Sprintf("author-fix.pass-%d.txt", passNumber))
		prompt := buildAuthorFixInstructions(cfg, passNumber, findings)
		if err := os.WriteFile(instructionsPath, []byte(prompt), 0o644); err != nil {
			return fmt.Errorf("write author fix instructions: %w", err)
		}
		roleCfg := configForSecondaryPass(cfg, instructionsPath, logPath, outputPath)
		out, _, err := runAgent(ex, roleCfg)
		if err != nil {
			return err
		}
		finalAgentOutput = out
		finalLogPath = logPath
		finalOutputPath = outputPath
		return nil
	}

	verifyAfterFix := func(passNumber int) error {
		stage := fmt.Sprintf("post_fix_%d", passNumber)
		run, err := runDeterministicChecks(ex, cfg, stage)
		checksReport.Runs = append(checksReport.Runs, run)
		if writeErr := writeJSONFile(cfg.DeterministicChecksPath, checksReport); writeErr != nil {
			return writeErr
		}
		if err != nil {
			return err
		}
		latestChecks = run
		return nil
	}

	loop := reviewLoopExecutor{cfg: cfg.ReviewLoop}
	result, loopErr := loop.Execute(reviewLoopHooks{
		RunReviewerPass:  reviewer,
		RunAuthorFixPass: authorFix,
		VerifyAfterFix:   verifyAfterFix,
	})
	writeErr := writeReviewArtifacts(cfg, result)
	if loopErr != nil {
		if writeErr != nil {
			return result, finalAgentOutput, finalLogPath, finalOutputPath, fmt.Errorf("%v (artifact write failed: %w)", loopErr, writeErr)
		}
		return result, finalAgentOutput, finalLogPath, finalOutputPath, loopErr
	}
	if writeErr != nil {
		return result, finalAgentOutput, finalLogPath, finalOutputPath, writeErr
	}
	return result, finalAgentOutput, finalLogPath, finalOutputPath, nil
}

type reviewGitSnapshot struct {
	StatusShort  string
	ChangedFiles string
	DiffStat     string
	Diff         string
}

func collectGitSnapshot(ex commandExecutor, cfg config) reviewGitSnapshot {
	return reviewGitSnapshot{
		StatusShort:  captureCommandForPrompt(ex, cfg.RepoDir, "git", "status", "--short"),
		ChangedFiles: captureCommandForPrompt(ex, cfg.RepoDir, "git", "diff", "--name-only"),
		DiffStat:     captureCommandForPrompt(ex, cfg.RepoDir, "git", "diff", "--stat", "--no-color"),
		Diff:         truncateForPrompt(captureCommandForPrompt(ex, cfg.RepoDir, "git", "diff", "--unified=0", "--no-color"), 32000),
	}
}

func captureCommandForPrompt(ex commandExecutor, dir string, name string, args ...string) string {
	out, err := ex.CombinedOutput(dir, nil, name, args...)
	text := strings.TrimSpace(out)
	if err != nil {
		if text == "" {
			return fmt.Sprintf("(command failed: %v)", err)
		}
		return fmt.Sprintf("%s\n\n(command failed: %v)", text, err)
	}
	if text == "" {
		return "(empty)"
	}
	return text
}

func truncateForPrompt(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	trimmed := text[:maxBytes]
	return trimmed + "\n\n...(truncated)..."
}

func buildReviewerInstructions(cfg config, passNumber int, phase reviewPassPhase, findingsPath, summaryPath string, checks deterministicCheckRun, snapshot reviewGitSnapshot, priorFindings []reviewFinding) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, `# Rascal Reviewer Pass

Role: You are a strict code reviewer. You must not edit repository code.
Pass: %d (%s)
Run ID: %s
Task ID: %s
Repository: %s

## Review Scope
- Requested task and constraints
- Current git diff/changed files
- Deterministic local check outputs
- Relevant issue/PR context

## Task
%s

## Context
- Trigger: %s
- Issue: %d
- Pull Request: %d
- Additional context: %s

## Deterministic Checks
%s

## Git Snapshot
### git status --short
%s

### git diff --name-only
%s

### git diff --stat --no-color
%s

### git diff --unified=0 --no-color (truncated)
%s

## Prior Findings (if any)
%s

## Output Contract (required)
1. Write JSON findings to:
%s

JSON schema:
{
  "summary": "short pass summary",
  "findings": [
    {
      "severity": "must_fix | should_fix | nit",
      "path": "file path when applicable",
      "rationale": "why this matters",
      "suggested_change": "specific change suggestion"
    }
  ]
}

2. Write a concise human-readable markdown summary to:
%s

If there are no findings, write an empty findings list and state "No findings."
Do not rely on conversational state from prior passes.
`, passNumber, phase, cfg.RunID, cfg.TaskID, cfg.Repo, cfg.Task, cfg.Trigger, cfg.IssueNumber, cfg.PRNumber, firstNonEmptyValue(cfg.Context, "(none)"),
		renderDeterministicChecks(checks), snapshot.StatusShort, snapshot.ChangedFiles, snapshot.DiffStat, snapshot.Diff,
		renderPriorFindings(priorFindings), findingsPath, summaryPath)
	return b.String()
}

func buildAuthorFixInstructions(cfg config, passNumber int, findings []reviewFinding) string {
	var b strings.Builder
	findingsJSON, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		findingsJSON = []byte("[]")
	}
	_, _ = fmt.Fprintf(&b, `# Rascal Author Fix Pass

Role: You are the implementing author.
Pass: %d
Run ID: %s
Task ID: %s
Repository: %s

## Task
%s

## Required Input
Apply fixes based only on:
1) The original task above
2) The reviewer findings JSON below

Do not rely on hidden conversational state.

## Reviewer Findings JSON
%s

## Constraints
- Fix all `+"`must_fix`"+` and `+"`should_fix`"+` findings where feasible.
- Keep scope minimal and avoid unrelated changes.
- Update tests as needed.
- Do not create commit messages or PR content in this pass.
`, passNumber, cfg.RunID, cfg.TaskID, cfg.Repo, cfg.Task, string(findingsJSON))
	return b.String()
}

func renderPriorFindings(findings []reviewFinding) string {
	if len(findings) == 0 {
		return "(none)"
	}
	data, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return "(failed to render prior findings)"
	}
	return string(data)
}

func renderDeterministicChecks(run deterministicCheckRun) string {
	if len(run.Checks) == 0 {
		return "(no deterministic checks configured)"
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "Stage: %s\nPassed: %t\n", run.Stage, run.Passed)
	for _, check := range run.Checks {
		_, _ = fmt.Fprintf(&b, "\n- %s (required=%t passed=%t)\n", check.Name, check.Required, check.Passed)
		if check.Command != "" {
			_, _ = fmt.Fprintf(&b, "  command: %s\n", check.Command)
		}
		if strings.TrimSpace(check.Output) != "" {
			_, _ = fmt.Fprintf(&b, "  output:\n%s\n", truncateForPrompt(check.Output, 4000))
		}
		if strings.TrimSpace(check.Error) != "" {
			_, _ = fmt.Fprintf(&b, "  error: %s\n", check.Error)
		}
	}
	return strings.TrimSpace(b.String())
}

func configForSecondaryPass(base config, instructionsPath, logPath, outputPath string) config {
	cfg := base
	cfg.InstructionsPath = instructionsPath
	cfg.GooseLogPath = logPath
	cfg.AgentOutputPath = outputPath
	cfg.AgentSessionMode = agent.SessionModeOff
	cfg.AgentSessionResume = false
	cfg.AgentSessionKey = ""
	cfg.BackendSessionID = ""
	cfg.GooseSessionMode = runner.GooseSessionModeOff
	cfg.GooseSessionResume = false
	cfg.GooseSessionKey = ""
	cfg.GooseSessionName = ""
	return cfg
}

func loadReviewerOutput(path string) (reviewerOutput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return reviewerOutput{}, fmt.Errorf("read reviewer findings %s: %w", path, err)
	}
	var out reviewerOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return reviewerOutput{}, fmt.Errorf("decode reviewer findings %s: %w", path, err)
	}
	out.Summary = strings.TrimSpace(out.Summary)
	out.Findings = normalizeFindings(out.Findings)
	return out, nil
}

func writeJSONFile(path string, payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json artifact %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write json artifact %s: %w", path, err)
	}
	return nil
}

func writeReviewArtifacts(cfg config, result reviewLoopResult) error {
	if err := writeJSONFile(cfg.ReviewLoopSummaryPath, result); err != nil {
		return err
	}
	findingsArtifact := struct {
		Ran            bool               `json:"ran"`
		ReviewerPasses int                `json:"reviewer_passes"`
		FinalFindings  []reviewFinding    `json:"final_findings"`
		NoFindings     bool               `json:"no_findings"`
		Passes         []reviewPassRecord `json:"passes"`
	}{
		Ran:            result.Ran,
		ReviewerPasses: result.ReviewerPasses,
		FinalFindings:  result.FinalFindings,
		NoFindings:     len(result.FinalFindings) == 0,
		Passes:         result.Passes,
	}
	if err := writeJSONFile(cfg.ReviewFindingsPath, findingsArtifact); err != nil {
		return err
	}
	if err := os.WriteFile(cfg.ReviewSummaryPath, []byte(buildReviewSummaryMarkdown(result)), 0o644); err != nil {
		return fmt.Errorf("write review summary %s: %w", cfg.ReviewSummaryPath, err)
	}
	return nil
}

func removeFileIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func buildReviewSummaryMarkdown(result reviewLoopResult) string {
	var b strings.Builder
	b.WriteString("# Review Loop Summary\n\n")
	_, _ = fmt.Fprintf(&b, "- Review loop ran: %t\n", result.Ran)
	_, _ = fmt.Fprintf(&b, "- Reviewer passes: %d\n", result.ReviewerPasses)
	_, _ = fmt.Fprintf(&b, "- Author fix passes: %d\n", result.AuthorFixPasses)
	_, _ = fmt.Fprintf(&b, "- Findings in final pass: %d\n", result.FinalFindingCount)
	_, _ = fmt.Fprintf(&b, "- Fixes applied: %t\n", result.FixesApplied)
	_, _ = fmt.Fprintf(&b, "- Exited cleanly: %t\n", result.exitedCleanly())
	_, _ = fmt.Fprintf(&b, "- Unresolved reviewer findings: %t\n", result.UnresolvedFindings)
	_, _ = fmt.Fprintf(&b, "- Budget exhausted: %t\n", result.BudgetExhausted)
	_, _ = fmt.Fprintf(&b, "- Verification skipped: %t\n", result.VerificationSkipped)
	if len(result.Passes) > 0 {
		b.WriteString("\n## Passes\n\n")
		for _, pass := range result.Passes {
			_, _ = fmt.Fprintf(&b, "- Pass %d (%s): %d findings", pass.PassNumber, pass.Phase, pass.FindingCount)
			if strings.TrimSpace(pass.Summary) != "" {
				_, _ = fmt.Fprintf(&b, " - %s", strings.TrimSpace(pass.Summary))
			}
			b.WriteByte('\n')
		}
	}
	if len(result.FinalFindings) > 0 {
		b.WriteString("\n## Final Findings\n\n")
		for _, finding := range result.FinalFindings {
			_, _ = fmt.Fprintf(&b, "- [%s] %s", finding.Severity, firstNonEmptyValue(finding.Rationale, "(no rationale)"))
			if strings.TrimSpace(finding.Path) != "" {
				_, _ = fmt.Fprintf(&b, " (%s)", finding.Path)
			}
			if strings.TrimSpace(finding.SuggestedChange) != "" {
				_, _ = fmt.Fprintf(&b, " -> %s", finding.SuggestedChange)
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func promoteAuthorOutputs(logPath, outputPath string, cfg config) error {
	if path := strings.TrimSpace(logPath); path != "" && !samePath(path, cfg.GooseLogPath) {
		if _, err := os.Stat(path); err == nil {
			if err := copyFile(path, cfg.GooseLogPath, 0o644); err != nil {
				return fmt.Errorf("promote agent log: %w", err)
			}
		}
	}
	if path := strings.TrimSpace(outputPath); path != "" && !samePath(path, cfg.AgentOutputPath) {
		if _, err := os.Stat(path); err == nil {
			if err := copyFile(path, cfg.AgentOutputPath, 0o644); err != nil {
				return fmt.Errorf("promote agent output: %w", err)
			}
		}
	}
	return nil
}

func runAgent(ex commandExecutor, cfg config) (string, string, error) {
	switch configuredAgentBackend(cfg) {
	case agent.BackendCodex:
		return runCodex(ex, cfg)
	default:
		return runGoose(ex, cfg)
	}
}

func runGoose(ex commandExecutor, cfg config) (string, string, error) {
	sessionID := configuredBackendSessionID(cfg)
	sessionMode := configuredSessionMode(cfg)
	sessionKey := configuredSessionKey(cfg)
	resume := configuredSessionResume(cfg)
	if resume && sessionID != "" {
		exists, err := gooseSessionExists(ex, cfg, sessionID)
		if err != nil {
			log.Printf("[%s] goose session preflight warning: name=%s error=%v", nowUTC(), sessionID, err)
		} else if !exists {
			log.Printf("[%s] goose session missing; starting fresh session name=%s", nowUTC(), sessionID)
			resume = false
		}
	}

	log.Printf("[%s] running goose (backend=%s debug=%t session_mode=%s session_key=%s session_name=%s resume=%t path_root=%s)",
		nowUTC(),
		configuredAgentBackend(cfg),
		cfg.GooseDebug,
		sessionMode,
		sessionKey,
		sessionID,
		resume,
		cfg.GoosePathRoot,
	)

	firstAttemptArgs := gooseRunArgs(cfg, resume)
	if err := runGooseOnce(ex, cfg, firstAttemptArgs); err != nil {
		if resume && isSessionResumeFailure(err, cfg.GooseLogPath) {
			log.Printf("[%s] goose session resume failed; falling back to fresh session name=%s reason=%s", nowUTC(), sessionID, strings.TrimSpace(err.Error()))
			if resetErr := resetGooseSessionRoot(cfg.GoosePathRoot); resetErr != nil {
				log.Printf("[%s] goose session reset warning: %v", nowUTC(), resetErr)
			}
			fallbackArgs := gooseRunArgs(cfg, false)
			if retryErr := runGooseOnce(ex, cfg, fallbackArgs); retryErr != nil {
				if writeErr := ensureGooseLogHasError(cfg.GooseLogPath); writeErr != nil {
					log.Printf("[%s] goose fallback log write warning: %v", nowUTC(), writeErr)
				}
				return "", sessionID, fmt.Errorf("goose run failed after session fallback: %w", retryErr)
			}
			log.Printf("[%s] goose session fallback succeeded; started fresh session name=%s", nowUTC(), sessionID)
		} else {
			if writeErr := ensureGooseLogHasError(cfg.GooseLogPath); writeErr != nil {
				log.Printf("[%s] goose log write warning: %v", nowUTC(), writeErr)
			}
			return "", sessionID, fmt.Errorf("goose run failed: %w", err)
		}
	}
	data, err := os.ReadFile(cfg.GooseLogPath)
	if err != nil {
		return "", sessionID, fmt.Errorf("read goose log: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "(no goose output captured)", sessionID, nil
	}
	return string(data), sessionID, nil
}

func gooseRunArgs(cfg config, resume bool) []string {
	args := []string{"run"}
	if configuredSessionMode(cfg) != agent.SessionModeOff && configuredBackendSessionID(cfg) != "" {
		args = append(args, "--name", configuredBackendSessionID(cfg))
		if resume {
			args = append(args, "--resume")
		}
	} else {
		args = append(args, "--no-session")
	}
	args = append(args, "-i", cfg.InstructionsPath, "--output-format", "stream-json")
	if cfg.GooseDebug {
		args = append(args, "--debug")
	}
	return args
}

func runGooseOnce(ex commandExecutor, cfg config, args []string) (err error) {
	logFile, err := os.OpenFile(cfg.GooseLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open goose log: %w", err)
	}
	defer func() {
		if closeErr := logFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close goose log: %w", closeErr)
		}
	}()

	env := []string{}
	if cfg.GooseDebug {
		env = append(env, "GOOSE_CODEX_DEBUG=1")
	}
	if err := ex.Run(cfg.RepoDir, env, logFile, logFile, "goose", args...); err != nil {
		return fmt.Errorf("run goose command: %w", err)
	}
	return nil
}

func runCodex(ex commandExecutor, cfg config) (output string, sessionID string, err error) {
	if err := ensureCodexHome(cfg); err != nil {
		return "", "", fmt.Errorf("ensure codex home: %w", err)
	}

	instructions, err := os.ReadFile(cfg.InstructionsPath)
	if err != nil {
		return "", "", fmt.Errorf("read instructions: %w", err)
	}

	args := codexRunArgs(cfg)
	log.Printf("[%s] running codex (backend=%s session_mode=%s session_key=%s session_id=%s resume=%t home=%s)",
		nowUTC(),
		configuredAgentBackend(cfg),
		configuredSessionMode(cfg),
		configuredSessionKey(cfg),
		configuredBackendSessionID(cfg),
		configuredSessionResume(cfg) && strings.TrimSpace(configuredBackendSessionID(cfg)) != "",
		cfg.CodexHome,
	)

	logFile, err := os.OpenFile(cfg.GooseLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", "", fmt.Errorf("open codex log: %w", err)
	}
	defer func() {
		if closeErr := logFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close codex log: %w", closeErr)
		}
	}()

	if err := ex.RunWithInput(cfg.RepoDir, nil, strings.NewReader(string(instructions)), logFile, logFile, "codex", args...); err != nil {
		sessionID, discoverErr := discoverLatestCodexSessionID(cfg.CodexHome)
		if discoverErr != nil {
			log.Printf("[%s] codex session discovery warning: %v", nowUTC(), discoverErr)
		}
		return "", sessionID, fmt.Errorf("codex run failed: %w", err)
	}

	sessionID, err = discoverLatestCodexSessionID(cfg.CodexHome)
	if err != nil {
		return "", "", fmt.Errorf("discover codex session: %w", err)
	}

	output, err = loadAgentOutput(cfg.AgentOutputPath, cfg.GooseLogPath, "codex")
	if err != nil {
		return "", sessionID, err
	}
	return output, sessionID, nil
}

func codexRunArgs(cfg config) []string {
	args := []string{"exec"}
	sessionID := strings.TrimSpace(configuredBackendSessionID(cfg))
	if configuredSessionResume(cfg) && sessionID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--full-auto", "--skip-git-repo-check", "-o", cfg.AgentOutputPath)
	if configuredSessionResume(cfg) && sessionID != "" {
		args = append(args, sessionID)
	} else {
		args = append(args, "-s", "workspace-write")
	}
	args = append(args, "-")
	return args
}

func ensureCodexHome(cfg config) error {
	if err := os.MkdirAll(cfg.CodexHome, 0o755); err != nil {
		return fmt.Errorf("create codex home: %w", err)
	}
	sourcePath := filepath.Join(cfg.MetaDir, "codex", defaultCodexAuthFile)
	if _, err := os.Stat(sourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat codex auth: %w", err)
	}
	targetPath := filepath.Join(cfg.CodexHome, defaultCodexAuthFile)
	if samePath(sourcePath, targetPath) {
		return nil
	}
	if err := copyFile(sourcePath, targetPath, 0o600); err != nil {
		return fmt.Errorf("copy codex auth into home: %w", err)
	}
	return nil
}

func loadAgentOutput(outputPath, fallbackLogPath, backend string) (string, error) {
	if data, err := os.ReadFile(outputPath); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			return text, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read agent output: %w", err)
	}

	data, err := os.ReadFile(fallbackLogPath)
	if err != nil {
		return "", fmt.Errorf("read %s log: %w", backend, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Sprintf("(no %s output captured)", backend), nil
	}
	return string(data), nil
}

func discoverLatestCodexSessionID(codexHome string) (string, error) {
	sessionFiles, err := listCodexSessionFiles(filepath.Join(strings.TrimSpace(codexHome), defaultCodexSessionDir))
	if err != nil {
		return "", fmt.Errorf("list codex session files: %w", err)
	}
	for _, sessionFile := range sessionFiles {
		sessionID, err := parseCodexSessionID(sessionFile)
		if err != nil {
			continue
		}
		if strings.TrimSpace(sessionID) != "" {
			return strings.TrimSpace(sessionID), nil
		}
	}
	return "", nil
}

func listCodexSessionFiles(root string) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat codex sessions root: %w", err)
	}

	type sessionFile struct {
		path    string
		modTime time.Time
	}
	var sessionFiles []sessionFile
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walk codex sessions: %w", err)
		}
		if info.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		sessionFiles = append(sessionFiles, sessionFile{path: path, modTime: info.ModTime()})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk codex sessions: %w", err)
	}

	sort.Slice(sessionFiles, func(i, j int) bool {
		if sessionFiles[i].modTime.Equal(sessionFiles[j].modTime) {
			return sessionFiles[i].path > sessionFiles[j].path
		}
		return sessionFiles[i].modTime.After(sessionFiles[j].modTime)
	})

	paths := make([]string, 0, len(sessionFiles))
	for _, sessionFile := range sessionFiles {
		paths = append(paths, sessionFile.path)
	}
	return paths, nil
}

func parseCodexSessionID(path string) (sessionID string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open codex session file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close codex session file: %w", closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read codex session file: %w", err)
		}
		return "", nil
	}

	var record struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		return "", fmt.Errorf("decode codex session metadata: %w", err)
	}
	return strings.TrimSpace(record.Payload.ID), nil
}

func samePath(left, right string) bool {
	return filepath.Clean(strings.TrimSpace(left)) == filepath.Clean(strings.TrimSpace(right))
}

func copyFile(src, dst string, mode os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source file %s: %w", src, err)
	}
	defer func() {
		if closeErr := in.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close source file %s: %w", src, closeErr)
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open destination file %s: %w", dst, err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close destination file %s: %w", dst, closeErr)
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

func gooseSessionExists(ex commandExecutor, cfg config, name string) (bool, error) {
	out, err := runCommand(ex, cfg.RepoDir, nil, "goose", "session", "list", "--format", "json")
	if err != nil {
		return false, fmt.Errorf("list goose sessions: %w", err)
	}
	var sessions []gooseSessionInfo
	if err := json.Unmarshal([]byte(out), &sessions); err != nil {
		return false, fmt.Errorf("decode goose session list: %w", err)
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.Name) == name {
			return true, nil
		}
	}
	return false, nil
}

func ensureGooseLogHasError(path string) error {
	if stat, err := os.Stat(path); err == nil && stat.Size() == 0 {
		if err := os.WriteFile(path, []byte(`{"event":"error","message":"goose run failed"}`+"\n"), 0o644); err != nil {
			return fmt.Errorf("write goose fallback error log: %w", err)
		}
	}
	return nil
}

func resetGooseSessionRoot(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("recreate goose session root: %w", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read goose session root: %w", err)
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		if err := os.RemoveAll(child); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove goose session root entry %s: %w", child, err)
		}
	}
	return nil
}

func isSessionResumeFailure(err error, logPath string) bool {
	var b strings.Builder
	if err != nil {
		b.WriteString(err.Error())
	}
	if data, readErr := os.ReadFile(logPath); readErr == nil {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.Write(data)
	}
	text := strings.ToLower(b.String())
	if text == "" {
		return false
	}
	hasSessionContext := strings.Contains(text, "session") || strings.Contains(text, "resume")
	if !hasSessionContext {
		return false
	}
	for _, marker := range []string{
		"not found",
		"no session found",
		"no such file",
		"no existing",
		"cannot find",
		"can't find",
		"does not exist",
		"missing",
		"corrupt",
		"invalid",
		"malformed",
		"failed to load",
		"failed loading",
		"decode",
		"deserialize",
		"unmarshal",
		"state",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func loadAgentCommitMessage(path string) (title, body string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("read commit message: %w", err)
	}
	title = firstNonEmptyLine(string(data))
	body, err = runsummary.ParseCommitBody(data)
	if err != nil {
		return "", "", fmt.Errorf("parse commit body: %w", err)
	}
	return title, body, nil
}

func firstNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line != "" {
			return line
		}
	}
	return ""
}

func loadPRView(ex commandExecutor, cfg config) (prView, bool, error) {
	out, err := runCommand(ex, cfg.RepoDir, nil, "gh", "pr", "view", cfg.HeadBranch, "--repo", cfg.Repo, "--json", "number,url")
	if err != nil {
		return prView{}, false, nil
	}
	var view prView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return prView{}, false, fmt.Errorf("decode gh pr view output: %w", err)
	}
	return view, true, nil
}

func runStage(name string, fn func() error) error {
	start := time.Now()
	log.Printf("[%s] stage_start stage=%s", nowUTC(), name)
	err := fn()
	duration := time.Since(start).Round(time.Millisecond)
	if err != nil {
		log.Printf("[%s] stage_fail stage=%s duration=%s error=%v", nowUTC(), name, duration, err)
		return fmt.Errorf("stage %s: %w", name, err)
	}
	log.Printf("[%s] stage_done stage=%s duration=%s", nowUTC(), name, duration)
	return nil
}

func runCommand(ex commandExecutor, dir string, extraEnv []string, name string, args ...string) (string, error) {
	out, err := ex.CombinedOutput(dir, extraEnv, name, args...)
	if err != nil {
		return out, fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func taskSubject(task, fallback string) string {
	s := strings.Join(strings.Fields(strings.ReplaceAll(strings.TrimSpace(task), "\r", " ")), " ")
	if s == "" {
		s = strings.TrimSpace(fallback)
	}
	if len(s) > 58 {
		return s[:55] + "..."
	}
	return s
}

func isConventionalTitle(title string) bool {
	return convCommitPattern.MatchString(strings.TrimSpace(title))
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func buildInfoSummary() string {
	return fmt.Sprintf("version=%s commit=%s built=%s", buildVersion, buildCommit, buildTime)
}
