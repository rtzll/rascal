package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

func (s *Store) CreateCampaign(in CreateCampaignInput) (Campaign, []CampaignItem, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Policy = NormalizeCampaignExecutionPolicy(in.Policy)
	if in.Name == "" {
		return Campaign{}, nil, fmt.Errorf("campaign name is required")
	}
	if len(in.Items) == 0 {
		return Campaign{}, nil, fmt.Errorf("at least one campaign item is required")
	}
	if in.ID == "" {
		id, err := NewCampaignID()
		if err != nil {
			return Campaign{}, nil, err
		}
		in.ID = id
	}

	now := time.Now().UTC()
	items := make([]CampaignItem, 0, len(in.Items))
	for i, raw := range in.Items {
		itemInput := NormalizeCampaignItemInput(raw)
		if itemInput.Repo == "" || itemInput.Task == "" {
			return Campaign{}, nil, fmt.Errorf("campaign item %d requires repo and task", i+1)
		}
		if itemInput.TaskID == "" {
			itemInput.TaskID = fmt.Sprintf("%s#item-%03d", in.ID, i+1)
		}
		items = append(items, CampaignItem{
			ID:              fmt.Sprintf("%s_item_%03d", in.ID, i+1),
			CampaignID:      in.ID,
			Order:           i + 1,
			Repo:            itemInput.Repo,
			Task:            itemInput.Task,
			TaskID:          itemInput.TaskID,
			BaseBranch:      itemInput.BaseBranch,
			BackendOverride: itemInput.BackendOverride,
			State:           CampaignItemStatePending,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Campaign{}, nil, fmt.Errorf("begin create campaign transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback create campaign transaction: %v", rollbackErr)
		}
	}()

	if _, err := tx.ExecContext(
		context.Background(),
		`INSERT INTO campaigns (
			id,
			name,
			description,
			state,
			max_concurrent,
			stop_after_failures,
			continue_on_failure,
			skip_if_open_pr,
			created_at,
			updated_at,
			started_at,
			completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		in.ID,
		in.Name,
		in.Description,
		string(CampaignStateDraft),
		in.Policy.MaxConcurrent,
		in.Policy.StopAfterFailures,
		boolToInt64(in.Policy.ContinueOnFailure),
		boolToInt64(in.Policy.SkipIfOpenPR),
		now.UnixNano(),
		now.UnixNano(),
	); err != nil {
		return Campaign{}, nil, fmt.Errorf("insert campaign %q: %w", in.ID, err)
	}

	for _, item := range items {
		if _, err := tx.ExecContext(
			context.Background(),
			`INSERT INTO campaign_items (
				id,
				campaign_id,
				item_order,
				repo,
				task,
				task_id,
				base_branch,
				backend_override,
				state,
				run_id,
				skip_reason,
				failure_reason,
				created_at,
				updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID,
			item.CampaignID,
			item.Order,
			item.Repo,
			item.Task,
			item.TaskID,
			item.BaseBranch,
			item.BackendOverride,
			string(item.State),
			item.RunID,
			item.SkipReason,
			item.FailureReason,
			item.CreatedAt.UnixNano(),
			item.UpdatedAt.UnixNano(),
		); err != nil {
			return Campaign{}, nil, fmt.Errorf("insert campaign item %q: %w", item.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Campaign{}, nil, fmt.Errorf("commit create campaign %q: %w", in.ID, err)
	}

	campaign := Campaign{
		ID:          in.ID,
		Name:        in.Name,
		Description: in.Description,
		State:       CampaignStateDraft,
		Policy:      in.Policy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	return campaign, items, nil
}

func (s *Store) GetCampaign(id string) (Campaign, bool, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT
	id,
	name,
	description,
	state,
	max_concurrent,
	stop_after_failures,
	continue_on_failure,
	skip_if_open_pr,
	created_at,
	updated_at,
	started_at,
	completed_at
FROM campaigns
WHERE id = ?`, strings.TrimSpace(id))
	campaign, err := scanCampaign(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Campaign{}, false, nil
		}
		return Campaign{}, false, fmt.Errorf("get campaign %q: %w", id, err)
	}
	return campaign, true, nil
}

func (s *Store) ListCampaigns() ([]Campaign, error) {
	rows, err := s.db.QueryContext(context.Background(), `
SELECT
	id,
	name,
	description,
	state,
	max_concurrent,
	stop_after_failures,
	continue_on_failure,
	skip_if_open_pr,
	created_at,
	updated_at,
	started_at,
	completed_at
FROM campaigns
ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("close campaign rows: %v", closeErr)
		}
	}()

	var out []Campaign
	for rows.Next() {
		campaign, err := scanCampaign(rows)
		if err != nil {
			return nil, fmt.Errorf("scan campaign: %w", err)
		}
		out = append(out, campaign)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate campaigns: %w", err)
	}
	return out, nil
}

func (s *Store) ListCampaignItems(campaignID string) ([]CampaignItem, error) {
	rows, err := s.db.QueryContext(context.Background(), `
SELECT
	id,
	campaign_id,
	item_order,
	repo,
	task,
	task_id,
	base_branch,
	backend_override,
	state,
	run_id,
	skip_reason,
	failure_reason,
	created_at,
	updated_at
FROM campaign_items
WHERE campaign_id = ?
ORDER BY item_order ASC, id ASC`, strings.TrimSpace(campaignID))
	if err != nil {
		return nil, fmt.Errorf("list campaign items for %q: %w", campaignID, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("close campaign item rows for %s: %v", campaignID, closeErr)
		}
	}()

	var out []CampaignItem
	for rows.Next() {
		item, err := scanCampaignItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan campaign item: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate campaign items for %q: %w", campaignID, err)
	}
	return out, nil
}

func (s *Store) UpdateCampaign(id string, fn func(*Campaign) error) (Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Campaign{}, fmt.Errorf("begin update campaign transaction for %q: %w", id, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback update campaign transaction: %v", rollbackErr)
		}
	}()

	row := tx.QueryRowContext(context.Background(), `
SELECT
	id,
	name,
	description,
	state,
	max_concurrent,
	stop_after_failures,
	continue_on_failure,
	skip_if_open_pr,
	created_at,
	updated_at,
	started_at,
	completed_at
FROM campaigns
WHERE id = ?`, strings.TrimSpace(id))
	campaign, err := scanCampaign(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Campaign{}, fmt.Errorf("campaign %q not found", id)
		}
		return Campaign{}, fmt.Errorf("load campaign %q for update: %w", id, err)
	}

	prevState := campaign.State
	if err := fn(&campaign); err != nil {
		return Campaign{}, fmt.Errorf("apply campaign update for %q: %w", id, err)
	}
	campaign.Policy = NormalizeCampaignExecutionPolicy(campaign.Policy)
	if err := ValidateCampaignStateTransition(prevState, campaign.State); err != nil {
		return Campaign{}, fmt.Errorf("validate campaign state transition for %q: %w", id, err)
	}
	campaign.UpdatedAt = time.Now().UTC()

	if _, err := tx.ExecContext(
		context.Background(),
		`UPDATE campaigns
SET
	name = ?,
	description = ?,
	state = ?,
	max_concurrent = ?,
	stop_after_failures = ?,
	continue_on_failure = ?,
	skip_if_open_pr = ?,
	updated_at = ?,
	started_at = ?,
	completed_at = ?
WHERE id = ?`,
		campaign.Name,
		campaign.Description,
		string(campaign.State),
		campaign.Policy.MaxConcurrent,
		campaign.Policy.StopAfterFailures,
		boolToInt64(campaign.Policy.ContinueOnFailure),
		boolToInt64(campaign.Policy.SkipIfOpenPR),
		campaign.UpdatedAt.UnixNano(),
		toNullInt64Time(campaign.StartedAt),
		toNullInt64Time(campaign.CompletedAt),
		campaign.ID,
	); err != nil {
		return Campaign{}, fmt.Errorf("update campaign %q: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return Campaign{}, fmt.Errorf("commit update campaign transaction for %q: %w", id, err)
	}
	return campaign, nil
}

func (s *Store) UpdateCampaignItem(id string, fn func(*CampaignItem) error) (CampaignItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return CampaignItem{}, fmt.Errorf("begin update campaign item transaction for %q: %w", id, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback update campaign item transaction: %v", rollbackErr)
		}
	}()

	row := tx.QueryRowContext(context.Background(), `
SELECT
	id,
	campaign_id,
	item_order,
	repo,
	task,
	task_id,
	base_branch,
	backend_override,
	state,
	run_id,
	skip_reason,
	failure_reason,
	created_at,
	updated_at
FROM campaign_items
WHERE id = ?`, strings.TrimSpace(id))
	item, err := scanCampaignItem(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CampaignItem{}, fmt.Errorf("campaign item %q not found", id)
		}
		return CampaignItem{}, fmt.Errorf("load campaign item %q for update: %w", id, err)
	}

	prevState := item.State
	if err := fn(&item); err != nil {
		return CampaignItem{}, fmt.Errorf("apply campaign item update for %q: %w", id, err)
	}
	if err := ValidateCampaignItemStateTransition(prevState, item.State); err != nil {
		return CampaignItem{}, fmt.Errorf("validate campaign item state transition for %q: %w", id, err)
	}
	item.UpdatedAt = time.Now().UTC()

	if _, err := tx.ExecContext(
		context.Background(),
		`UPDATE campaign_items
SET
	repo = ?,
	task = ?,
	task_id = ?,
	base_branch = ?,
	backend_override = ?,
	state = ?,
	run_id = ?,
	skip_reason = ?,
	failure_reason = ?,
	updated_at = ?
WHERE id = ?`,
		item.Repo,
		item.Task,
		item.TaskID,
		item.BaseBranch,
		item.BackendOverride,
		string(item.State),
		item.RunID,
		item.SkipReason,
		item.FailureReason,
		item.UpdatedAt.UnixNano(),
		item.ID,
	); err != nil {
		return CampaignItem{}, fmt.Errorf("update campaign item %q: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return CampaignItem{}, fmt.Errorf("commit update campaign item transaction for %q: %w", id, err)
	}
	return item, nil
}

type campaignScanner interface {
	Scan(dest ...any) error
}

func scanCampaign(scanner campaignScanner) (Campaign, error) {
	var (
		campaign             Campaign
		state                string
		continueOnFailureInt int64
		skipIfOpenPRInt      int64
		createdAt            int64
		updatedAt            int64
		startedAt            sql.NullInt64
		completedAt          sql.NullInt64
	)
	if err := scanner.Scan(
		&campaign.ID,
		&campaign.Name,
		&campaign.Description,
		&state,
		&campaign.Policy.MaxConcurrent,
		&campaign.Policy.StopAfterFailures,
		&continueOnFailureInt,
		&skipIfOpenPRInt,
		&createdAt,
		&updatedAt,
		&startedAt,
		&completedAt,
	); err != nil {
		return Campaign{}, fmt.Errorf("scan campaign: %w", err)
	}
	campaign.State = CampaignState(state)
	campaign.Policy.ContinueOnFailure = continueOnFailureInt != 0
	campaign.Policy.SkipIfOpenPR = skipIfOpenPRInt != 0
	campaign.Policy = NormalizeCampaignExecutionPolicy(campaign.Policy)
	campaign.CreatedAt = time.Unix(0, createdAt).UTC()
	campaign.UpdatedAt = time.Unix(0, updatedAt).UTC()
	campaign.StartedAt = fromNullInt64Time(startedAt)
	campaign.CompletedAt = fromNullInt64Time(completedAt)
	return campaign, nil
}

func scanCampaignItem(scanner campaignScanner) (CampaignItem, error) {
	var (
		item      CampaignItem
		state     string
		createdAt int64
		updatedAt int64
	)
	if err := scanner.Scan(
		&item.ID,
		&item.CampaignID,
		&item.Order,
		&item.Repo,
		&item.Task,
		&item.TaskID,
		&item.BaseBranch,
		&item.BackendOverride,
		&state,
		&item.RunID,
		&item.SkipReason,
		&item.FailureReason,
		&createdAt,
		&updatedAt,
	); err != nil {
		return CampaignItem{}, fmt.Errorf("scan campaign item: %w", err)
	}
	item.State = CampaignItemState(state)
	item.CreatedAt = time.Unix(0, createdAt).UTC()
	item.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return item, nil
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func fromNullInt64Time(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	ts := time.Unix(0, v.Int64).UTC()
	return &ts
}

func toNullInt64Time(v *time.Time) sql.NullInt64 {
	if v == nil || v.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v.UTC().UnixNano(), Valid: true}
}
