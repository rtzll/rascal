package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

type createRepositoryRequest struct {
	FullName      string `json:"full_name"`
	WebhookSecret string `json:"webhook_secret"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

type patchRepositoryRequest struct {
	Enabled       *bool   `json:"enabled,omitempty"`
	WebhookSecret *string `json:"webhook_secret,omitempty"`
}

func (s *Server) HandleRepositories(w http.ResponseWriter, r *http.Request) {
	if !requesterIsAdmin(r.Context()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		repos, err := s.Store.ListRepositories()
		if err != nil {
			http.Error(w, "failed to list repositories", http.StatusInternalServerError)
			return
		}
		out := make([]map[string]any, 0, len(repos))
		for _, repo := range repos {
			out = append(out, repositoryResponse(repo))
		}
		writeJSON(w, http.StatusOK, map[string]any{"repositories": out})
	case http.MethodPost:
		if s.Cipher == nil {
			http.Error(w, "repository registry unavailable", http.StatusServiceUnavailable)
			return
		}
		var req createRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		fullName := state.NormalizeRepo(req.FullName)
		if !isValidRepoFullName(fullName) {
			http.Error(w, "invalid full_name (expected owner/repo)", http.StatusBadRequest)
			return
		}
		webhookSecret := strings.TrimSpace(req.WebhookSecret)
		if webhookSecret == "" {
			http.Error(w, "webhook_secret is required", http.StatusBadRequest)
			return
		}
		encryptedSecret, err := s.Cipher.Encrypt([]byte(webhookSecret))
		if err != nil {
			http.Error(w, "failed to encrypt webhook secret", http.StatusInternalServerError)
			return
		}

		for attempt := 0; attempt < 5; attempt++ {
			webhookKey, err := newWebhookKey()
			if err != nil {
				http.Error(w, "failed to generate webhook key", http.StatusInternalServerError)
				return
			}
			repo, err := s.Store.CreateRepository(state.CreateRepositoryInput{
				FullName:               fullName,
				WebhookKey:             webhookKey,
				Enabled:                boolOrDefault(req.Enabled, true),
				EncryptedWebhookSecret: encryptedSecret,
			})
			if err == nil {
				writeJSON(w, http.StatusCreated, map[string]any{"repository": repositoryResponse(repo)})
				return
			}
			if strings.Contains(err.Error(), "UNIQUE constraint failed: repositories.webhook_key") {
				continue
			}
			if strings.Contains(err.Error(), "UNIQUE constraint failed: repositories.full_name") {
				http.Error(w, "repository already exists", http.StatusConflict)
				return
			}
			http.Error(w, "failed to create repository", http.StatusInternalServerError)
			return
		}
		http.Error(w, "failed to create repository", http.StatusInternalServerError)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) HandleRepositorySubresources(w http.ResponseWriter, r *http.Request) {
	if !requesterIsAdmin(r.Context()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/repositories/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Error(w, "repository is required", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.Error(w, "repository is required", http.StatusBadRequest)
		return
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid owner", http.StatusBadRequest)
		return
	}
	name, err := url.PathUnescape(parts[1])
	if err != nil {
		http.Error(w, "invalid repo", http.StatusBadRequest)
		return
	}
	fullName := state.NormalizeRepo(owner + "/" + name)
	if !isValidRepoFullName(fullName) {
		http.Error(w, "invalid repository", http.StatusBadRequest)
		return
	}
	s.handleRepositoryResource(w, r, fullName)
}

func (s *Server) handleRepositoryResource(w http.ResponseWriter, r *http.Request, fullName string) {
	switch r.Method {
	case http.MethodGet:
		repo, ok, err := s.Store.GetRepository(fullName)
		if err != nil {
			http.Error(w, "failed to load repository", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"repository": repositoryResponse(repo)})
	case http.MethodPatch:
		if s.Cipher == nil {
			http.Error(w, "repository registry unavailable", http.StatusServiceUnavailable)
			return
		}
		var req patchRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		var encryptedSecret []byte
		if req.WebhookSecret != nil {
			secret := strings.TrimSpace(*req.WebhookSecret)
			if secret == "" {
				http.Error(w, "webhook_secret cannot be empty", http.StatusBadRequest)
				return
			}
			var err error
			encryptedSecret, err = s.Cipher.Encrypt([]byte(secret))
			if err != nil {
				http.Error(w, "failed to encrypt webhook secret", http.StatusInternalServerError)
				return
			}
		}
		repo, err := s.Store.UpdateRepository(state.UpdateRepositoryInput{
			FullName:               fullName,
			Enabled:                req.Enabled,
			EncryptedWebhookSecret: encryptedSecret,
		})
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, "repository not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to update repository", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"repository": repositoryResponse(repo)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) HandleWebhookScoped(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Cipher == nil {
		http.Error(w, "repository registry unavailable", http.StatusNotFound)
		return
	}
	if s.isDraining() {
		http.Error(w, "server is draining", http.StatusServiceUnavailable)
		return
	}
	if !s.isActiveWebhookSlot() {
		accepted := false
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": accepted, "inactive_slot": true})
		return
	}

	webhookKey := strings.TrimPrefix(r.URL.Path, "/v1/webhooks/github/")
	webhookKey = strings.Trim(webhookKey, "/")
	if webhookKey == "" || strings.Contains(webhookKey, "/") {
		http.Error(w, "invalid webhook key", http.StatusBadRequest)
		return
	}
	repo, ok, err := s.Store.GetRepositoryByWebhookKey(webhookKey)
	if err != nil {
		http.Error(w, "failed to resolve repository", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}

	webhookSecretRaw, err := s.Cipher.Decrypt(repo.EncryptedWebhookSecret)
	if err != nil {
		http.Error(w, "failed to decrypt webhook secret", http.StatusInternalServerError)
		return
	}
	payload, deliveryClaim, ok := s.scopedWebhookDelivery(w, r, strings.TrimSpace(string(webhookSecretRaw)))
	if !ok {
		return
	}
	if fullName := payloadRepositoryFullName(payload); fullName == "" || state.NormalizeRepo(fullName) != repo.FullName {
		if deliveryClaim.ID != "" {
			s.releaseDeliveryClaimBestEffort(deliveryClaim)
		}
		http.Error(w, "repository mismatch", http.StatusBadRequest)
		return
	}

	eventType := github.EventType(r.Header)
	admissionEvent, err := scopedWebhookAdmissionEvent(eventType, payload)
	if err != nil {
		if deliveryClaim.ID != "" {
			s.releaseDeliveryClaimBestEffort(deliveryClaim)
		}
		http.Error(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}
	if admissionEvent && !repo.Enabled {
		if deliveryClaim.ID != "" {
			if err := s.Store.CompleteDeliveryClaim(deliveryClaim); err != nil {
				http.Error(w, "failed to finalize delivery id", http.StatusInternalServerError)
				return
			}
		}
		accepted := false
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": accepted, "reason": "repository disabled"})
		return
	}

	if err := s.processWebhookEvent(r.Context(), eventType, payload); err != nil {
		if deliveryClaim.ID != "" {
			s.releaseDeliveryClaimBestEffort(deliveryClaim)
		}
		http.Error(w, "webhook processing failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if deliveryClaim.ID != "" {
		if err := s.Store.CompleteDeliveryClaim(deliveryClaim); err != nil {
			http.Error(w, "failed to finalize delivery id", http.StatusInternalServerError)
			return
		}
	}
	accepted := true
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": accepted})
}

func (s *Server) scopedWebhookDelivery(w http.ResponseWriter, r *http.Request, webhookSecret string) ([]byte, state.DeliveryClaim, bool) {
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return nil, state.DeliveryClaim{}, false
	}
	if !github.VerifySignatureSHA256([]byte(webhookSecret), payload, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return nil, state.DeliveryClaim{}, false
	}

	deliveryID := github.DeliveryID(r.Header)
	var claim state.DeliveryClaim
	if deliveryID != "" {
		c, claimed, err := s.Store.ClaimDelivery(deliveryID, s.InstanceID)
		if err != nil {
			http.Error(w, "failed to claim delivery id", http.StatusInternalServerError)
			return nil, state.DeliveryClaim{}, false
		}
		if !claimed {
			writeJSON(w, http.StatusOK, map[string]any{"duplicate": true})
			return nil, state.DeliveryClaim{}, false
		}
		claim = c
	}
	return payload, claim, true
}

func payloadRepositoryFullName(payload []byte) string {
	var envelope struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Repository.FullName)
}

func scopedWebhookAdmissionEvent(eventType string, payload []byte) (bool, error) {
	switch eventType {
	case "issues":
		var ev github.IssuesEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, fmt.Errorf("decode issues event: %w", err)
		}
		switch ev.Action {
		case "labeled":
			return runtime.IsRascalLabel(ev.Label.Name), nil
		case "edited", "reopened":
			return github.IssueHasRascalLabel(ev.Issue.Labels), nil
		default:
			return false, nil
		}
	case "issue_comment":
		var ev github.IssueCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, fmt.Errorf("decode issue_comment event: %w", err)
		}
		if ev.Issue.PullRequest == nil {
			return false, nil
		}
		switch ev.Action {
		case "created":
			return true, nil
		case "edited":
			return github.IssueCommentBodyChanged(ev), nil
		default:
			return false, nil
		}
	case "pull_request_review":
		var ev github.PullRequestReviewEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, fmt.Errorf("decode pull_request_review event: %w", err)
		}
		return ev.Action == "submitted", nil
	case "pull_request_review_comment":
		var ev github.PullRequestReviewCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, fmt.Errorf("decode pull_request_review_comment event: %w", err)
		}
		switch ev.Action {
		case "created":
			return true, nil
		case "edited":
			return github.ReviewCommentBodyChanged(ev), nil
		default:
			return false, nil
		}
	default:
		return false, nil
	}
}

func repositoryResponse(repo state.Repository) map[string]any {
	return map[string]any{
		"full_name":   repo.FullName,
		"webhook_key": repo.WebhookKey,
		"enabled":     repo.Enabled,
		"created_at":  repo.CreatedAt,
		"updated_at":  repo.UpdatedAt,
	}
}

func newWebhookKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate webhook key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func isValidRepoFullName(fullName string) bool {
	parts := strings.Split(strings.TrimSpace(fullName), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func boolOrDefault(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}
