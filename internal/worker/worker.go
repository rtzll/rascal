package worker

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rtzll/rascal/internal/runner"
)

const (
	defaultMetaDir                    = "/rascal-meta"
	defaultWorkRoot                   = "/work"
	defaultRepoDirName                = "repo"
	defaultAgentLogFile               = "agent.ndjson"
	defaultMetaFile                   = "meta.json"
	defaultInstructionsFile           = "instructions.md"
	defaultPersistentInstructionsFile = "persistent_instructions.md"
	defaultCommitMsgFile              = "commit_message.txt"
	defaultAgentOutputFile            = "agent_output.txt"
	defaultPRBodyFile                 = "pr_body.md"
	defaultCodexAuthFile              = "auth.json"
	defaultCodexSessionDir            = "sessions"
	defaultClaudeOAuthFile            = "oauth_token"
)

var (
	convCommitPattern = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9._/-]+\))?(!)?:[[:space:]].+`)

	BuildVersion = "dev"
	BuildCommit  = "unknown"
	BuildTime    = "unknown"
)

type prView struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

type CommandExecutor interface {
	LookPath(name string) error
	CombinedOutput(dir string, extraEnv []string, name string, args ...string) (string, error)
	Run(dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error
	RunWithInput(dir string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

type OSExecutor struct{}

func (OSExecutor) LookPath(name string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("look up %s: %w", name, err)
	}
	return nil
}

func (OSExecutor) CombinedOutput(dir string, extraEnv []string, name string, args ...string) (string, error) {
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

func (OSExecutor) Run(dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error {
	return OSExecutor{}.RunWithInput(dir, extraEnv, nil, stdout, stderr, name, args...)
}

func (OSExecutor) RunWithInput(dir string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
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

func Run() error {
	return RunWithExecutor(OSExecutor{})
}

func RunWithExecutor(ex CommandExecutor) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load Config: %w", err)
	}

	meta := runner.Meta{
		RunID:       cfg.RunID,
		TaskID:      cfg.TaskID,
		Repo:        cfg.Repo,
		BaseBranch:  cfg.BaseBranch,
		HeadBranch:  cfg.HeadBranch,
		BuildCommit: strings.TrimSpace(BuildCommit),
		ExitCode:    1,
	}
	if err := runner.WriteMeta(cfg.MetaPath, meta); err != nil {
		log.Printf("[%s] failed to write initial meta: %v", nowUTC(), err)
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

	if err := RunStage("prepare_workspace", func() error {
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

	if err := RunStage("prepare_instructions", func() error {
		if err := ensureInstructions(cfg); err != nil {
			return err
		}
		return ensurePersistentInstructions(cfg)
	}); err != nil {
		return fail(err)
	}
	log.Printf("[%s] run started run_id=%s repo=%s", nowUTC(), cfg.RunID, cfg.Repo)

	if err := RunStage("validate_commands", func() error {
		return validateCommands(ex, cfg)
	}); err != nil {
		return fail(err)
	}

	if err := RunStage("checkout_repo", func() error {
		return checkoutRepo(ex, cfg)
	}); err != nil {
		return fail(err)
	}

	if err := RunStage("configure_git_identity", func() error {
		authorName, authorEmail, err := resolveGitIdentity(ex)
		if err != nil {
			return fmt.Errorf("resolve git identity: %w", err)
		}
		if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "config", "user.name", authorName); err != nil {
			return fmt.Errorf("git config user.name: %w", err)
		}
		if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "config", "user.email", authorEmail); err != nil {
			return fmt.Errorf("git config user.email: %w", err)
		}
		log.Printf("[%s] configured local git identity: %s <%s>", nowUTC(), authorName, authorEmail)
		return nil
	}); err != nil {
		return fail(err)
	}

	var agentOutput string
	if err := RunStage("run_agent", func() error {
		var err error
		var agentSessionID string
		agentOutput, agentSessionID, err = runAgent(ex, cfg)
		if strings.TrimSpace(agentSessionID) != "" {
			meta.TaskSessionID = strings.TrimSpace(agentSessionID)
		}
		return err
	}); err != nil {
		return fail(err)
	}

	if _, statErr := os.Stat(filepath.Join(cfg.RepoDir, "Makefile")); statErr == nil {
		if err := RunStage("verify", func() error {
			log.Printf("[%s] running lightweight verify: make -n test", nowUTC())
			if _, err := runCommand(ex, cfg.RepoDir, nil, "make", "-n", "test"); err != nil {
				return fmt.Errorf("preview make test target: %w", err)
			}
			return nil
		}); err != nil {
			log.Printf("[%s] lightweight verify warning: %v", nowUTC(), err)
		}
	}

	if err := RunStage("normalize_artifacts", func() error {
		if err := NormalizeRepoLocalMetaArtifacts(cfg); err != nil {
			return fmt.Errorf("normalize repo-local meta artifacts: %w", err)
		}
		return nil
	}); err != nil {
		return fail(err)
	}

	if err := RunStage("finalize_meta", func() error {
		headSHA, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("git rev-parse HEAD: %w", err)
		}
		meta.HeadSHA = strings.TrimSpace(headSHA)
		view, found, err := loadPRView(ex, cfg)
		if err != nil {
			return fmt.Errorf("load pull request view: %w", err)
		}
		if found {
			meta.PRNumber = view.Number
			meta.PRURL = strings.TrimSpace(view.URL)
		}
		if strings.TrimSpace(agentOutput) != "" {
			log.Printf("[%s] captured agent output bytes=%d", nowUTC(), len(agentOutput))
		}
		return nil
	}); err != nil {
		return fail(err)
	}

	meta.ExitCode = 0
	meta.Error = ""
	log.Printf("[%s] run completed exit_code=0", nowUTC())
	return nil
}
