package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

func TestCreateTaskRequestUnmarshalUsesLegacyTaskField(t *testing.T) {
	var req CreateTaskRequest
	if err := json.Unmarshal([]byte(`{"repo":"rtzll/rascal","task":"refactor tests","base_branch":"main"}`), &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if req.Instruction != "refactor tests" {
		t.Fatalf("Instruction = %q, want legacy task value", req.Instruction)
	}
	if req.Repo != "rtzll/rascal" || req.BaseBranch != "main" {
		t.Fatalf("request = %#v, want repo/base branch populated", req)
	}
}

func TestCreateTaskRequestUnmarshalPrefersInstructionField(t *testing.T) {
	var req CreateTaskRequest
	if err := json.Unmarshal([]byte(`{"repo":"rtzll/rascal","task":"legacy","instruction":"canonical","base_branch":"main"}`), &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if req.Instruction != "canonical" {
		t.Fatalf("Instruction = %q, want canonical field", req.Instruction)
	}
}

func TestCredentialFromState(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	cooldown := now.Add(15 * time.Minute)
	input := state.Credential{
		ID:            "cred_123",
		OwnerUserID:   "user_123",
		Scope:         state.CredentialScopeShared,
		Provider:      "codex",
		Weight:        7,
		Status:        state.CredentialStatusCooldown,
		CooldownUntil: &cooldown,
		LastError:     "rate limited",
		CreatedAt:     now,
		UpdatedAt:     now.Add(time.Minute),
	}

	got := CredentialFromState(input)
	if got.ID != input.ID || got.OwnerUserID != input.OwnerUserID || got.Scope != input.Scope || got.Provider != input.Provider {
		t.Fatalf("CredentialFromState() identity fields mismatch: %#v", got)
	}
	if got.Weight != input.Weight || got.Status != input.Status || got.LastError != input.LastError {
		t.Fatalf("CredentialFromState() state fields mismatch: %#v", got)
	}
	if got.CooldownUntil == nil || !got.CooldownUntil.Equal(cooldown) {
		t.Fatalf("CooldownUntil = %v, want %v", got.CooldownUntil, cooldown)
	}
	if !got.CreatedAt.Equal(input.CreatedAt) || !got.UpdatedAt.Equal(input.UpdatedAt) {
		t.Fatalf("timestamps mismatch: %#v", got)
	}
}
