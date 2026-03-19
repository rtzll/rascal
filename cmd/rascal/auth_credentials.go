package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/state"
	"github.com/spf13/cobra"
)

type credentialRecord = api.Credential
type credentialListResponse = api.CredentialListResponse
type credentialGetResponse = api.CredentialResponse
type credentialDisableResponse = api.CredentialDisabledResponse
type credentialCreateRequest = api.CreateCredentialRequest
type credentialUpdateRequest = api.UpdateCredentialRequest

const bootstrapSharedCredentialID = "cred_bootstrap_shared"

func (a *app) newAuthCredentialsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "Manage stored credentials on rascald",
		Long:  "List, inspect, create, update, and change state for stored credentials managed by rascald.",
		Example: strings.TrimSpace(`
rascal auth credentials list
rascal auth credentials create --auth-file ~/.codex/auth.json --scope personal --provider codex
rascal auth credentials create --auth-file oauth_token --scope shared --provider anthropic
rascal auth credentials disable cred_123
rascal auth credentials cooldown cred_123 --for 30m --reason "upstream auth failures"
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(a.newAuthCredentialsListCmd())
	cmd.AddCommand(a.newAuthCredentialsGetCmd())
	cmd.AddCommand(a.newAuthCredentialsCreateCmd())
	cmd.AddCommand(a.newAuthCredentialsUpdateCmd())
	cmd.AddCommand(a.newAuthCredentialsDisableCmd())
	cmd.AddCommand(a.newAuthCredentialsEnableCmd())
	cmd.AddCommand(a.newAuthCredentialsCooldownCmd())
	return cmd
}

func (a *app) newAuthCredentialsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored credentials visible to the current principal",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			creds, err := a.listCredentials()
			if err != nil {
				return err
			}
			out := credentialListResponse{Credentials: creds}
			return emit(a, out, func() error {
				return renderCredentialListTable(creds)
			})
		},
	}
}

func (a *app) newAuthCredentialsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <credential-id>",
		Short: "Show one stored credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			cred, err := a.getCredential(args[0])
			if err != nil {
				return err
			}
			out := credentialGetResponse{Credential: cred}
			return emit(a, out, func() error {
				return renderCredentialDetailTable(cred)
			})
		},
	}
}

func (a *app) newAuthCredentialsCreateCmd() *cobra.Command {
	var (
		id          string
		scope       string
		provider    string
		runtime     string
		ownerUserID string
		weight      int
		authFile    string
		authBlob    string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a stored credential",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			resolvedScope, err := normalizeCredentialScope(scope)
			if err != nil {
				return err
			}
			resolvedProvider, err := resolveCredentialProvider(cmd, provider, runtime, true)
			if err != nil {
				return err
			}
			resolvedAuthBlob, err := resolveCredentialAuthBlob(authFile, authBlob, true)
			if err != nil {
				return err
			}
			cred, err := a.createCredential(credentialCreateRequest{
				ID:          strings.TrimSpace(id),
				OwnerUserID: strings.TrimSpace(ownerUserID),
				Scope:       resolvedScope,
				Provider:    resolvedProvider,
				AuthBlob:    resolvedAuthBlob,
				Weight:      weight,
			})
			if err != nil {
				return err
			}
			out := credentialGetResponse{Credential: cred}
			return emit(a, out, func() error {
				return renderCredentialDetailTable(cred)
			})
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "credential id (auto-generated when empty)")
	cmd.Flags().StringVar(&scope, "scope", "personal", "credential scope: personal|shared")
	cmd.Flags().StringVar(&provider, "provider", "", "credential provider: codex|anthropic (default: codex)")
	cmd.Flags().StringVar(&runtime, "runtime", "", "deprecated alias for --provider")
	if err := cmd.Flags().MarkDeprecated("runtime", "use --provider instead"); err != nil {
		panic(err)
	}
	cmd.Flags().StringVar(&ownerUserID, "owner-user-id", "", "owner user id for personal credentials (admin only)")
	cmd.Flags().IntVar(&weight, "weight", 1, "selection weight")
	cmd.Flags().StringVar(&authFile, "auth-file", "", "path to auth payload file to store")
	cmd.Flags().StringVar(&authBlob, "auth-blob", "", "raw auth payload to store")
	return cmd
}

func (a *app) newAuthCredentialsUpdateCmd() *cobra.Command {
	var (
		scope       string
		provider    string
		runtime     string
		ownerUserID string
		weight      int
		authFile    string
		authBlob    string
	)
	cmd := &cobra.Command{
		Use:   "update <credential-id>",
		Short: "Update metadata or auth material for a stored credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			var req credentialUpdateRequest
			changed := false
			if cmd.Flags().Changed("scope") {
				resolvedScope, err := normalizeCredentialScope(scope)
				if err != nil {
					return err
				}
				value := resolvedScope
				req.Scope = &value
				changed = true
			}
			resolvedProvider, err := resolveCredentialProvider(cmd, provider, runtime, false)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("provider") || cmd.Flags().Changed("runtime") {
				value := resolvedProvider
				req.Provider = &value
				changed = true
			}
			if cmd.Flags().Changed("owner-user-id") {
				value := strings.TrimSpace(ownerUserID)
				req.OwnerUserID = &value
				changed = true
			}
			if cmd.Flags().Changed("weight") {
				if weight <= 0 {
					return &cliError{Code: exitInput, Message: "--weight must be positive"}
				}
				value := weight
				req.Weight = &value
				changed = true
			}
			if cmd.Flags().Changed("auth-file") || cmd.Flags().Changed("auth-blob") {
				value, err := resolveCredentialAuthBlob(authFile, authBlob, true)
				if err != nil {
					return err
				}
				req.AuthBlob = &value
				changed = true
			}
			if !changed {
				return &cliError{Code: exitInput, Message: "no credential fields to update"}
			}
			cred, err := a.updateCredential(args[0], req)
			if err != nil {
				return err
			}
			out := credentialGetResponse{Credential: cred}
			return emit(a, out, func() error {
				return renderCredentialDetailTable(cred)
			})
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "credential scope: personal|shared")
	cmd.Flags().StringVar(&provider, "provider", "", "credential provider: codex|anthropic")
	cmd.Flags().StringVar(&runtime, "runtime", "", "deprecated alias for --provider")
	if err := cmd.Flags().MarkDeprecated("runtime", "use --provider instead"); err != nil {
		panic(err)
	}
	cmd.Flags().StringVar(&ownerUserID, "owner-user-id", "", "owner user id for personal credentials (admin only)")
	cmd.Flags().IntVar(&weight, "weight", 0, "selection weight")
	cmd.Flags().StringVar(&authFile, "auth-file", "", "path to replacement auth payload file")
	cmd.Flags().StringVar(&authBlob, "auth-blob", "", "replacement raw auth payload")
	return cmd
}

func (a *app) newAuthCredentialsDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <credential-id>",
		Short: "Disable a stored credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			cred, err := a.disableCredential(args[0])
			if err != nil {
				return err
			}
			out := credentialDisableResponse{Disabled: true, Credential: &cred}
			return emit(a, out, func() error {
				return renderCredentialDetailTable(cred)
			})
		},
	}
}

func (a *app) newAuthCredentialsEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <credential-id>",
		Short: "Enable a stored credential and clear manual cooldown state",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			status := state.CredentialStatusActive
			clearCooldown := ""
			clearError := ""
			cred, err := a.updateCredential(args[0], credentialUpdateRequest{
				Status:        &status,
				CooldownUntil: &clearCooldown,
				LastError:     &clearError,
			})
			if err != nil {
				return err
			}
			out := credentialGetResponse{Credential: cred}
			return emit(a, out, func() error {
				return renderCredentialDetailTable(cred)
			})
		},
	}
}

func (a *app) newAuthCredentialsCooldownCmd() *cobra.Command {
	var (
		cooldownFor time.Duration
		reason      string
		clear       bool
	)
	cmd := &cobra.Command{
		Use:   "cooldown <credential-id>",
		Short: "Set or clear credential cooldown state",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			switch {
			case clear && cooldownFor > 0:
				return &cliError{Code: exitInput, Message: "--clear cannot be combined with --for"}
			case !clear && cooldownFor <= 0:
				return &cliError{Code: exitInput, Message: "set --for or use --clear"}
			}
			req := credentialUpdateRequest{}
			if clear {
				status := state.CredentialStatusActive
				cooldownUntil := ""
				lastError := ""
				req.Status = &status
				req.CooldownUntil = &cooldownUntil
				req.LastError = &lastError
			} else {
				status := state.CredentialStatusCooldown
				until := time.Now().UTC().Add(cooldownFor).Format(time.RFC3339)
				req.Status = &status
				req.CooldownUntil = &until
				text := strings.TrimSpace(reason)
				if text == "" {
					text = "manual cooldown"
				}
				req.LastError = &text
			}
			cred, err := a.updateCredential(args[0], req)
			if err != nil {
				return err
			}
			out := credentialGetResponse{Credential: cred}
			return emit(a, out, func() error {
				return renderCredentialDetailTable(cred)
			})
		},
	}
	cmd.Flags().DurationVar(&cooldownFor, "for", 0, "cooldown duration")
	cmd.Flags().StringVar(&reason, "reason", "", "cooldown reason")
	cmd.Flags().BoolVar(&clear, "clear", false, "clear cooldown and reactivate the credential")
	return cmd
}

func (a *app) listCredentials() ([]credentialRecord, error) {
	return listCredentialsWithClient(a.client)
}

func (a *app) getCredential(id string) (credentialRecord, error) {
	cred, _, err := getCredentialWithClient(a.client, id)
	return cred, err
}

func (a *app) createCredential(req credentialCreateRequest) (credentialRecord, error) {
	return createCredentialWithClient(a.client, req)
}

func (a *app) updateCredential(id string, req credentialUpdateRequest) (credentialRecord, error) {
	return updateCredentialWithClient(a.client, id, req)
}

func (a *app) disableCredential(id string) (credentialRecord, error) {
	resp, err := a.client.do(http.MethodDelete, "/v1/credentials/"+url.PathEscape(strings.TrimSpace(id)), nil)
	if err != nil {
		return credentialRecord{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close disable credential response body", resp.Body)
	if resp.StatusCode >= 300 {
		return credentialRecord{}, decodeServerError(resp)
	}
	return a.getCredential(id)
}

func listCredentialsWithClient(client apiClient) ([]credentialRecord, error) {
	resp, err := client.do(http.MethodGet, "/v1/credentials", nil)
	if err != nil {
		return nil, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close list credentials response body", resp.Body)
	if resp.StatusCode >= 300 {
		return nil, decodeServerError(resp)
	}
	var out credentialListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Credentials, nil
}

func getCredentialWithClient(client apiClient, id string) (credentialRecord, bool, error) {
	resp, err := client.do(http.MethodGet, "/v1/credentials/"+url.PathEscape(strings.TrimSpace(id)), nil)
	if err != nil {
		return credentialRecord{}, false, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close get credential response body", resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return credentialRecord{}, false, nil
	}
	if resp.StatusCode >= 300 {
		return credentialRecord{}, false, decodeServerError(resp)
	}
	var out credentialGetResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return credentialRecord{}, false, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Credential, true, nil
}

func createCredentialWithClient(client apiClient, req credentialCreateRequest) (credentialRecord, error) {
	resp, err := doJSON(client, http.MethodPost, "/v1/credentials", req)
	if err != nil {
		return credentialRecord{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close create credential response body", resp.Body)
	if resp.StatusCode >= 300 {
		return credentialRecord{}, decodeServerError(resp)
	}
	var out credentialGetResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return credentialRecord{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Credential, nil
}

func updateCredentialWithClient(client apiClient, id string, req credentialUpdateRequest) (credentialRecord, error) {
	resp, err := doJSON(client, http.MethodPatch, "/v1/credentials/"+url.PathEscape(strings.TrimSpace(id)), req)
	if err != nil {
		return credentialRecord{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close update credential response body", resp.Body)
	if resp.StatusCode >= 300 {
		return credentialRecord{}, decodeServerError(resp)
	}
	var out credentialGetResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return credentialRecord{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Credential, nil
}

func seedBootstrapSharedCredential(client apiClient, authFilePath string) (credentialRecord, error) {
	authBlob, err := resolveCredentialAuthBlob(authFilePath, "", true)
	if err != nil {
		return credentialRecord{}, err
	}

	if _, found, err := getCredentialWithClient(client, bootstrapSharedCredentialID); err != nil {
		return credentialRecord{}, err
	} else if !found {
		return createCredentialWithClient(client, credentialCreateRequest{
			ID:       bootstrapSharedCredentialID,
			Scope:    state.CredentialScopeShared,
			AuthBlob: authBlob,
			Weight:   1,
		})
	}

	scope := state.CredentialScopeShared
	status := state.CredentialStatusActive
	clearValue := ""
	return updateCredentialWithClient(client, bootstrapSharedCredentialID, credentialUpdateRequest{
		Scope:         &scope,
		AuthBlob:      &authBlob,
		Status:        &status,
		CooldownUntil: &clearValue,
		LastError:     &clearValue,
	})
}

func resolveCredentialAuthBlob(authFile, authBlob string, required bool) (string, error) {
	authFile = strings.TrimSpace(authFile)
	if authFile != "" && strings.TrimSpace(authBlob) != "" {
		return "", &cliError{Code: exitInput, Message: "--auth-file and --auth-blob cannot be combined"}
	}
	switch {
	case authFile != "":
		data, err := os.ReadFile(authFile)
		if err != nil {
			return "", &cliError{Code: exitInput, Message: "failed to read auth file", Cause: err}
		}
		value := strings.TrimSpace(string(data))
		if required && value == "" {
			return "", &cliError{Code: exitInput, Message: "auth payload is empty"}
		}
		return value, nil
	case strings.TrimSpace(authBlob) != "":
		return strings.TrimSpace(authBlob), nil
	case required:
		return "", &cliError{Code: exitInput, Message: "set --auth-file or --auth-blob"}
	default:
		return "", nil
	}
}

func normalizeCredentialScope(scope string) (state.CredentialScope, error) {
	resolved, ok := state.ParseCredentialScope(scope)
	if !ok {
		return "", &cliError{Code: exitInput, Message: "invalid credential scope", Hint: "use personal or shared"}
	}
	return resolved, nil
}

func resolveCredentialProvider(cmd *cobra.Command, provider, runtime string, defaultToCodex bool) (string, error) {
	resolvedProvider, err := normalizeCredentialProvider(provider)
	if err != nil {
		return "", err
	}
	resolvedRuntime, err := normalizeCredentialProvider(runtime)
	if err != nil {
		return "", err
	}
	providerChanged := cmd.Flags().Changed("provider")
	runtimeChanged := cmd.Flags().Changed("runtime")
	if providerChanged && runtimeChanged && resolvedProvider != resolvedRuntime {
		return "", &cliError{Code: exitInput, Message: "conflicting credential provider flags", Hint: "use --provider codex|anthropic"}
	}
	switch {
	case providerChanged:
		return resolvedProvider, nil
	case runtimeChanged:
		return resolvedRuntime, nil
	case defaultToCodex:
		return "codex", nil
	default:
		return "", nil
	}
}

func normalizeCredentialProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return "", nil
	case "codex":
		return "codex", nil
	case "anthropic", "claude":
		return "anthropic", nil
	default:
		return "", &cliError{Code: exitInput, Message: "invalid credential provider", Hint: "use codex or anthropic"}
	}
}

func renderCredentialListTable(creds []credentialRecord) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tPROVIDER\tSCOPE\tOWNER\tSTATUS\tCOOLDOWN\tUPDATED"); err != nil {
		return fmt.Errorf("write credential list header: %w", err)
	}
	for _, cred := range creds {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			cred.ID,
			credentialProviderLabel(cred.Provider),
			firstNonEmpty(strings.TrimSpace(string(cred.Scope)), "-"),
			credentialOwnerLabel(cred),
			firstNonEmpty(strings.TrimSpace(string(cred.Status)), "-"),
			credentialCooldownLabel(cred.CooldownUntil),
			credentialTimeLabel(cred.UpdatedAt),
		); err != nil {
			return fmt.Errorf("write credential list row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush credential list table: %w", err)
	}
	return nil
}

func renderCredentialDetailTable(cred credentialRecord) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	rows := [][2]string{
		{"id", cred.ID},
		{"provider", credentialProviderLabel(cred.Provider)},
		{"scope", string(cred.Scope)},
		{"owner_user_id", credentialOwnerLabel(cred)},
		{"status", string(cred.Status)},
		{"weight", fmt.Sprintf("%d", cred.Weight)},
		{"cooldown_until", credentialCooldownLabel(cred.CooldownUntil)},
		{"last_error", firstNonEmpty(strings.TrimSpace(cred.LastError), "-")},
		{"created_at", credentialTimeLabel(cred.CreatedAt)},
		{"updated_at", credentialTimeLabel(cred.UpdatedAt)},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", row[0], row[1]); err != nil {
			return fmt.Errorf("write credential detail row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush credential detail table: %w", err)
	}
	return nil
}

func credentialProviderLabel(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "codex"
	}
	return provider
}

func credentialOwnerLabel(cred credentialRecord) string {
	return firstNonEmpty(strings.TrimSpace(cred.OwnerUserID), "-")
}

func credentialCooldownLabel(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func credentialTimeLabel(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}
