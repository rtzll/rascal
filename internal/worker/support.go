package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/runsummary"
)

type publishService struct {
	server    *http.Server
	listener  net.Listener
	endpoint  string
	authToken string
}

type publishRequest struct {
	Source         string `json:"source"`
	Branch         string `json:"branch"`
	ForceWithLease bool   `json:"force_with_lease"`
}

type publishResponse struct {
	Branch         string `json:"branch"`
	Scope          string `json:"scope"`
	ForceWithLease bool   `json:"force_with_lease"`
}

func resolveGitIdentity(ex CommandExecutor, githubToken string) (string, string, error) {
	out, err := runCommand(ex, "", githubAuthEnv(githubToken), "gh", "api", "user")
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
	repoURL := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", cfg.GitHubToken, cfg.Repo)
	if _, err := os.Stat(filepath.Join(cfg.RepoDir, ".git")); err == nil {
		log.Printf("[%s] repo already present, refreshing", nowUTC())
		if _, err := runCommand(ex, "", nil, "git", "-C", cfg.RepoDir, "remote", "set-url", "origin", repoURL); err != nil {
			return fmt.Errorf("reset origin url: %w", err)
		}
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
		return configurePushAccess(ex, cfg)
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "ls-remote", "--exit-code", "--heads", "origin", cfg.HeadBranch); err == nil {
		_, err = runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch, "origin/"+cfg.HeadBranch)
		if err != nil {
			return fmt.Errorf("checkout remote head branch %s: %w", cfg.HeadBranch, err)
		}
		return configurePushAccess(ex, cfg)
	}
	_, err := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch)
	if err != nil {
		return fmt.Errorf("create head branch %s: %w", cfg.HeadBranch, err)
	}
	return configurePushAccess(ex, cfg)
}

func configurePushAccess(ex CommandExecutor, cfg Config) error {
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "remote", "set-url", "--push", "origin", "no_push://rascal-disabled"); err != nil {
		return fmt.Errorf("disable direct git push: %w", err)
	}
	return nil
}

func githubAuthEnv(githubToken string) []string {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return nil
	}
	return []string{"GH_TOKEN=" + githubToken}
}

func publishAgentEnv(cfg Config, publishSvc *publishService) []string {
	env := []string{
		"RASCAL_PUBLISH_SCOPE=" + strings.TrimSpace(cfg.PublishScope),
		"RASCAL_PUBLISH_BRANCHES=" + strings.Join(cfg.PublishBranches, ","),
	}
	if publishSvc == nil {
		return env
	}
	env = append(env,
		"RASCAL_PUBLISH_ENDPOINT="+publishSvc.endpoint,
		"RASCAL_PUBLISH_AUTH_TOKEN="+publishSvc.authToken,
	)
	return env
}

func scrubAgentGitHubAuth() error {
	if err := os.Unsetenv("GH_TOKEN"); err != nil {
		return fmt.Errorf("unset GH_TOKEN: %w", err)
	}
	if err := os.Unsetenv("GITHUB_TOKEN"); err != nil {
		return fmt.Errorf("unset GITHUB_TOKEN: %w", err)
	}
	return nil
}

func startPublishService(cfg Config) (*publishService, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate publish auth token: %w", err)
	}
	authToken := hex.EncodeToString(tokenBytes)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on publish socket: %w", err)
	}

	mux := http.NewServeMux()
	svc := &publishService{
		listener:  listener,
		endpoint:  "http://" + listener.Addr().String(),
		authToken: authToken,
	}
	mux.HandleFunc("/publish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if subtleAuthHeaderMismatch(r.Header.Get("Authorization"), authToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req publishRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		source := strings.TrimSpace(req.Source)
		if source == "" {
			source = "HEAD"
		}
		branch := strings.TrimSpace(req.Branch)
		if branch == "" {
			branch = strings.TrimSpace(cfg.HeadBranch)
		}
		if err := validatePublishBranch(cfg, branch); err != nil {
			log.Printf("[%s] publish_denied scope=%s branch=%s force_with_lease=%t error=%v", nowUTC(), strings.TrimSpace(cfg.PublishScope), branch, req.ForceWithLease, err)
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if err := runPublish(cfg, source, branch, req.ForceWithLease); err != nil {
			log.Printf("[%s] publish_failed scope=%s branch=%s force_with_lease=%t error=%v", nowUTC(), strings.TrimSpace(cfg.PublishScope), branch, req.ForceWithLease, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		log.Printf("[%s] publish_succeeded scope=%s branch=%s force_with_lease=%t", nowUTC(), strings.TrimSpace(cfg.PublishScope), branch, req.ForceWithLease)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(publishResponse{
			Branch:         branch,
			Scope:          strings.TrimSpace(cfg.PublishScope),
			ForceWithLease: req.ForceWithLease,
		}); err != nil {
			log.Printf("[%s] publish_response_write_failed branch=%s error=%v", nowUTC(), branch, err)
		}
	})
	svc.server = &http.Server{Handler: mux}
	go func() {
		if err := svc.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[%s] publish service stopped unexpectedly: %v", nowUTC(), err)
		}
	}()
	return svc, nil
}

func (s *publishService) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown publish service: %w", err)
	}
	return nil
}

func subtleAuthHeaderMismatch(headerValue, authToken string) bool {
	expected := "Bearer " + strings.TrimSpace(authToken)
	if len(headerValue) != len(expected) {
		return true
	}
	return !strings.EqualFold(headerValue[:7], "Bearer ") || !constantTimeEqual(headerValue, expected)
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for i := range left {
		diff |= left[i] ^ right[i]
	}
	return diff == 0
}

func validatePublishBranch(cfg Config, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("publish branch is required")
	}
	switch strings.TrimSpace(cfg.PublishScope) {
	case "", "branch_scoped", "task_scoped":
		for _, allowed := range cfg.PublishBranches {
			if strings.TrimSpace(allowed) == branch {
				return nil
			}
		}
		return fmt.Errorf("publish to branch %q is not allowed for scope %q", branch, firstNonEmptyValue(cfg.PublishScope, "branch_scoped"))
	case "repo_maintainer":
		if branch == strings.TrimSpace(cfg.BaseBranch) {
			return fmt.Errorf("publish to protected base branch %q is not allowed for repo_maintainer scope", branch)
		}
		return nil
	case "unrestricted":
		return nil
	default:
		return fmt.Errorf("unsupported publish scope %q", cfg.PublishScope)
	}
}

func runPublish(cfg Config, source, branch string, forceWithLease bool) error {
	authURL := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", cfg.GitHubToken, cfg.Repo)
	args := []string{"-C", cfg.RepoDir, "-c", "remote.origin.pushurl=" + authURL, "push"}
	if forceWithLease {
		args = append(args, "--force-with-lease")
	}
	args = append(args, "origin", source+":"+branch)
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text == "" {
			return fmt.Errorf("git push failed: %w", err)
		}
		return fmt.Errorf("git push failed: %w (%s)", err, text)
	}
	localSHA, err := gitRefSHA(cfg.RepoDir, source)
	if err != nil {
		return fmt.Errorf("resolve pushed ref %q: %w", source, err)
	}
	if err := exec.Command("git", "-C", cfg.RepoDir, "update-ref", "refs/remotes/origin/"+branch, localSHA).Run(); err != nil {
		return fmt.Errorf("update remote-tracking ref for %s: %w", branch, err)
	}
	return nil
}

func gitRefSHA(repoDir, ref string) (string, error) {
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", ref).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
		}
		return "", fmt.Errorf("git rev-parse %s: %w (%s)", ref, err, text)
	}
	return text, nil
}

func ensureWorkspaceClean(ex CommandExecutor, cfg Config) error {
	statusOut, err := runCommand(ex, cfg.RepoDir, nil, "git", "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status --porcelain: %w", err)
	}
	if strings.TrimSpace(statusOut) != "" {
		return fmt.Errorf("working tree is dirty; commit, stash, or discard changes before finishing")
	}
	return nil
}

func ensurePublishedState(ex CommandExecutor, cfg Config) error {
	switch strings.TrimSpace(cfg.PublishScope) {
	case "repo_maintainer", "unrestricted":
		return nil
	}
	localSHA, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	remoteHeadRef := "refs/remotes/origin/" + strings.TrimSpace(cfg.HeadBranch)
	if remoteSHA, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "--verify", remoteHeadRef); err == nil {
		if strings.TrimSpace(remoteSHA) != strings.TrimSpace(localSHA) {
			return fmt.Errorf("head branch %q is not published; use rascal-publish before finishing", cfg.HeadBranch)
		}
		return nil
	}
	baseRef := "refs/remotes/origin/" + strings.TrimSpace(cfg.BaseBranch)
	baseSHA, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "--verify", baseRef)
	if err != nil {
		return nil
	}
	if strings.TrimSpace(baseSHA) != strings.TrimSpace(localSHA) {
		return fmt.Errorf("head branch %q has unpublished commits; use rascal-publish before finishing", cfg.HeadBranch)
	}
	return nil
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
	out, err := runCommand(ex, cfg.RepoDir, githubAuthEnv(cfg.GitHubToken), "gh", "pr", "view", cfg.HeadBranch, "--repo", cfg.Repo, "--json", "number,url")
	if err != nil {
		return prView{}, false, nil
	}
	var view prView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return prView{}, false, fmt.Errorf("decode gh pr view output: %w", err)
	}
	return view, true, nil
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
