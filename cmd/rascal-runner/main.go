package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runsummary"
)

const (
	defaultMetaDir          = "/rascal-meta"
	defaultWorkRoot         = "/work"
	defaultRepoDirName      = "repo"
	defaultGooseLogFile     = "goose.ndjson"
	defaultMetaFile         = "meta.json"
	defaultInstructionsFile = "instructions.md"
	defaultCommitMsgFile    = "commit_message.txt"
	defaultPRBodyFile       = "pr_body.md"
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
	Trigger     string
	GitHubToken string

	MetaDir          string
	WorkRoot         string
	RepoDir          string
	GooseLogPath     string
	MetaPath         string
	InstructionsPath string
	CommitMsgPath    string
	PRBodyPath       string

	GooseDebug bool
}

type prView struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

type commandExecutor interface {
	LookPath(name string) error
	CombinedOutput(dir string, extraEnv []string, name string, args ...string) (string, error)
	Run(dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error
}

type osExecutor struct{}

func (osExecutor) LookPath(name string) error {
	_, err := exec.LookPath(name)
	return err
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
	cmd := exec.Command(name, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
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
		return err
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
		if err := os.MkdirAll(filepath.Join(cfg.MetaDir, "codex"), 0o755); err != nil {
			return fmt.Errorf("create codex dir: %w", err)
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
		return requireCommands(ex, "git", "gh", "goose")
	}); err != nil {
		return fail(err)
	}

	var authorName string
	var authorEmail string
	if err := runStage("resolve_identity", func() error {
		var err error
		authorName, authorEmail, err = resolveGitIdentity(ex)
		return err
	}); err != nil {
		return fail(err)
	}
	log.Printf("[%s] using commit identity: %s <%s>", nowUTC(), authorName, authorEmail)

	if err := runStage("checkout_repo", func() error {
		return checkoutRepo(ex, cfg)
	}); err != nil {
		return fail(err)
	}

	var gooseOutput string
	if err := runStage("run_goose", func() error {
		var err error
		gooseOutput, err = runGoose(ex, cfg)
		return err
	}); err != nil {
		return fail(err)
	}

	if _, statErr := os.Stat(filepath.Join(cfg.RepoDir, "Makefile")); statErr == nil {
		_ = runStage("verify", func() error {
			log.Printf("[%s] running lightweight verify: make -n test", nowUTC())
			_, _ = runCommand(ex, cfg.RepoDir, nil, "make", "-n", "test")
			return nil
		})
	}

	commitTitle := fmt.Sprintf("chore(rascal): %s", taskSubject(cfg.Task, cfg.TaskID))
	commitBody := ""
	if err := runStage("prepare_commit", func() error {
		if title, body, msgErr := loadAgentCommitMessage(cfg.CommitMsgPath); msgErr != nil {
			return msgErr
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
			return err
		}
		if strings.TrimSpace(statusOut) != "" {
			if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "add", "-A"); err != nil {
				return err
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
				return err
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
		return err
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
			body := runsummary.BuildPRBody(cfg.RunID, commitBody, gooseOutput, runDuration, closesSection)
			if err := os.WriteFile(cfg.PRBodyPath, []byte(body), 0o644); err != nil {
				return fmt.Errorf("write pr body: %w", err)
			}

			out, err := runCommand(ex, cfg.RepoDir, nil, "gh", "pr", "create",
				"--repo", cfg.Repo,
				"--base", cfg.BaseBranch,
				"--head", cfg.HeadBranch,
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
			return err
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

	return config{
		RunID:            runID,
		TaskID:           taskID,
		Task:             strings.TrimSpace(os.Getenv("RASCAL_TASK")),
		Repo:             repo,
		BaseBranch:       baseBranch,
		HeadBranch:       headBranch,
		IssueNumber:      issueNumber,
		Trigger:          trigger,
		GitHubToken:      ghToken,
		MetaDir:          metaDir,
		WorkRoot:         workRoot,
		RepoDir:          repoDir,
		GooseLogPath:     filepath.Join(metaDir, defaultGooseLogFile),
		MetaPath:         filepath.Join(metaDir, defaultMetaFile),
		InstructionsPath: filepath.Join(metaDir, defaultInstructionsFile),
		CommitMsgPath:    filepath.Join(metaDir, defaultCommitMsgFile),
		PRBodyPath:       filepath.Join(metaDir, defaultPRBodyFile),
		GooseDebug:       debug,
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
			return err
		}
	} else {
		log.Printf("[%s] cloning %s", nowUTC(), cfg.Repo)
		if _, err := runCommand(ex, "", nil, "git", "clone", repoURL, cfg.RepoDir); err != nil {
			return err
		}
	}

	_, _ = runCommand(ex, cfg.RepoDir, nil, "git", "fetch", "origin", cfg.BaseBranch, cfg.HeadBranch)
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", cfg.BaseBranch); err != nil {
		if _, createErr := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.BaseBranch, "origin/"+cfg.BaseBranch); createErr != nil {
			return createErr
		}
	}
	_, _ = runCommand(ex, cfg.RepoDir, nil, "git", "pull", "--ff-only", "origin", cfg.BaseBranch)

	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "--verify", cfg.HeadBranch); err == nil {
		_, err = runCommand(ex, cfg.RepoDir, nil, "git", "checkout", cfg.HeadBranch)
		return err
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "ls-remote", "--exit-code", "--heads", "origin", cfg.HeadBranch); err == nil {
		_, err = runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch, "origin/"+cfg.HeadBranch)
		return err
	}
	_, err := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch)
	return err
}

func runGoose(ex commandExecutor, cfg config) (string, error) {
	log.Printf("[%s] running goose (debug=%t)", nowUTC(), cfg.GooseDebug)

	logFile, err := os.OpenFile(cfg.GooseLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("open goose log: %w", err)
	}
	defer logFile.Close()

	args := []string{"run", "--no-session", "-i", cfg.InstructionsPath, "--output-format", "stream-json"}
	env := []string{}
	if cfg.GooseDebug {
		args = append(args, "--debug")
		env = append(env, "GOOSE_CODEX_DEBUG=1")
	}

	// Keep goose.ndjson reserved for structured stdout. Goose debug output goes to
	// stderr, and mixing the two makes token extraction flaky because the final
	// complete event can be corrupted by interleaved debug lines.
	if err := ex.Run(cfg.RepoDir, env, logFile, os.Stderr, "goose", args...); err != nil {
		if stat, statErr := os.Stat(cfg.GooseLogPath); statErr == nil && stat.Size() == 0 {
			_ = os.WriteFile(cfg.GooseLogPath, []byte(`{"event":"error","message":"goose run failed"}`+"\n"), 0o644)
		}
		return "", fmt.Errorf("goose run failed: %w", err)
	}
	data, err := os.ReadFile(cfg.GooseLogPath)
	if err != nil {
		return "", fmt.Errorf("read goose log: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "(no goose output captured)", nil
	}
	return string(data), nil
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
	return ex.CombinedOutput(dir, extraEnv, name, args...)
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
