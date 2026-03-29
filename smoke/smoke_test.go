//go:build smoke

package smoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	api "github.com/rtzll/rascal/internal/api"
	irascalruntime "github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

const (
	smokeAPIToken   = "smoke-api-token"
	smokeEncKey     = "smoke-credential-key-123456789012"
	smokeGitHubRepo = "smoke/example"
)

type smokeServer struct {
	baseURL string
	cmd     *exec.Cmd
	output  *bytes.Buffer
	client  *http.Client
}

func TestSmokeNoop(t *testing.T) {
	server := startSmokeServer(t, smokeConfig{
		name:       "noop",
		runnerMode: "noop",
	})
	createSharedCodexCredential(t, server)
	run := createTask(t, server, "Smoke noop run")
	run = waitForFinalRun(t, server, run.ID, 30*time.Second)
	if run.Status != state.StatusSucceeded {
		t.Fatalf("run status = %q, want %q\nserver output:\n%s", run.Status, state.StatusSucceeded, server.output.String())
	}
	logs := fetchRunLogs(t, server, run.ID)
	if !strings.Contains(logs, "noop runner executed") {
		t.Fatalf("logs missing noop marker:\n%s", logs)
	}
}

func TestSmokeDocker(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("SMOKE_DOCKER_IMAGE"))
	if image == "" {
		image = "rascal-runner-smoke-codex:latest"
	}

	server := startSmokeServer(t, smokeConfig{
		name:             "docker",
		runnerMode:       "docker",
		runnerImageCodex: image,
	})
	createSharedCodexCredential(t, server)
	run := createTask(t, server, "Smoke docker run")
	run = waitForFinalRun(t, server, run.ID, 90*time.Second)
	if run.Status != state.StatusReview {
		t.Fatalf("run status = %q, want %q\nserver output:\n%s", run.Status, state.StatusReview, server.output.String())
	}
	if strings.TrimSpace(run.PRURL) == "" {
		t.Fatalf("run pr url empty for review run\nserver output:\n%s", server.output.String())
	}
	logs := fetchRunLogs(t, server, run.ID)
	for _, want := range []string{"starting docker runner", "docker security mode=baseline", "smoke codex ran"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q:\n%s", want, logs)
		}
	}
}

type smokeConfig struct {
	name             string
	runnerMode       string
	runnerImageCodex string
}

func startSmokeServer(t *testing.T, cfg smokeConfig) *smokeServer {
	t.Helper()

	repoRoot := repoRoot(t)
	daemonPath := filepath.Join(repoRoot, "bin", "rascald")
	if _, err := os.Stat(daemonPath); err != nil {
		t.Fatalf("smoke daemon binary missing at %s: %v", daemonPath, err)
	}

	port := reservePort(t)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)
	dataDir := filepath.Join(t.TempDir(), "data")

	cmd := exec.Command(daemonPath)
	cmd.Dir = repoRoot
	output := &bytes.Buffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = append(os.Environ(),
		"RASCAL_LISTEN_ADDR="+listenAddr,
		"RASCAL_DATA_DIR="+dataDir,
		"RASCAL_STATE_PATH="+filepath.Join(dataDir, "state.db"),
		"RASCAL_API_TOKEN="+smokeAPIToken,
		"RASCAL_CREDENTIAL_ENCRYPTION_KEY="+smokeEncKey,
		"RASCAL_RUNNER_MODE="+cfg.runnerMode,
		"RASCAL_AGENT_RUNTIME=codex",
		"RASCAL_GITHUB_TOKEN=smoke-github-token",
		"RASCAL_TASK_SESSION_MODE=off",
	)
	if strings.TrimSpace(cfg.runnerImageCodex) != "" {
		cmd.Env = append(cmd.Env, "RASCAL_RUNNER_IMAGE_CODEX="+cfg.runnerImageCodex)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start rascald: %v", err)
	}

	server := &smokeServer{
		baseURL: "http://" + listenAddr,
		cmd:     cmd,
		output:  output,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}

	t.Cleanup(func() {
		stopSmokeServer(t, server)
	})

	waitForReady(t, server, 15*time.Second)
	return server
}

func stopSmokeServer(t *testing.T, server *smokeServer) {
	t.Helper()
	if server == nil || server.cmd == nil || server.cmd.Process == nil {
		return
	}
	_ = server.cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() {
		done <- server.cmd.Wait()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait for rascald shutdown: %v\nserver output:\n%s", err, server.output.String())
		}
	case <-time.After(10 * time.Second):
		_ = server.cmd.Process.Kill()
		<-done
		t.Fatalf("timed out waiting for rascald shutdown\nserver output:\n%s", server.output.String())
	}
}

func waitForReady(t *testing.T, server *smokeServer, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := server.client.Get(server.baseURL + "/readyz")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte(`"ready":true`)) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("rascald never became ready\nserver output:\n%s", server.output.String())
}

func createSharedCodexCredential(t *testing.T, server *smokeServer) {
	t.Helper()
	payload := api.CreateCredentialRequest{
		ID:       "cred-smoke-codex",
		Scope:    state.CredentialScopeShared,
		Provider: "codex",
		AuthBlob: `{"token":"smoke"}`,
		Weight:   1,
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal credential payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.baseURL+"/v1/credentials", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new credential request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+smokeAPIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.client.Do(req)
	if err != nil {
		t.Fatalf("create credential: %v\nserver output:\n%s", err, server.output.String())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create credential status = %d, want %d: %s\nserver output:\n%s", resp.StatusCode, http.StatusCreated, strings.TrimSpace(string(body)), server.output.String())
	}
}

func createTask(t *testing.T, server *smokeServer, instruction string) state.Run {
	t.Helper()
	debug := false
	agentRuntime := irascalruntime.RuntimeCodex
	payload := api.CreateTaskRequest{
		Repo:         smokeGitHubRepo,
		Instruction:  instruction,
		AgentRuntime: &agentRuntime,
		BaseBranch:   "main",
		Debug:        &debug,
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal task payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.baseURL+"/v1/tasks", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new task request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+smokeAPIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := server.client.Do(req)
	if err != nil {
		t.Fatalf("create task: %v\nserver output:\n%s", err, server.output.String())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create task status = %d, want %d: %s\nserver output:\n%s", resp.StatusCode, http.StatusAccepted, strings.TrimSpace(string(body)), server.output.String())
	}
	var out api.RunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create task response: %v", err)
	}
	return out.Run
}

func waitForFinalRun(t *testing.T, server *smokeServer, runID string, timeout time.Duration) state.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run := getRun(t, server, runID)
		if state.IsFinalRunStatus(run.Status) {
			return run
		}
		time.Sleep(250 * time.Millisecond)
	}
	run := getRun(t, server, runID)
	t.Fatalf("run %s did not finish before timeout; last status=%q\nserver output:\n%s", runID, run.Status, server.output.String())
	return run
}

func getRun(t *testing.T, server *smokeServer, runID string) state.Run {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.baseURL+"/v1/runs/"+runID, nil)
	if err != nil {
		t.Fatalf("new get run request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+smokeAPIToken)
	resp, err := server.client.Do(req)
	if err != nil {
		t.Fatalf("get run: %v\nserver output:\n%s", err, server.output.String())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("get run status = %d, want %d: %s\nserver output:\n%s", resp.StatusCode, http.StatusOK, strings.TrimSpace(string(body)), server.output.String())
	}
	var out api.RunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	return out.Run
}

func fetchRunLogs(t *testing.T, server *smokeServer, runID string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.baseURL+"/v1/runs/"+runID+"/logs?lines=500", nil)
	if err != nil {
		t.Fatalf("new logs request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+smokeAPIToken)
	resp, err := server.client.Do(req)
	if err != nil {
		t.Fatalf("get run logs: %v\nserver output:\n%s", err, server.output.String())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("logs status = %d, want %d: %s\nserver output:\n%s", resp.StatusCode, http.StatusOK, strings.TrimSpace(string(body)), server.output.String())
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read logs response: %v", err)
	}
	return string(body)
}

func reservePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("resolve caller path")
	}
	return filepath.Dir(filepath.Dir(file))
}
