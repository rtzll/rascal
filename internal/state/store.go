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
	Tasks      []Task               `json:"tasks"`
	Deliveries map[string]time.Time `json:"deliveries"`
	PRTaskMap  map[string]string    `json:"pr_task_map"`
}

type Store struct {
	mu         sync.RWMutex
	path       string
	maxRuns    int
	runs       map[string]Run
	order      []string
	tasks      map[string]Task
	deliveries map[string]time.Time
	prTaskMap  map[string]string
}

func New(path string, maxRuns int) (*Store, error) {
	if maxRuns <= 0 {
		maxRuns = 200
	}

	s := &Store{
		path:       path,
		maxRuns:    maxRuns,
		runs:       make(map[string]Run),
		tasks:      make(map[string]Task),
		deliveries: make(map[string]time.Time),
		prTaskMap:  make(map[string]string),
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

func (s *Store) UpsertTask(in UpsertTaskInput) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if in.ID == "" || in.Repo == "" {
		return Task{}, fmt.Errorf("task id and repo are required")
	}

	now := time.Now().UTC()
	task, exists := s.tasks[in.ID]
	if !exists {
		task = Task{
			ID:          in.ID,
			Repo:        in.Repo,
			IssueNumber: in.IssueNumber,
			PRNumber:    in.PRNumber,
			Status:      TaskOpen,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
	} else {
		if in.IssueNumber > 0 {
			task.IssueNumber = in.IssueNumber
		}
		if in.PRNumber > 0 {
			task.PRNumber = in.PRNumber
		}
		task.UpdatedAt = now
	}

	s.tasks[in.ID] = task
	if task.PRNumber > 0 {
		s.prTaskMap[prKey(task.Repo, task.PRNumber)] = task.ID
	}

	if err := s.persistLocked(); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) GetTask(taskID string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task, ok := s.tasks[taskID]
	return task, ok
}

func (s *Store) FindTaskByPR(repo string, prNumber int) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	taskID, ok := s.prTaskMap[prKey(repo, prNumber)]
	if !ok {
		return Task{}, false
	}
	task, ok := s.tasks[taskID]
	return task, ok
}

func (s *Store) SetTaskPR(taskID, repo string, prNumber int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	if prNumber <= 0 {
		return nil
	}

	task.PRNumber = prNumber
	task.UpdatedAt = time.Now().UTC()
	s.tasks[taskID] = task
	s.prTaskMap[prKey(repo, prNumber)] = taskID

	return s.persistLocked()
}

func (s *Store) SetTaskPendingInput(taskID string, pending bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	task.PendingInput = pending
	task.UpdatedAt = time.Now().UTC()
	s.tasks[taskID] = task
	return s.persistLocked()
}

func (s *Store) MarkTaskCompleted(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	task.Status = TaskCompleted
	task.PendingInput = false
	task.UpdatedAt = time.Now().UTC()
	s.tasks[taskID] = task
	return s.persistLocked()
}

func (s *Store) IsTaskCompleted(taskID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return false
	}
	return task.Status == TaskCompleted
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
		ID:          in.ID,
		TaskID:      in.TaskID,
		Repo:        in.Repo,
		Task:        in.Task,
		BaseBranch:  baseBranch,
		HeadBranch:  in.HeadBranch,
		Trigger:     in.Trigger,
		Status:      StatusQueued,
		RunDir:      in.RunDir,
		IssueNumber: in.IssueNumber,
		PRNumber:    in.PRNumber,
		Context:     in.Context,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if r.Trigger == "" {
		r.Trigger = "cli"
	}

	task, ok := s.tasks[in.TaskID]
	if !ok {
		task = Task{
			ID:          in.TaskID,
			Repo:        in.Repo,
			IssueNumber: in.IssueNumber,
			PRNumber:    in.PRNumber,
			Status:      TaskOpen,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
	} else {
		if in.IssueNumber > 0 {
			task.IssueNumber = in.IssueNumber
		}
		if in.PRNumber > 0 {
			task.PRNumber = in.PRNumber
		}
		task.UpdatedAt = now
	}
	task.LastRunID = r.ID
	s.tasks[task.ID] = task
	if task.PRNumber > 0 {
		s.prTaskMap[prKey(task.Repo, task.PRNumber)] = task.ID
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

	task, ok := s.tasks[r.TaskID]
	if ok {
		task.LastRunID = r.ID
		task.UpdatedAt = r.UpdatedAt
		if r.PRNumber > 0 {
			task.PRNumber = r.PRNumber
			s.prTaskMap[prKey(r.Repo, r.PRNumber)] = r.TaskID
		}
		s.tasks[r.TaskID] = task
	}

	if err := s.persistLocked(); err != nil {
		return Run{}, err
	}
	return r, nil
}

func (s *Store) SetRunStatus(runID string, status RunStatus, errText string) (Run, error) {
	return s.UpdateRun(runID, func(r *Run) error {
		now := time.Now().UTC()
		r.Status = status
		if status == StatusRunning {
			r.StartedAt = &now
		}
		if status == StatusSucceeded || status == StatusFailed || status == StatusCanceled || status == StatusAwaitingFeedback {
			r.CompletedAt = &now
		}
		r.Error = errText
		return nil
	})
}

func (s *Store) ActiveRunForTask(taskID string) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, id := range s.order {
		r, ok := s.runs[id]
		if !ok || r.TaskID != taskID {
			continue
		}
		if r.Status == StatusQueued || r.Status == StatusRunning {
			return r, true
		}
	}
	return Run{}, false
}

func (s *Store) LastRunForTask(taskID string) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, id := range s.order {
		r, ok := s.runs[id]
		if !ok || r.TaskID != taskID {
			continue
		}
		return r, true
	}
	return Run{}, false
}

func (s *Store) CancelQueuedRuns(taskID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	for id, r := range s.runs {
		if r.TaskID != taskID || r.Status != StatusQueued {
			continue
		}
		r.Status = StatusCanceled
		r.Error = reason
		r.UpdatedAt = now
		r.CompletedAt = &now
		s.runs[id] = r
	}
	return s.persistLocked()
}

// DeliverySeen returns true if the delivery was already processed.
func (s *Store) DeliverySeen(deliveryID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if deliveryID == "" {
		return false
	}
	_, ok := s.deliveries[deliveryID]
	return ok
}

// RecordDelivery stores a processed delivery id.
func (s *Store) RecordDelivery(deliveryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if deliveryID == "" {
		return nil
	}
	if _, ok := s.deliveries[deliveryID]; ok {
		return nil
	}

	s.deliveries[deliveryID] = time.Now().UTC()
	s.compactDeliveriesLocked()
	return s.persistLocked()
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
	for _, t := range p.Tasks {
		s.tasks[t.ID] = t
	}
	if p.Deliveries != nil {
		s.deliveries = p.Deliveries
	}
	if p.PRTaskMap != nil {
		s.prTaskMap = p.PRTaskMap
	}

	s.compactRunsLocked()
	s.compactDeliveriesLocked()
	return nil
}

func (s *Store) persistLocked() error {
	p := persistentState{
		Runs:       make([]Run, 0, len(s.order)),
		Tasks:      make([]Task, 0, len(s.tasks)),
		Deliveries: s.deliveries,
		PRTaskMap:  s.prTaskMap,
	}
	for _, id := range s.order {
		if r, ok := s.runs[id]; ok {
			p.Runs = append(p.Runs, r)
		}
	}
	for _, t := range s.tasks {
		p.Tasks = append(p.Tasks, t)
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

func prKey(repo string, prNumber int) string {
	return fmt.Sprintf("%s#%d", repo, prNumber)
}
