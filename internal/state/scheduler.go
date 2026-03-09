package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/state/sqlitegen"
)

func (s *Store) ListQueuedRunsOrdered(limit int) ([]Run, error) {
	if limit <= 0 {
		limit = s.maxRuns
	}
	rows, err := s.q.ListQueuedRunsOrdered(context.Background(), int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list queued runs ordered: %w", err)
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBRun(row))
	}
	return out, nil
}

func (s *Store) ListQueuedRunsForTask(taskID string, limit int) ([]Run, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = s.maxRuns
	}
	rows, err := s.q.ListQueuedRunsForTask(context.Background(), sqlitegen.ListQueuedRunsForTaskParams{
		TaskID: taskID,
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list queued runs for task %q: %w", taskID, err)
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBRun(row))
	}
	return out, nil
}

func (s *Store) ClaimQueuedRunByID(runID string) (Run, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Run{}, false, fmt.Errorf("run id is required")
	}
	now := time.Now().UTC().UnixNano()
	row, err := s.q.ClaimQueuedRunByID(context.Background(), sqlitegen.ClaimQueuedRunByIDParams{
		UpdatedAt: now,
		StartedAt: sql.NullInt64{Int64: now, Valid: true},
		RunID:     runID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, nil
		}
		return Run{}, false, fmt.Errorf("claim queued run by id %q: %w", runID, err)
	}
	return fromDBRun(row), true, nil
}
