package worker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/runsummary"
)

func resolveGitIdentityWithToken(ex CommandExecutor, token string) (string, string, error) {
	out, err := runCommand(ex, "", githubCLIEnv(token), "gh", "api", "user")
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

func checkoutRepo(ex CommandExecutor, cfg Config) error {
	repoURL := fmt.Sprintf("https://github.com/%s.git", cfg.Repo)
	gitHubEnv := gitHubRemoteEnv(cfg.GitHubToken)
	if _, err := os.Stat(filepath.Join(cfg.RepoDir, ".git")); err == nil {
		log.Printf("[%s] repo already present, refreshing", nowUTC())
		if _, err := runCommand(ex, "", gitHubEnv, "git", "-C", cfg.RepoDir, "fetch", "--all", "--prune"); err != nil {
			return fmt.Errorf("refresh existing checkout: %w", err)
		}
	} else {
		log.Printf("[%s] cloning %s", nowUTC(), cfg.Repo)
		if _, err := runCommand(ex, "", gitHubEnv, "git", "clone", repoURL, cfg.RepoDir); err != nil {
			return fmt.Errorf("clone repository: %w", err)
		}
	}

	if _, err := runCommand(ex, cfg.RepoDir, gitHubEnv, "git", "fetch", "origin", cfg.BaseBranch, cfg.HeadBranch); err != nil {
		log.Printf("[%s] git fetch warning: %v", nowUTC(), err)
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", cfg.BaseBranch); err != nil {
		if _, createErr := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.BaseBranch, "origin/"+cfg.BaseBranch); createErr != nil {
			return fmt.Errorf("checkout base branch %s: %w", cfg.BaseBranch, createErr)
		}
	}
	if _, err := runCommand(ex, cfg.RepoDir, gitHubEnv, "git", "pull", "--ff-only", "origin", cfg.BaseBranch); err != nil {
		log.Printf("[%s] git pull warning: %v", nowUTC(), err)
	}

	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "--verify", cfg.HeadBranch); err == nil {
		_, err = runCommand(ex, cfg.RepoDir, nil, "git", "checkout", cfg.HeadBranch)
		if err != nil {
			return fmt.Errorf("checkout head branch %s: %w", cfg.HeadBranch, err)
		}
		return nil
	}
	if _, err := runCommand(ex, cfg.RepoDir, gitHubEnv, "git", "ls-remote", "--exit-code", "--heads", "origin", cfg.HeadBranch); err == nil {
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

func configureRepoGitIdentity(ex CommandExecutor, repoDir, authorName, authorEmail string) error {
	authorName = strings.TrimSpace(authorName)
	authorEmail = strings.TrimSpace(authorEmail)
	if authorName == "" || authorEmail == "" {
		return fmt.Errorf("git identity requires non-empty author name and email")
	}
	if _, err := runCommand(ex, repoDir, nil, "git", "config", "user.name", authorName); err != nil {
		return fmt.Errorf("configure repo git user.name: %w", err)
	}
	if _, err := runCommand(ex, repoDir, nil, "git", "config", "user.email", authorEmail); err != nil {
		return fmt.Errorf("configure repo git user.email: %w", err)
	}
	return nil
}

func githubCLIEnv(token string) []string {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return []string{"GH_TOKEN=" + token}
}

func gitHubRemoteEnv(token string) []string {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=" + header,
	}
}

func LoadAgentCommitMessage(path string) (title, body string, err error) {
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

func NormalizeRepoLocalMetaArtifacts(cfg Config) error {
	repoDir := strings.TrimSpace(cfg.RepoDir)
	if repoDir == "" {
		return nil
	}
	repoLocalMetaDir := filepath.Join(repoDir, "rascal-meta")
	repoLocalCommitPath := filepath.Join(repoLocalMetaDir, defaultCommitMsgFile)
	commitPath := strings.TrimSpace(cfg.CommitMsgPath)
	if commitPath != "" && commitPath != repoLocalCommitPath {
		if data, err := os.ReadFile(repoLocalCommitPath); err == nil {
			if _, statErr := os.Stat(commitPath); errors.Is(statErr, os.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(commitPath), 0o755); err != nil {
					return fmt.Errorf("create commit message directory: %w", err)
				}
				if err := os.WriteFile(commitPath, data, 0o644); err != nil {
					return fmt.Errorf("adopt repo-local commit message: %w", err)
				}
				log.Printf("[%s] adopted repo-local commit message into %s", nowUTC(), commitPath)
			} else if statErr != nil {
				return fmt.Errorf("stat commit message path: %w", statErr)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read repo-local commit message: %w", err)
		}
	}
	if err := os.RemoveAll(repoLocalMetaDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove repo-local rascal-meta: %w", err)
	}
	return nil
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

func loadPRView(ex CommandExecutor, cfg Config) (prView, bool, error) {
	out, err := runCommand(ex, cfg.RepoDir, githubCLIEnv(cfg.GitHubToken), "gh", "pr", "view", cfg.HeadBranch, "--repo", cfg.Repo, "--json", "number,url")
	if err != nil {
		return prView{}, false, nil
	}
	var view prView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return prView{}, false, fmt.Errorf("decode gh pr view output: %w", err)
	}
	return view, true, nil
}

func branchAheadOfBase(ex CommandExecutor, cfg Config) (bool, error) {
	out, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-list", "--left-right", "--count", "origin/"+cfg.BaseBranch+"...HEAD")
	if err != nil {
		return false, fmt.Errorf("compare branch with origin/%s: %w", cfg.BaseBranch, err)
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return false, fmt.Errorf("unexpected git rev-list output: %q", strings.TrimSpace(out))
	}
	ahead, err := strconv.Atoi(parts[1])
	if err != nil {
		return false, fmt.Errorf("parse ahead count %q: %w", parts[1], err)
	}
	return ahead > 0, nil
}

func RunStage(name string, fn func() error) error {
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

func runCommand(ex CommandExecutor, dir string, extraEnv []string, name string, args ...string) (string, error) {
	out, err := ex.CombinedOutput(dir, extraEnv, name, args...)
	if err != nil {
		return out, fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func TaskSubject(task, fallback string) string {
	s := strings.Join(strings.Fields(strings.ReplaceAll(strings.TrimSpace(task), "\r", " ")), " ")
	if s == "" {
		s = strings.TrimSpace(fallback)
	}
	if len(s) > 58 {
		return s[:55] + "..."
	}
	return s
}

func IsConventionalTitle(title string) bool {
	return convCommitPattern.MatchString(strings.TrimSpace(title))
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func BuildInfoSummary() string {
	return fmt.Sprintf("version=%s commit=%s built=%s", BuildVersion, BuildCommit, BuildTime)
}
