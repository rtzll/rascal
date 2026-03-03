package main

import (
	"encoding/json"
	"errors"
	"fmt"
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

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Printf("[%s] run failed: %v", nowUTC(), err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	started := time.Now().UTC()

	if err := os.MkdirAll(filepath.Join(cfg.MetaDir, "goose"), 0o755); err != nil {
		return fmt.Errorf("create goose dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.MetaDir, "codex"), 0o755); err != nil {
		return fmt.Errorf("create codex dir: %w", err)
	}
	if err := os.MkdirAll(cfg.WorkRoot, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}

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

	if err := ensureInstructions(cfg); err != nil {
		meta.Error = err.Error()
		return err
	}
	log.Printf("[%s] run started run_id=%s repo=%s", nowUTC(), cfg.RunID, cfg.Repo)

	if err := requireCommands("git", "gh", "goose"); err != nil {
		meta.Error = err.Error()
		return err
	}

	authorName, authorEmail, err := resolveGitIdentity()
	if err != nil {
		meta.Error = err.Error()
		return err
	}
	log.Printf("[%s] using commit identity: %s <%s>", nowUTC(), authorName, authorEmail)

	if err := checkoutRepo(cfg); err != nil {
		meta.Error = err.Error()
		return err
	}

	gooseOutput, err := runGoose(cfg)
	if err != nil {
		meta.Error = err.Error()
		return err
	}

	if _, statErr := os.Stat(filepath.Join(cfg.RepoDir, "Makefile")); statErr == nil {
		log.Printf("[%s] running lightweight verify: make -n test", nowUTC())
		_, _ = runCommand(cfg.RepoDir, nil, "make", "-n", "test")
	}

	commitTitle := fmt.Sprintf("chore(rascal): %s", taskSubject(cfg.Task, cfg.TaskID))
	commitBody := ""
	if title, body, msgErr := loadAgentCommitMessage(cfg.CommitMsgPath); msgErr != nil {
		meta.Error = msgErr.Error()
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

	statusOut, err := runCommand(cfg.RepoDir, nil, "git", "status", "--porcelain")
	if err != nil {
		meta.Error = err.Error()
		return err
	}
	if strings.TrimSpace(statusOut) != "" {
		if _, err := runCommand(cfg.RepoDir, nil, "git", "add", "-A"); err != nil {
			meta.Error = err.Error()
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
		if _, err := runCommand(cfg.RepoDir, commitEnv, "git", "commit", "-m", commitTitle, "-m", finalBody); err != nil {
			meta.Error = err.Error()
			return err
		}
	}

	log.Printf("[%s] pushing branch", nowUTC())
	if _, err := runCommand(cfg.RepoDir, nil, "git", "push", "-u", "origin", cfg.HeadBranch); err != nil {
		meta.Error = "git push failed: " + err.Error()
		return err
	}

	view, found, err := loadPRView(cfg)
	if err != nil {
		meta.Error = err.Error()
		return err
	}
	if !found {
		log.Printf("[%s] creating pull request", nowUTC())
		closesSection := ""
		if cfg.IssueNumber > 0 {
			closesSection = fmt.Sprintf("\n\nCloses #%d", cfg.IssueNumber)
		}
		runDuration := runsummary.FormatDuration(int64(time.Since(started).Seconds()))
		body := runsummary.BuildPRBody(cfg.RunID, commitBody, gooseOutput, runDuration, closesSection)
		if err := os.WriteFile(cfg.PRBodyPath, []byte(body), 0o644); err != nil {
			meta.Error = fmt.Sprintf("write pr body: %v", err)
			return fmt.Errorf("write pr body: %w", err)
		}

		out, err := runCommand(cfg.RepoDir, nil, "gh", "pr", "create",
			"--repo", cfg.Repo,
			"--base", cfg.BaseBranch,
			"--head", cfg.HeadBranch,
			"--title", commitTitle,
			"--body-file", cfg.PRBodyPath,
		)
		if err != nil {
			meta.Error = "gh pr create failed: " + err.Error()
			return err
		}

		if latest, ok, err := loadPRView(cfg); err == nil && ok {
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
	}

	headSHA, err := runCommand(cfg.RepoDir, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		meta.Error = err.Error()
		return err
	}
	meta.HeadSHA = strings.TrimSpace(headSHA)
	meta.PRNumber = view.Number
	meta.PRURL = strings.TrimSpace(view.URL)
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

func requireCommands(names ...string) error {
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("required command missing: %s", name)
		}
	}
	return nil
}

func resolveGitIdentity() (string, string, error) {
	out, err := runCommand("", nil, "gh", "api", "user")
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

func checkoutRepo(cfg config) error {
	repoURL := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", cfg.GitHubToken, cfg.Repo)
	if _, err := os.Stat(filepath.Join(cfg.RepoDir, ".git")); err == nil {
		log.Printf("[%s] repo already present, refreshing", nowUTC())
		if _, err := runCommand("", nil, "git", "-C", cfg.RepoDir, "fetch", "--all", "--prune"); err != nil {
			return err
		}
	} else {
		log.Printf("[%s] cloning %s", nowUTC(), cfg.Repo)
		if _, err := runCommand("", nil, "git", "clone", repoURL, cfg.RepoDir); err != nil {
			return err
		}
	}

	_, _ = runCommand(cfg.RepoDir, nil, "git", "fetch", "origin", cfg.BaseBranch, cfg.HeadBranch)
	if _, err := runCommand(cfg.RepoDir, nil, "git", "checkout", cfg.BaseBranch); err != nil {
		if _, createErr := runCommand(cfg.RepoDir, nil, "git", "checkout", "-b", cfg.BaseBranch, "origin/"+cfg.BaseBranch); createErr != nil {
			return createErr
		}
	}
	_, _ = runCommand(cfg.RepoDir, nil, "git", "pull", "--ff-only", "origin", cfg.BaseBranch)

	if _, err := runCommand(cfg.RepoDir, nil, "git", "rev-parse", "--verify", cfg.HeadBranch); err == nil {
		_, err = runCommand(cfg.RepoDir, nil, "git", "checkout", cfg.HeadBranch)
		return err
	}
	if _, err := runCommand(cfg.RepoDir, nil, "git", "ls-remote", "--exit-code", "--heads", "origin", cfg.HeadBranch); err == nil {
		_, err = runCommand(cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch, "origin/"+cfg.HeadBranch)
		return err
	}
	_, err := runCommand(cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch)
	return err
}

func runGoose(cfg config) (string, error) {
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

	cmd := exec.Command("goose", args...)
	cmd.Dir = cfg.RepoDir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Run(); err != nil {
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

func loadPRView(cfg config) (prView, bool, error) {
	out, err := runCommand(cfg.RepoDir, nil, "gh", "pr", "view", cfg.HeadBranch, "--repo", cfg.Repo, "--json", "number,url")
	if err != nil {
		return prView{}, false, nil
	}
	var view prView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return prView{}, false, fmt.Errorf("decode gh pr view output: %w", err)
	}
	return view, true, nil
}

func runCommand(dir string, extraEnv []string, name string, args ...string) (string, error) {
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
