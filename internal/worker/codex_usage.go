package worker

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtzll/rascal/internal/runsummary"
)

type codexUsageBaseline struct {
	sessionPath string
	offset      int64
	usage       runsummary.TokenUsage
	hasUsage    bool
}

func captureCodexUsageBaseline(cfg Config) (codexUsageBaseline, error) {
	sessionPath, err := resolveCodexSessionPath(cfg.CodexHome, configuredRuntimeSessionID(cfg))
	if err != nil {
		return codexUsageBaseline{}, err
	}
	if strings.TrimSpace(sessionPath) == "" {
		return codexUsageBaseline{}, nil
	}

	info, err := os.Stat(sessionPath)
	if err != nil {
		return codexUsageBaseline{}, fmt.Errorf("stat codex session file: %w", err)
	}

	baseline := codexUsageBaseline{
		sessionPath: sessionPath,
		offset:      info.Size(),
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return baseline, fmt.Errorf("read codex session file: %w", err)
	}
	usage, ok, err := runsummary.ExtractCodexSessionUsage(string(data))
	if err != nil {
		return baseline, fmt.Errorf("extract codex session baseline usage: %w", err)
	}
	if ok {
		baseline.usage = usage
		baseline.hasUsage = true
	}
	return baseline, nil
}

func recordCodexRunTokenUsage(cfg Config, baseline codexUsageBaseline, sessionID string) {
	usage, ok, err := loadCodexRunTokenUsage(cfg, baseline, sessionID)
	if err != nil {
		log.Printf("[%s] codex token usage capture warning: %v", nowUTC(), err)
		return
	}
	if !ok {
		return
	}
	if err := runsummary.WriteRecordedTokenUsage(filepath.Join(cfg.MetaDir, runsummary.RecordedTokenUsageFile), usage); err != nil {
		log.Printf("[%s] codex token usage persist warning: %v", nowUTC(), err)
	}
}

func loadCodexRunTokenUsage(cfg Config, baseline codexUsageBaseline, sessionID string) (runsummary.TokenUsage, bool, error) {
	sessionPath, err := resolveCodexSessionPath(cfg.CodexHome, sessionID)
	if err != nil {
		return runsummary.TokenUsage{}, false, err
	}
	if strings.TrimSpace(sessionPath) == "" {
		return runsummary.TokenUsage{}, false, nil
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return runsummary.TokenUsage{}, false, fmt.Errorf("read codex session file: %w", err)
	}

	finalUsage, finalOK, finalErr := runsummary.ExtractCodexSessionUsage(string(data))

	offset := baseline.offset
	if samePath(sessionPath, baseline.sessionPath) && offset > int64(len(data)) {
		offset = int64(len(data))
	}
	if offset > 0 && samePath(sessionPath, baseline.sessionPath) {
		if deltaUsage, ok, err := runsummary.ExtractCodexSessionUsageDelta(string(data[offset:])); ok {
			if finalOK {
				deltaUsage.Provider = firstNonEmptyValue(deltaUsage.Provider, finalUsage.Provider)
				deltaUsage.Model = firstNonEmptyValue(deltaUsage.Model, finalUsage.Model)
			}
			return deltaUsage, true, nil
		} else if err != nil {
			finalErr = err
		}
	} else if offset == 0 {
		if deltaUsage, ok, err := runsummary.ExtractCodexSessionUsageDelta(string(data)); ok {
			if finalOK {
				deltaUsage.Provider = firstNonEmptyValue(deltaUsage.Provider, finalUsage.Provider)
				deltaUsage.Model = firstNonEmptyValue(deltaUsage.Model, finalUsage.Model)
			}
			return deltaUsage, true, nil
		} else if err != nil {
			finalErr = err
		}
	}

	if finalOK {
		if baseline.hasUsage {
			if deltaUsage, ok := runsummary.SubtractTokenUsage(finalUsage, baseline.usage); ok {
				return deltaUsage, true, nil
			}
			return runsummary.TokenUsage{}, false, nil
		}
		return finalUsage, true, nil
	}
	if finalErr != nil {
		return runsummary.TokenUsage{}, false, finalErr
	}
	return runsummary.TokenUsage{}, false, nil
}

func resolveCodexSessionPath(codexHome, sessionID string) (string, error) {
	sessionFiles, err := listCodexSessionFiles(filepath.Join(strings.TrimSpace(codexHome), defaultCodexSessionDir))
	if err != nil {
		return "", fmt.Errorf("list codex session files: %w", err)
	}
	if len(sessionFiles) == 0 {
		return "", nil
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessionFiles[0], nil
	}

	for _, path := range sessionFiles {
		currentID, err := parseCodexSessionID(path)
		if err != nil {
			continue
		}
		if strings.TrimSpace(currentID) == sessionID {
			return path, nil
		}
	}
	return "", nil
}
