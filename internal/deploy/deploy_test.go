package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/defaults"
	"github.com/rtzll/rascal/internal/runtime"
)

func TestGoarchFromUnameMachine(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "x86_64", want: "amd64", ok: true},
		{in: "amd64", want: "amd64", ok: true},
		{in: "aarch64", want: "arm64", ok: true},
		{in: "arm64", want: "arm64", ok: true},
		{in: "ppc64le", want: "", ok: false},
	}
	for _, tc := range tests {
		got, ok := GoarchFromUnameMachine(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("GoarchFromUnameMachine(%q) = (%q, %t), want (%q, %t)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestGoarchFromHetznerArchitecture(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "x86", want: "amd64", ok: true},
		{in: "x86_64", want: "amd64", ok: true},
		{in: "amd64", want: "amd64", ok: true},
		{in: "arm", want: "arm64", ok: true},
		{in: "aarch64", want: "arm64", ok: true},
		{in: "arm64", want: "arm64", ok: true},
		{in: "unknown", want: "", ok: false},
	}
	for _, tc := range tests {
		got, ok := GoarchFromHetznerArchitecture(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("GoarchFromHetznerArchitecture(%q) = (%q, %t), want (%q, %t)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRenderCaddyfileVariants(t *testing.T) {
	local, err := renderCaddyfile("")
	if err != nil {
		t.Fatalf("render local caddyfile: %v", err)
	}
	if !strings.Contains(local, ":8080 {") {
		t.Fatalf("local caddyfile missing :8080 site:\n%s", local)
	}
	if strings.Contains(local, "example.com {") {
		t.Fatalf("local caddyfile unexpectedly includes domain block:\n%s", local)
	}

	domain, err := renderCaddyfile("example.com")
	if err != nil {
		t.Fatalf("render domain caddyfile: %v", err)
	}
	if strings.Contains(domain, ":8080 {") {
		t.Fatalf("domain caddyfile should not include local :8080 block:\n%s", domain)
	}
	if !strings.Contains(domain, "example.com {") {
		t.Fatalf("domain caddyfile missing domain block:\n%s", domain)
	}
	for _, want := range []string{
		"@allowed_health",
		"@allowed_api",
		"@allowed_repository_api",
		"@allowed_webhook",
		"path /v1/webhooks/github",
		"header X-GitHub-Event *",
		"header X-GitHub-Delivery *",
		"header X-Hub-Signature-256 *",
		"respond 404",
	} {
		if !strings.Contains(local, want) {
			t.Fatalf("local caddyfile missing %q:\n%s", want, local)
		}
		if !strings.Contains(domain, want) {
			t.Fatalf("domain caddyfile missing %q:\n%s", want, domain)
		}
	}
}

func TestRenderCaddyfileAllowsRepositoryWriteMethods(t *testing.T) {
	rendered, err := renderCaddyfile("")
	if err != nil {
		t.Fatalf("render caddyfile: %v", err)
	}

	wantBlock := strings.TrimSpace(`
@allowed_repository_api {
    method GET POST PATCH PUT DELETE
    path /v1/repositories /v1/repositories/*
  }
  handle @allowed_repository_api {
    import /etc/caddy/rascal-upstream.caddy
  }`)
	if !strings.Contains(rendered, wantBlock) {
		t.Fatalf("repository API matcher missing required methods or paths:\n%s", rendered)
	}
}

func TestExecuteRollsBackWhenCaddyReloadFails(t *testing.T) {
	logDir := setupFakeDeployCommands(t, "caddy_reload")

	err := Execute(testDeployConfig())
	if err == nil || !strings.Contains(err.Error(), "failed to reload caddy with new upstream") {
		t.Fatalf("expected caddy reload failure, got: %v", err)
	}

	scripts := readCapturedSSHScripts(t, logDir)
	if !containsScript(scripts, "systemctl enable caddy --now") {
		t.Fatalf("expected cutover caddy reload script, got %d scripts", len(scripts))
	}
	if !containsScript(scripts, "EOF_ROLLBACK") {
		t.Fatalf("expected rollback script, got %d scripts", len(scripts))
	}
}

func TestExecuteRollsBackWhenProxyReadinessFails(t *testing.T) {
	logDir := setupFakeDeployCommands(t, "proxy_readiness")

	err := Execute(testDeployConfig())
	if err == nil || !strings.Contains(err.Error(), "proxy readiness check failed on caddy") {
		t.Fatalf("expected proxy readiness failure, got: %v", err)
	}

	scripts := readCapturedSSHScripts(t, logDir)
	if !containsScript(scripts, "proxy readiness check failed on caddy; rolling back") {
		t.Fatalf("expected proxy readiness probe script, got %d scripts", len(scripts))
	}
	if !containsScript(scripts, "EOF_ROLLBACK") {
		t.Fatalf("expected rollback script, got %d scripts", len(scripts))
	}
}

func TestExecuteRollsOutRunnerBinaryBeforeImageBuild(t *testing.T) {
	logDir := setupFakeDeployCommands(t, "")
	t.Setenv("RASCAL_BUILD_VERSION", "v-test")
	t.Setenv("RASCAL_BUILD_COMMIT", "deadbee")
	t.Setenv("RASCAL_BUILD_TIME", "2026-03-03T00:00:00Z")

	if err := Execute(testDeployConfig()); err != nil {
		t.Fatalf("execute deploy: %v", err)
	}

	goCalls := readCapturedCommandLines(t, filepath.Join(logDir, "go_calls.log"))
	if !containsLine(goCalls, "./cmd/rascald") {
		t.Fatalf("expected go build for rascald, got calls: %v", goCalls)
	}
	if !containsLine(goCalls, "./cmd/rascal-runner") {
		t.Fatalf("expected go build for rascal-runner, got calls: %v", goCalls)
	}
	runnerCall := firstLineContaining(goCalls, "./cmd/rascal-runner")
	if runnerCall == "" {
		t.Fatalf("missing runner build call in go calls: %v", goCalls)
	}
	for _, needle := range []string{"-ldflags", "main.buildVersion=v-test", "main.buildCommit=deadbee", "main.buildTime=2026-03-03T00:00:00Z"} {
		if !strings.Contains(runnerCall, needle) {
			t.Fatalf("expected runner build call to contain %q, got: %s", needle, runnerCall)
		}
	}

	scpCalls := readCapturedCommandLines(t, filepath.Join(logDir, "scp_calls.log"))
	if !containsLine(scpCalls, "/tmp/rascal-bootstrap/rascal-runner") {
		t.Fatalf("expected scp upload of rascal-runner binary, got calls: %v", scpCalls)
	}

	scripts := readCapturedSSHScripts(t, logDir)
	foundBuildScript := false
	for _, script := range scripts {
		installIdx := strings.Index(script, "install -m 0755 /tmp/rascal-bootstrap/rascal-runner /opt/rascal/runner/rascal-runner")
		dockerIdx := strings.Index(script, "docker build --quiet --build-arg CACHE_BUST=")
		if installIdx < 0 || dockerIdx < 0 {
			continue
		}
		foundBuildScript = true
		if installIdx > dockerIdx {
			t.Fatalf("expected rascal-runner install before docker build, script:\n%s", script)
		}
	}
	if !foundBuildScript {
		t.Fatalf("expected deploy script containing rascal-runner install and docker build, got %d scripts", len(scripts))
	}
}

func TestExecuteDoesNotEmitLegacySingleUnitSystemdCommands(t *testing.T) {
	logDir := setupFakeDeployCommands(t, "")

	if err := Execute(testDeployConfig()); err != nil {
		t.Fatalf("execute deploy: %v", err)
	}

	scripts := readCapturedSSHScripts(t, logDir)
	for _, script := range scripts {
		if containsLegacySingleUnitCommand(script) {
			t.Fatalf("unexpected legacy single-unit command in deploy script:\n%s", script)
		}
	}
}

func TestEmbeddedBootstrapHostScriptOwnsPackageInstallation(t *testing.T) {
	content, err := assetsFS.ReadFile("assets/bootstrap_host.sh")
	if err != nil {
		t.Fatalf("read embedded bootstrap_host.sh: %v", err)
	}
	script := string(content)
	for _, want := range []string{
		"ensure_base_packages()",
		"ensure_docker()",
		"ensure_caddy()",
		"apt-get install -y -qq sqlite3 ripgrep curl gpg debian-keyring debian-archive-keyring apt-transport-https ca-certificates gnupg lsb-release >/dev/null",
		"apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null",
		"apt-get install -y -qq caddy >/dev/null",
		"ensure_host_layout()",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bootstrap_host.sh missing %q:\n%s", want, script)
		}
	}
}

func TestExecuteRunsUnifiedBootstrapHostStep(t *testing.T) {
	logDir := setupFakeDeployCommands(t, "")

	if err := Execute(testDeployConfig()); err != nil {
		t.Fatalf("execute deploy: %v", err)
	}

	scpCalls := readCapturedCommandLines(t, filepath.Join(logDir, "scp_calls.log"))
	if !containsLine(scpCalls, "/tmp/rascal-bootstrap/bootstrap_host.sh") {
		t.Fatalf("expected scp upload of bootstrap_host.sh, got calls: %v", scpCalls)
	}
	scripts := readCapturedSSHScripts(t, logDir)
	if !containsScript(scripts, "chmod +x /tmp/rascal-bootstrap/bootstrap_host.sh") {
		t.Fatalf("expected deploy to execute bootstrap_host.sh, got %d scripts", len(scripts))
	}
	if containsScript(scripts, "install_docker.sh") {
		t.Fatalf("did not expect deploy scripts to reference legacy install_docker.sh, got %d scripts", len(scripts))
	}
}

func TestDeployGoNoLongerEmbedsPackageInstallationInline(t *testing.T) {
	content, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatalf("read deploy.go: %v", err)
	}
	source := string(content)
	for _, forbidden := range []string{
		"apt-get install -y -qq caddy",
		"apt-get install -y -qq sqlite3 ripgrep",
		"https://dl.cloudsmith.io/public/caddy/stable/gpg.key",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("deploy.go should not embed host package installation (%q):\n%s", forbidden, source)
		}
	}
}

func TestExecuteReclaimsInactiveDrainingSlotBeforeRestart(t *testing.T) {
	logDir := setupFakeDeployCommands(t, "")

	if err := Execute(testDeployConfig()); err != nil {
		t.Fatalf("execute deploy: %v", err)
	}

	scripts := readCapturedSSHScripts(t, logDir)
	reclaimIdx := scriptIndexContaining(scripts, `systemctl kill -s SIGUSR2 "rascal@green"`)
	restartIdx := scriptIndexContaining(scripts, `systemctl restart 'rascal@green'`)
	if reclaimIdx < 0 {
		t.Fatalf("expected reclaim script for inactive green slot, got %d scripts", len(scripts))
	}
	if restartIdx < 0 {
		t.Fatalf("expected restart script for inactive green slot, got %d scripts", len(scripts))
	}
	if reclaimIdx >= restartIdx {
		t.Fatalf("expected reclaim before restart, got reclaim=%d restart=%d", reclaimIdx, restartIdx)
	}
}

func TestExecuteDrainsPreviousSlotInsteadOfStoppingItImmediately(t *testing.T) {
	logDir := setupFakeDeployCommands(t, "")

	if err := Execute(testDeployConfig()); err != nil {
		t.Fatalf("execute deploy: %v", err)
	}

	scripts := readCapturedSSHScripts(t, logDir)
	cutoverIdx := scriptIndexContaining(scripts, `echo 'green' >/etc/rascal/active_slot`)
	if cutoverIdx < 0 {
		t.Fatalf("expected cutover script, got %d scripts", len(scripts))
	}
	cutoverScript := scripts[cutoverIdx]
	if !strings.Contains(cutoverScript, `systemctl kill -s SIGUSR1 "rascal@blue"`) {
		t.Fatalf("expected cutover to signal old blue slot into drain mode:\n%s", cutoverScript)
	}
	for _, forbidden := range []string{
		`systemctl stop --no-block "rascal@blue"`,
		`systemctl disable "rascal@blue"`,
	} {
		if strings.Contains(cutoverScript, forbidden) {
			t.Fatalf("cutover script should not stop old slot immediately (%s):\n%s", forbidden, cutoverScript)
		}
	}
}

func TestResolveRunnerBuildInfoUsesEnv(t *testing.T) {
	t.Setenv("RASCAL_BUILD_VERSION", "v1.2.3")
	t.Setenv("RASCAL_BUILD_COMMIT", "abc1234")
	t.Setenv("RASCAL_BUILD_TIME", "2026-03-03T00:00:00Z")

	version, commit, builtAt := resolveRunnerBuildInfo()
	if version != "v1.2.3" {
		t.Fatalf("version = %q, want v1.2.3", version)
	}
	if commit != "abc1234" {
		t.Fatalf("commit = %q, want abc1234", commit)
	}
	if builtAt != "2026-03-03T00:00:00Z" {
		t.Fatalf("builtAt = %q, want 2026-03-03T00:00:00Z", builtAt)
	}
}

func TestResolveRunnerBuildInfoDefaults(t *testing.T) {
	t.Setenv("RASCAL_BUILD_VERSION", "")
	t.Setenv("RASCAL_BUILD_COMMIT", "")
	t.Setenv("RASCAL_BUILD_TIME", "")

	version, commit, builtAt := resolveRunnerBuildInfo()
	if version != "dev" {
		t.Fatalf("version = %q, want dev", version)
	}
	if commit != "unknown" {
		t.Fatalf("commit = %q, want unknown", commit)
	}
	if builtAt == "" {
		t.Fatal("builtAt should not be empty")
	}
	if _, err := time.Parse(time.RFC3339, builtAt); err != nil {
		t.Fatalf("builtAt is not RFC3339: %q (%v)", builtAt, err)
	}
}

func TestSystemdServiceContentUsesMixedKillModeForDrain(t *testing.T) {
	content := systemdServiceContent()
	for _, want := range []string{
		"KillSignal=SIGTERM",
		"KillMode=mixed",
		"TimeoutStopSec=330",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("service content missing %q:\n%s", want, content)
		}
	}
}

func TestServerEnvFileEnablesAgentSessionsByDefault(t *testing.T) {
	content := serverEnvFile(testDeployConfig())
	if !strings.Contains(content, "RASCAL_TASK_SESSION_MODE=all") {
		t.Fatalf("expected agent sessions enabled by default, got:\n%s", content)
	}
}

func TestServerEnvFileIncludesDockerHardeningDefaults(t *testing.T) {
	content := serverEnvFile(testDeployConfig())
	for _, want := range []string{
		"RASCAL_RUNNER_DOCKER_SECURITY_MODE=baseline",
		"RASCAL_RUNNER_DOCKER_CPUS=2",
		"RASCAL_RUNNER_DOCKER_MEMORY=4g",
		"RASCAL_RUNNER_DOCKER_PIDS_LIMIT=256",
		"RASCAL_RUNNER_DOCKER_TMPFS_TMP_SIZE=512m",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected server env file to include %q, got:\n%s", want, content)
		}
	}
}

func TestServerEnvFileOmitsLegacyRunnerImageEnv(t *testing.T) {
	content := serverEnvFile(testDeployConfig())
	if strings.Contains(content, "RASCAL_RUNNER_IMAGE=") {
		t.Fatalf("expected server env file to omit legacy runner image, got:\n%s", content)
	}
	if strings.Contains(content, "RASCAL_CODEX_AUTH_PATH=") {
		t.Fatalf("expected server env file to omit static codex auth path, got:\n%s", content)
	}
}

func testDeployConfig() Config {
	return Config{
		Host:                  "example-host",
		SSHUser:               "root",
		SSHPort:               22,
		Domain:                "rascal.example.com",
		AgentRuntime:          runtime.RuntimeCodex,
		RunnerImageGooseCodex: defaults.GooseCodexRunnerImageTag,
		RunnerImageCodex:      defaults.CodexRunnerImageTag,
		ServerListenAddr:      ":8080",
		ServerDataDir:         "/var/lib/rascal",
		ServerStatePath:       "/var/lib/rascal/state.db",
		GOARCH:                "amd64",
		UploadEnvFile:         false,
	}
}

func setupFakeDeployCommands(t *testing.T, failMode string) string {
	t.Helper()

	binDir := t.TempDir()
	logDir := t.TempDir()
	t.Setenv("RASCAL_TEST_LOG_DIR", logDir)
	t.Setenv("RASCAL_TEST_FAIL_MODE", failMode)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	writeExe(t, filepath.Join(binDir, "go"), `#!/usr/bin/env bash
set -eu
log_dir="${RASCAL_TEST_LOG_DIR:?}"
printf '%s\n' "$*" >> "$log_dir/go_calls.log"
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out="$arg"
    break
  fi
  prev="$arg"
done
if [ -n "$out" ]; then
  mkdir -p "$(dirname "$out")"
  : > "$out"
fi
exit 0
`)

	writeExe(t, filepath.Join(binDir, "tar"), `#!/usr/bin/env bash
set -eu
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-czf" ]; then
    out="$arg"
    break
  fi
  prev="$arg"
done
if [ -n "$out" ]; then
  mkdir -p "$(dirname "$out")"
  : > "$out"
fi
exit 0
`)

	writeExe(t, filepath.Join(binDir, "scp"), `#!/usr/bin/env bash
set -eu
log_dir="${RASCAL_TEST_LOG_DIR:?}"
printf '%s\n' "$*" >> "$log_dir/scp_calls.log"
exit 0
`)

	writeExe(t, filepath.Join(binDir, "ssh"), `#!/usr/bin/env bash
set -eu
log_dir="${RASCAL_TEST_LOG_DIR:?}"
count_file="$log_dir/ssh_count"
n=0
if [ -f "$count_file" ]; then
  n="$(cat "$count_file")"
fi
n=$((n + 1))
echo "$n" > "$count_file"
script_file="$log_dir/ssh_script_${n}.sh"
cat > "$script_file"

if grep -Fq "slot=''" "$script_file" && grep -Fq "printf 'blue'" "$script_file"; then
  printf 'blue'
  exit 0
fi

case "${RASCAL_TEST_FAIL_MODE:-}" in
  caddy_reload)
    if grep -Fq "systemctl enable caddy --now" "$script_file"; then
      echo "forced caddy reload failure" >&2
      exit 1
    fi
    ;;
  proxy_readiness)
    if grep -Fq "proxy readiness check failed on caddy; rolling back" "$script_file"; then
      echo "forced proxy readiness failure" >&2
      exit 1
    fi
    ;;
esac

exit 0
`)

	return logDir
}

func writeExe(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake executable %s: %v", path, err)
	}
}

func readCapturedSSHScripts(t *testing.T, logDir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(logDir, "ssh_script_*.sh"))
	if err != nil {
		t.Fatalf("glob scripts: %v", err)
	}
	sort.Slice(files, func(i, j int) bool {
		return scriptSequenceNumber(files[i]) < scriptSequenceNumber(files[j])
	})
	out := make([]string, 0, len(files))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read captured script %s: %v", f, err)
		}
		out = append(out, string(b))
	}
	return out
}

func containsScript(scripts []string, needle string) bool {
	for _, script := range scripts {
		if strings.Contains(script, needle) {
			return true
		}
	}
	return false
}

func scriptIndexContaining(scripts []string, needle string) int {
	for i, script := range scripts {
		if strings.Contains(script, needle) {
			return i
		}
	}
	return -1
}

func readCapturedCommandLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read command log %s: %v", path, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func containsLine(lines []string, needle string) bool {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func firstLineContaining(lines []string, needle string) string {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func containsLegacySingleUnitCommand(script string) bool {
	legacyPattern := regexp.MustCompile(`\bsystemctl\s+(?:is-active --quiet|stop|disable)\s+rascal(?:\s|;|$)`)
	return legacyPattern.MatchString(script)
}

func scriptSequenceNumber(path string) int {
	base := filepath.Base(path)
	base = strings.TrimPrefix(base, "ssh_script_")
	base = strings.TrimSuffix(base, ".sh")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 0
	}
	return n
}
