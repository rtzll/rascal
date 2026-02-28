package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type persistentState struct {
	Runs       []Run                `json:"runs"`
	Deliveries map[string]time.Time `json:"deliveries"`
}

type Store struct {
	mu         sync.RWMutex
	path       string
	maxRuns    int
	runs       map[string]Run
	order      []string
	deliveries map[string]time.Time
}

func New(path string, maxRuns int) (*Store, error) {
	if maxRuns <= 0 {
		maxRuns = 200
	}

	s := &Store{
		path:       path,
		maxRuns:    maxRuns,
		runs:       make(map[string]Run),
		deliveries: make(map[string]time.Time),
	}

	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func NewRunID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("create run id: %w", err)
	}
	return "run_" + hex.EncodeToString(buf), nil
}

func (s *Store) AddRun(in CreateRunInput) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if in.ID == "" || in.TaskID == "" || in.Repo == "" {
		return Run{}, fmt.Errorf("id, task_id and repo are required")
	}
	if _, exists := s.runs[in.ID]; exists {
		return Run{}, fmt.Errorf("run %q already exists", in.ID)
	}

	now := time.Now().UTC()
	baseBranch := in.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	r := Run{
		ID:         in.ID,
		TaskID:     in.TaskID,
		Repo:       in.Repo,
		Task:       in.Task,
		BaseBranch: baseBranch,
		HeadBranch: in.HeadBranch,
		Trigger:    in.Trigger,
		Status:     StatusQueued,
		RunDir:     in.RunDir,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if r.Trigger == "" {
		r.Trigger = "cli"
	}

	s.runs[r.ID] = r
	s.order = append([]string{r.ID}, s.order...)
	s.compactRunsLocked()

	if err := s.persistLocked(); err != nil {
		return Run{}, err
	}
	return r, nil
}

func (s *Store) GetRun(id string) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.runs[id]
	return r, ok
}

func (s *Store) ListRuns(limit int) []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.order) {
		limit = len(s.order)
	}

	out := make([]Run, 0, limit)
	for i := 0; i < limit; i++ {
		id := s.order[i]
		if r, ok := s.runs[id]; ok {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) UpdateRun(id string, fn func(*Run) error) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.runs[id]
	if !ok {
		return Run{}, fmt.Errorf("run %q not found", id)
	}
	if err := fn(&r); err != nil {
		return Run{}, err
	}
	r.UpdatedAt = time.Now().UTC()
	s.runs[id] = r

	if err := s.persistLocked(); err != nil {
		return Run{}, err
	}
	return r, nil
}

// SeenDelivery returns true if the delivery was already processed.
func (s *Store) SeenDelivery(deliveryID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if deliveryID == "" {
		return false, nil
	}
	if _, ok := s.deliveries[deliveryID]; ok {
		return true, nil
	}

	s.deliveries[deliveryID] = time.Now().UTC()
	s.compactDeliveriesLocked()
	if err := s.persistLocked(); err != nil {
		return false, err
	}
	return false, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open state file: %w", err)
	}
	defer f.Close()

	var p persistentState
	if err := json.NewDecoder(f).Decode(&p); err != nil {
		return fmt.Errorf("decode state file: %w", err)
	}

	for _, r := range p.Runs {
		s.runs[r.ID] = r
		s.order = append(s.order, r.ID)
	}
	if p.Deliveries != nil {
		s.deliveries = p.Deliveries
	}

	s.compactRunsLocked()
	s.compactDeliveriesLocked()
	return nil
}

func (s *Store) persistLocked() error {
	p := persistentState{
		Runs:       make([]Run, 0, len(s.order)),
		Deliveries: s.deliveries,
	}
	for _, id := range s.order {
		if r, ok := s.runs[id]; ok {
			p.Runs = append(p.Runs, r)
		}
	}

	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode state file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

func (s *Store) compactRunsLocked() {
	if len(s.order) <= s.maxRuns {
		return
	}
	cut := s.order[s.maxRuns:]
	for _, id := range cut {
		delete(s.runs, id)
	}
	s.order = s.order[:s.maxRuns]
}

func (s *Store) compactDeliveriesLocked() {
	const maxDeliveries = 1000
	if len(s.deliveries) <= maxDeliveries {
		return
	}

	type delivery struct {
		id string
		ts time.Time
	}
	items := make([]delivery, 0, len(s.deliveries))
	for id, ts := range s.deliveries {
		items = append(items, delivery{id: id, ts: ts})
	}

	for len(items) > maxDeliveries {
		oldest := 0
		for i := 1; i < len(items); i++ {
			if items[i].ts.Before(items[oldest].ts) {
				oldest = i
			}
		}
		delete(s.deliveries, items[oldest].id)
		items = append(items[:oldest], items[oldest+1:]...)
	}
}
