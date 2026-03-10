package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rtzll/rascal/internal/api"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/repositories"
	rt "github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

func (s *Server) legacyRepoFallbackAllowed() (bool, error) {
	count, err := s.Store.CountRepositories()
	if err != nil {
		return false, fmt.Errorf("count repositories: %w", err)
	}
	return count == 0, nil
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
	return state.NormalizeRepo(envelope.Repository.FullName)
}

func mapWebhookTriggerAllowed(cfg repositories.ResolvedRepoConfig, trigger string) bool {
	switch strings.TrimSpace(trigger) {
	case "issue_label":
		return cfg.AllowedWebhookTriggers.IssueLabel
	case "issue_edited":
		return cfg.AllowedWebhookTriggers.IssueEdit
	case "pr_comment":
		return cfg.AllowedWebhookTriggers.PRComment
	case "pr_review":
		return cfg.AllowedWebhookTriggers.PRReview
	case "pr_review_comment":
		return cfg.AllowedWebhookTriggers.PRReviewComment
	default:
		return true
	}
}

func webhookTriggerAllowed(eventType string, payload []byte, cfg repositories.ResolvedRepoConfig) (bool, bool, error) {
	switch eventType {
	case "issues":
		var ev ghapi.IssuesEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode issues event: %w", err)
		}
		switch ev.Action {
		case "labeled":
			if !rt.IsRascalLabel(ev.Label.Name) {
				return true, false, nil
			}
			return mapWebhookTriggerAllowed(cfg, "issue_label"), true, nil
		case "edited":
			if !ghapi.IssueHasRascalLabel(ev.Issue.Labels) {
				return true, false, nil
			}
			return mapWebhookTriggerAllowed(cfg, "issue_edited"), true, nil
		default:
			return true, false, nil
		}
	case "issue_comment":
		var ev ghapi.IssueCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode issue_comment event: %w", err)
		}
		if ev.Issue.PullRequest == nil {
			return true, false, nil
		}
		switch ev.Action {
		case "created":
			return mapWebhookTriggerAllowed(cfg, "pr_comment"), true, nil
		case "edited":
			if !ghapi.IssueCommentBodyChanged(ev) {
				return true, false, nil
			}
			return mapWebhookTriggerAllowed(cfg, "pr_comment"), true, nil
		default:
			return true, false, nil
		}
	case "pull_request_review":
		var ev ghapi.PullRequestReviewEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode pull_request_review event: %w", err)
		}
		if ev.Action != "submitted" {
			return true, false, nil
		}
		return mapWebhookTriggerAllowed(cfg, "pr_review"), true, nil
	case "pull_request_review_comment":
		var ev ghapi.PullRequestReviewCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode pull_request_review_comment event: %w", err)
		}
		switch ev.Action {
		case "created":
			return mapWebhookTriggerAllowed(cfg, "pr_review_comment"), true, nil
		case "edited":
			if !ghapi.ReviewCommentBodyChanged(ev) {
				return true, false, nil
			}
			return mapWebhookTriggerAllowed(cfg, "pr_review_comment"), true, nil
		default:
			return true, false, nil
		}
	case "pull_request_review_thread":
		var ev ghapi.PullRequestReviewThreadEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode pull_request_review_thread event: %w", err)
		}
		if ev.Action != "unresolved" {
			return true, false, nil
		}
		return mapWebhookTriggerAllowed(cfg, "pr_review_comment"), true, nil
	default:
		return true, false, nil
	}
}

func (s *Server) scopedWebhookDelivery(w http.ResponseWriter, r *http.Request, webhookSecret string) ([]byte, state.DeliveryClaim, bool) {
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return nil, state.DeliveryClaim{}, false
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	if !ghapi.VerifySignatureSHA256([]byte(webhookSecret), payload, sig) {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return nil, state.DeliveryClaim{}, false
	}
	deliveryID := ghapi.DeliveryID(r.Header)
	var claim state.DeliveryClaim
	if deliveryID != "" {
		c, claimed, claimErr := s.Store.ClaimDelivery(deliveryID, s.InstanceID)
		if claimErr != nil {
			http.Error(w, "failed to claim delivery id", http.StatusInternalServerError)
			return nil, state.DeliveryClaim{}, false
		}
		if !claimed {
			writeJSON(w, http.StatusOK, api.AcceptedResponse{Duplicate: true})
			return nil, state.DeliveryClaim{}, false
		}
		claim = c
	}
	return payload, claim, true
}

func (s *Server) resolveLegacyWebhookPolicy(payload []byte) (*repositories.ResolvedRepoConfig, int, error) {
	legacyAllowed, err := s.legacyRepoFallbackAllowed()
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("load repository policy: %w", err)
	}
	if legacyAllowed {
		return nil, 0, nil
	}
	if s.RepositoryResolver == nil {
		return nil, http.StatusAccepted, nil
	}
	fullName := payloadRepositoryFullName(payload)
	if fullName == "" {
		return nil, http.StatusBadRequest, nil
	}
	resolved, err := s.RepositoryResolver.Resolve(fullName)
	if err != nil {
		if errors.Is(err, repositories.ErrNotFound) {
			return nil, http.StatusAccepted, nil
		}
		return nil, http.StatusInternalServerError, fmt.Errorf("resolve repository %q: %w", fullName, err)
	}
	return &resolved, 0, nil
}

func (s *Server) HandleWebhookScoped(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isDraining() {
		http.Error(w, "server is draining", http.StatusServiceUnavailable)
		return
	}
	if !s.isActiveWebhookSlot() {
		accepted := false
		writeJSON(w, http.StatusAccepted, api.AcceptedResponse{Accepted: &accepted, InactiveSlot: true})
		return
	}

	webhookKey := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/webhooks/github/"), "/")
	if webhookKey == "" || strings.Contains(webhookKey, "/") {
		http.Error(w, "invalid webhook key", http.StatusBadRequest)
		return
	}
	if s.RepositoryResolver == nil {
		http.Error(w, "repository registry unavailable", http.StatusNotFound)
		return
	}
	resolved, err := s.RepositoryResolver.ResolveByWebhookKey(webhookKey)
	if err != nil {
		if errors.Is(err, repositories.ErrNotFound) {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to resolve repository", http.StatusInternalServerError)
		return
	}

	payload, deliveryClaim, ok := s.scopedWebhookDelivery(w, r, resolved.WebhookSecret)
	if !ok {
		return
	}
	if payloadRepositoryFullName(payload) != resolved.FullName {
		if deliveryClaim.ID != "" {
			s.releaseDeliveryClaimBestEffort(deliveryClaim)
		}
		http.Error(w, "repository mismatch", http.StatusBadRequest)
		return
	}

	accepted := false
	if !resolved.Enabled {
		if deliveryClaim.ID != "" {
			if err := s.Store.CompleteDeliveryClaim(deliveryClaim); err != nil {
				http.Error(w, "failed to finalize delivery id", http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusAccepted, api.AcceptedResponse{Accepted: &accepted})
		return
	}

	eventType := ghapi.EventType(r.Header)
	allowed, relevant, err := webhookTriggerAllowed(eventType, payload, resolved)
	if err != nil {
		if deliveryClaim.ID != "" {
			s.releaseDeliveryClaimBestEffort(deliveryClaim)
		}
		http.Error(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}
	if relevant && !allowed {
		if deliveryClaim.ID != "" {
			if err := s.Store.CompleteDeliveryClaim(deliveryClaim); err != nil {
				http.Error(w, "failed to finalize delivery id", http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusAccepted, api.AcceptedResponse{Accepted: &accepted})
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

	accepted = true
	writeJSON(w, http.StatusAccepted, api.AcceptedResponse{Accepted: &accepted})
}
