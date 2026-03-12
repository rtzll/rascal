package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/worker"
)

const defaultPRLabel = "rascal"

type capabilityResponseTarget struct {
	Repo           string `json:"repo"`
	IssueNumber    int    `json:"issue_number"`
	RequestedBy    string `json:"requested_by,omitempty"`
	Trigger        string `json:"trigger"`
	ReviewThreadID int64  `json:"review_thread_id,omitempty"`
}

type multiValueFlag []string

func (f *multiValueFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *multiValueFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func runCapabilityCommand(ex commandExecutor, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("capability command is required")
	}
	if strings.TrimSpace(args[0]) != "capability" {
		return fmt.Errorf("unknown rascal-runner command %q", args[0])
	}
	if len(args) < 2 {
		return fmt.Errorf("capability name is required")
	}
	cfg, err := worker.LoadConfig()
	if err != nil {
		return fmt.Errorf("load capability config: %w", err)
	}
	switch strings.TrimSpace(args[1]) {
	case "publish":
		return runPublishCapability(ex, cfg, args[2:])
	case "pr":
		return runPRCapability(ex, cfg, args[2:])
	case "comment":
		return runCommentCapability(ex, cfg, args[2:])
	default:
		return fmt.Errorf("unknown capability %q", args[1])
	}
}

func runPublishCapability(ex commandExecutor, cfg worker.Config, args []string) error {
	fs := newCapabilityFlagSet("publish")
	sourceRef := fs.String("source", "HEAD", "source ref to publish")
	forceWithLease := fs.Bool("force-with-lease", false, "force push with lease")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse publish flags: %w", err)
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("publish does not accept positional arguments")
	}
	headBranch := strings.TrimSpace(cfg.HeadBranch)
	if headBranch == "" {
		return fmt.Errorf("publish capability requires an allowed head branch")
	}
	source := strings.TrimSpace(*sourceRef)
	if source == "" {
		source = "HEAD"
	}
	refspec := fmt.Sprintf("%s:%s", source, headBranch)
	gitArgs := []string{"push"}
	if *forceWithLease {
		gitArgs = append(gitArgs, "--force-with-lease")
	}
	gitArgs = append(gitArgs, "origin", refspec)
	log.Printf("[%s] capability_publish repo=%s refspec=%s force_with_lease=%t", nowUTC(), cfg.Repo, refspec, *forceWithLease)
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", gitArgs...); err != nil {
		return fmt.Errorf("publish branch: %w", err)
	}
	return nil
}

func runPRCapability(ex commandExecutor, cfg worker.Config, args []string) error {
	fs := newCapabilityFlagSet("pr")
	title := fs.String("title", "", "pull request title")
	body := fs.String("body", "", "pull request body")
	bodyFile := fs.String("body-file", "", "path to a pull request body file")
	draft := fs.Bool("draft", false, "create a draft pull request")
	var labels multiValueFlag
	fs.Var(&labels, "label", "label to apply")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse pr flags: %w", err)
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("pr does not accept positional arguments")
	}
	baseBranch := strings.TrimSpace(cfg.BaseBranch)
	headBranch := strings.TrimSpace(cfg.HeadBranch)
	if baseBranch == "" || headBranch == "" {
		return fmt.Errorf("pr capability requires base and head branches")
	}

	bodyArgs, err := bodyArgs(*body, *bodyFile)
	if err != nil {
		return err
	}
	view, found, err := loadPRView(ex, cfg)
	if err != nil {
		return fmt.Errorf("load pull request view: %w", err)
	}

	if found {
		ghArgs := []string{"pr", "edit", strconv.Itoa(view.Number), "--repo", cfg.Repo}
		if strings.TrimSpace(*title) != "" {
			ghArgs = append(ghArgs, "--title", strings.TrimSpace(*title))
		}
		ghArgs = append(ghArgs, bodyArgs...)
		if deduped := dedupeStrings(labels); len(deduped) > 0 {
			ghArgs = append(ghArgs, "--add-label", strings.Join(deduped, ","))
		}
		if len(ghArgs) == 5 {
			return fmt.Errorf("pr capability requires at least one mutation when updating an existing pull request")
		}
		log.Printf("[%s] capability_pr_update repo=%s pr_number=%d", nowUTC(), cfg.Repo, view.Number)
		if _, err := runCommand(ex, cfg.RepoDir, nil, "gh", ghArgs...); err != nil {
			return fmt.Errorf("update pull request: %w", err)
		}
		if latest, ok, err := loadPRView(ex, cfg); err == nil && ok && strings.TrimSpace(latest.URL) != "" {
			if _, err := fmt.Fprintln(os.Stdout, strings.TrimSpace(latest.URL)); err != nil {
				return fmt.Errorf("write updated pull request url: %w", err)
			}
		}
		return nil
	}

	prTitle := strings.TrimSpace(*title)
	if prTitle == "" {
		return fmt.Errorf("pr capability requires --title when creating a pull request")
	}
	ghArgs := []string{
		"pr", "create",
		"--repo", cfg.Repo,
		"--base", baseBranch,
		"--head", headBranch,
		"--title", prTitle,
	}
	ghArgs = append(ghArgs, bodyArgs...)
	if *draft {
		ghArgs = append(ghArgs, "--draft")
	}
	allLabels := dedupeStrings(append(labels, defaultPRLabel))
	for _, label := range allLabels {
		ghArgs = append(ghArgs, "--label", label)
	}
	log.Printf("[%s] capability_pr_create repo=%s base=%s head=%s", nowUTC(), cfg.Repo, baseBranch, headBranch)
	out, err := runCommand(ex, cfg.RepoDir, nil, "gh", ghArgs...)
	if err != nil {
		return fmt.Errorf("create pull request: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		if _, err := fmt.Fprintln(os.Stdout, strings.TrimSpace(out)); err != nil {
			return fmt.Errorf("write created pull request url: %w", err)
		}
	}
	return nil
}

func runCommentCapability(ex commandExecutor, cfg worker.Config, args []string) error {
	fs := newCapabilityFlagSet("comment")
	body := fs.String("body", "", "comment body")
	bodyFile := fs.String("body-file", "", "path to a comment body file")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse comment flags: %w", err)
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("comment does not accept positional arguments")
	}

	target, ok, err := loadCapabilityResponseTarget(cfg.MetaDir)
	if err != nil {
		return fmt.Errorf("load response target: %w", err)
	}
	repo := strings.TrimSpace(cfg.Repo)
	issueNumber := 0
	if ok {
		if strings.TrimSpace(target.Repo) != "" {
			repo = strings.TrimSpace(target.Repo)
		}
		issueNumber = target.IssueNumber
	}
	if issueNumber <= 0 {
		if cfg.PRNumber > 0 {
			issueNumber = cfg.PRNumber
		} else {
			issueNumber = cfg.IssueNumber
		}
	}
	if repo == "" || issueNumber <= 0 {
		return fmt.Errorf("comment capability is not available for this run")
	}
	commentBody, err := resolveBodyValue(*body, *bodyFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(commentBody) == "" {
		return fmt.Errorf("comment capability requires --body or --body-file")
	}
	log.Printf("[%s] capability_comment repo=%s issue_number=%d review_thread_id=%d", nowUTC(), repo, issueNumber, target.ReviewThreadID)
	if _, err := runCommand(ex, cfg.RepoDir, nil, "gh", "api", "--method", "POST", fmt.Sprintf("repos/%s/issues/%d/comments", repo, issueNumber), "-f", "body="+commentBody); err != nil {
		return fmt.Errorf("post github comment: %w", err)
	}
	return nil
}

func newCapabilityFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func bodyArgs(body, bodyFile string) ([]string, error) {
	if strings.TrimSpace(body) != "" && strings.TrimSpace(bodyFile) != "" {
		return nil, fmt.Errorf("use either --body or --body-file, not both")
	}
	if strings.TrimSpace(bodyFile) != "" {
		return []string{"--body-file", strings.TrimSpace(bodyFile)}, nil
	}
	if strings.TrimSpace(body) != "" {
		return []string{"--body", body}, nil
	}
	return nil, nil
}

func resolveBodyValue(body, bodyFile string) (string, error) {
	if strings.TrimSpace(body) != "" && strings.TrimSpace(bodyFile) != "" {
		return "", fmt.Errorf("use either --body or --body-file, not both")
	}
	if strings.TrimSpace(bodyFile) != "" {
		data, err := os.ReadFile(strings.TrimSpace(bodyFile))
		if err != nil {
			return "", fmt.Errorf("read body file: %w", err)
		}
		return string(data), nil
	}
	return body, nil
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func loadCapabilityResponseTarget(metaDir string) (capabilityResponseTarget, bool, error) {
	path := filepath.Join(strings.TrimSpace(metaDir), "response_target.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return capabilityResponseTarget{}, false, nil
		}
		return capabilityResponseTarget{}, false, fmt.Errorf("read response target file: %w", err)
	}
	var target capabilityResponseTarget
	if err := json.Unmarshal(data, &target); err != nil {
		return capabilityResponseTarget{}, false, fmt.Errorf("decode response target file: %w", err)
	}
	return target, true, nil
}

type prView struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func loadPRView(ex commandExecutor, cfg worker.Config) (prView, bool, error) {
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

func runCommand(ex commandExecutor, dir string, extraEnv []string, name string, args ...string) (string, error) {
	out, err := ex.CombinedOutput(dir, extraEnv, name, args...)
	if err != nil {
		return out, fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
