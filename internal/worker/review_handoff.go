package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rtzll/rascal/internal/reviewhandoff"
)

func buildReviewHandoff(ex CommandExecutor, cfg Config, excludeReviewer string) (reviewhandoff.Report, error) {
	changedFiles, err := loadChangedFiles(ex, cfg)
	if err != nil {
		return reviewhandoff.Report{}, err
	}

	codeowners, err := loadCODEOWNERS(cfg.RepoDir)
	if err != nil {
		return reviewhandoff.Report{}, err
	}

	history, err := loadHistoryTouches(ex, cfg, changedFiles)
	if err != nil {
		return reviewhandoff.Report{}, err
	}

	report := reviewhandoff.Analyze(reviewhandoff.Input{
		BaseRef:           cfg.BaseBranch,
		HeadRef:           "HEAD",
		ChangedFiles:      changedFiles,
		Codeowners:        codeowners,
		History:           history,
		ExcludedReviewers: []string{excludeReviewer},
	})
	if err := reviewhandoff.WriteArtifacts(cfg.MetaDir, report); err != nil {
		return reviewhandoff.Report{}, fmt.Errorf("write review handoff artifacts: %w", err)
	}
	return report, nil
}

func loadChangedFiles(ex CommandExecutor, cfg Config) ([]reviewhandoff.ChangedFile, error) {
	out, err := runCommand(ex, cfg.RepoDir, nil, "git", "diff", "--numstat", cfg.BaseBranch+"...HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat %s...HEAD: %w", cfg.BaseBranch, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	changed := make([]reviewhandoff.ChangedFile, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		added := parseNumstatCount(fields[0])
		deleted := parseNumstatCount(fields[1])
		path := normalizeNumstatPath(fields[2:])
		if path == "" {
			continue
		}
		changed = append(changed, reviewhandoff.ChangedFile{
			Path:    path,
			Added:   added,
			Deleted: deleted,
		})
	}
	return changed, nil
}

func loadCODEOWNERS(repoDir string) (string, error) {
	for _, candidate := range []string{
		filepath.Join(repoDir, ".github", "CODEOWNERS"),
		filepath.Join(repoDir, "CODEOWNERS"),
		filepath.Join(repoDir, "docs", "CODEOWNERS"),
	} {
		data, err := os.ReadFile(candidate)
		if err == nil {
			return string(data), nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("read CODEOWNERS: %w", err)
		}
	}
	return "", nil
}

func loadHistoryTouches(ex CommandExecutor, cfg Config, changed []reviewhandoff.ChangedFile) ([]reviewhandoff.HistoryTouch, error) {
	touches := make([]reviewhandoff.HistoryTouch, 0)
	for _, file := range changed {
		out, err := runCommand(ex, cfg.RepoDir, nil, "git", "log", "-n", "12", "--format=%an <%ae>%n%cn <%ce>", "--", file.Path)
		if err != nil {
			return nil, fmt.Errorf("git log history for %s: %w", file.Path, err)
		}
		for _, line := range strings.Split(out, "\n") {
			reviewer := strings.TrimSpace(line)
			if reviewer == "" {
				continue
			}
			touches = append(touches, reviewhandoff.HistoryTouch{
				Path:     file.Path,
				Reviewer: reviewer,
			})
		}
	}
	return touches, nil
}

func parseNumstatCount(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

func normalizeNumstatPath(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	path := strings.TrimSpace(parts[len(parts)-1])
	if strings.Contains(path, "=>") {
		if idx := strings.LastIndex(path, "=>"); idx >= 0 {
			path = path[idx+2:]
		}
	}
	path = strings.Trim(path, "{} ")
	return strings.TrimSpace(path)
}
