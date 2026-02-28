package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/christianrotzoll/rascal/internal/config"
	"github.com/christianrotzoll/rascal/internal/state"
)

type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func main() {
	cfg := config.LoadClientConfig()
	client := apiClient{
		baseURL: cfg.ServerURL,
		token:   cfg.APIToken,
		http:    &http.Client{Timeout: 20 * time.Second},
	}

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	switch cmd {
	case "bootstrap":
		runBootstrap(os.Args[2:])
	case "run":
		if err := runTaskCommand(client, cfg, os.Args[2:]); err != nil {
			exitErr(err)
		}
	case "issue":
		if err := runIssueCommand(client, os.Args[2:]); err != nil {
			exitErr(err)
		}
	case "ps":
		if err := runPSCommand(client); err != nil {
			exitErr(err)
		}
	case "logs":
		if err := runLogsCommand(client, os.Args[2:]); err != nil {
			exitErr(err)
		}
	case "doctor":
		runDoctor(cfg)
	default:
		usage()
		exitErr(fmt.Errorf("unknown command %q", cmd))
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rascal commands:
  bootstrap
  run   -R OWNER/REPO -t "task text" [-b main]
  issue OWNER/REPO#123
  ps
  logs  <run_id>
  doctor`)
}

func runBootstrap(_ []string) {
	fmt.Println("bootstrap is not implemented yet; start rascald and set RASCAL_SERVER_URL + RASCAL_API_TOKEN")
}

func runTaskCommand(client apiClient, cfg config.ClientConfig, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	repo := fs.String("R", cfg.DefaultRepo, "repository in OWNER/REPO form")
	task := fs.String("t", "", "task text")
	base := fs.String("b", "main", "base branch")
	if err := fs.Parse(args); err != nil {
		return err
	}

	*repo = strings.TrimSpace(*repo)
	*task = strings.TrimSpace(*task)
	if *repo == "" || *task == "" {
		return fmt.Errorf("both -R and -t are required")
	}

	payload := map[string]any{
		"repo":        *repo,
		"task":        *task,
		"base_branch": strings.TrimSpace(*base),
	}

	resp, err := client.doJSON(http.MethodPost, "/v1/tasks", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Run state.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Printf("run created: %s (%s)\n", out.Run.ID, out.Run.Status)
	return nil
}

func runIssueCommand(client apiClient, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: rascal issue OWNER/REPO#123")
	}
	repo, issueNumber, err := parseIssueRef(args[0])
	if err != nil {
		return err
	}

	payload := map[string]any{
		"repo":         repo,
		"issue_number": issueNumber,
	}
	resp, err := client.doJSON(http.MethodPost, "/v1/tasks/issue", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotImplemented {
		return fmt.Errorf("issue-based runs are not implemented yet")
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	fmt.Println("issue run accepted")
	return nil
}

func runPSCommand(client apiClient) error {
	resp, err := client.do(http.MethodGet, "/v1/runs", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Runs []state.Run `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN ID\tSTATUS\tREPO\tCREATED")
	for _, run := range out.Runs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", run.ID, run.Status, run.Repo, run.CreatedAt.Format(time.RFC3339))
	}
	return tw.Flush()
}

func runLogsCommand(client apiClient, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: rascal logs <run_id>")
	}

	resp, err := client.do(http.MethodGet, "/v1/runs/"+args[0]+"/logs", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

func runDoctor(cfg config.ClientConfig) {
	fmt.Printf("server: %s\n", cfg.ServerURL)
	if cfg.APIToken == "" {
		fmt.Println("api token: missing (set RASCAL_API_TOKEN)")
	} else {
		fmt.Println("api token: set")
	}
	if cfg.DefaultRepo == "" {
		fmt.Println("default repo: not set (set RASCAL_DEFAULT_REPO)")
	} else {
		fmt.Printf("default repo: %s\n", cfg.DefaultRepo)
	}
}

func parseIssueRef(input string) (string, int, error) {
	parts := strings.Split(input, "#")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid issue ref %q, expected OWNER/REPO#123", input)
	}
	repo := strings.TrimSpace(parts[0])
	var issue int
	if _, err := fmt.Sscanf(parts[1], "%d", &issue); err != nil || issue <= 0 {
		return "", 0, fmt.Errorf("invalid issue number in %q", input)
	}
	if repo == "" {
		return "", 0, fmt.Errorf("invalid repo in %q", input)
	}
	return repo, issue, nil
}

func (c apiClient) doJSON(method, path string, payload any) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return c.do(method, path, bytes.NewReader(data))
}

func (c apiClient) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
