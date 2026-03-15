package worker

import (
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
	defaultPRLabel                    = "rascal"
	defaultCodexAuthFile              = "auth.json"
	defaultCodexSessionDir            = "sessions"
	defaultClaudeOAuthFile            = "oauth_token"
)

var (
	convCommitPattern = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9._/-]+\))?(!)?:[[:space:]].+`)
	prURLPattern      = regexp.MustCompile(`https://github\.com/[^[:space:]]+/pull/[0-9]+`)

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
	started := time.Now().UTC()

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
		if err := os.MkdirAll(filepath.Join(cfg.MetaDir, "pi"), 0o755); err != nil {
			return fmt.Errorf("create pi dir: %w", err)
		}
		if err := os.MkdirAll(cfg.PiSessionDir, 0o755); err != nil {
			return fmt.Errorf("create pi session dir: %w", err)
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

	var authorName string
	var authorEmail string
	if err := RunStage("resolve_identity", func() error {
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

	if err := RunStage("checkout_repo", func() error {
		return checkoutRepo(ex, cfg)
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

	commitTitle := fmt.Sprintf("chore(rascal): %s", TaskSubject(cfg.Instruction, cfg.TaskID))
	commitBody := ""
	if err := RunStage("prepare_commit", func() error {
		if err := NormalizeRepoLocalMetaArtifacts(cfg); err != nil {
			return fmt.Errorf("normalize repo-local meta artifacts: %w", err)
		}
		if title, body, msgErr := LoadAgentCommitMessage(cfg.CommitMsgPath); msgErr != nil {
			return fmt.Errorf("load agent commit message: %w", msgErr)
		} else {
			commitBody = body
			if title != "" {
				if IsConventionalTitle(title) {
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

	if err := RunStage("check_branch_diff", func() error {
		ahead, err := branchAheadOfBase(ex, cfg)
		if err != nil {
			return err
		}
		if !ahead {
			return fmt.Errorf("agent produced no commits ahead of %s; skipping branch push and pull request creation", cfg.BaseBranch)
		}
		return nil
	}); err != nil {
		return fail(err)
	}

	if err := RunStage("push_branch", func() error {
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
	if err := RunStage("load_pr", func() error {
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
		if err := RunStage("pr_create", func() error {
			log.Printf("[%s] creating pull request", nowUTC())
			closesSection := ""
			if cfg.IssueNumber > 0 {
				closesSection = fmt.Sprintf("\n\nCloses #%d", cfg.IssueNumber)
			}
			runDuration := runsummary.FormatDuration(int64(time.Since(started).Seconds()))
			var totalTokens *int64
			if usage, ok, err := runsummary.ReadRecordedTokenUsage(filepath.Join(cfg.MetaDir, runsummary.RecordedTokenUsageFile)); err != nil {
				log.Printf("[%s] recorded token usage warning: %v", nowUTC(), err)
			} else if ok && usage.TotalTokens > 0 {
				totalTokens = &usage.TotalTokens
			}
			body := runsummary.BuildPRBody(cfg.RunID, commitBody, agentOutput, runDuration, closesSection, totalTokens)
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

	if err := RunStage("finalize_meta", func() error {
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
