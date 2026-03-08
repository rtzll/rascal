package deploy

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/defaults"
)

const (
	SlotBlue      = "blue"
	SlotGreen     = "green"
	SlotBluePort  = 18080
	SlotGreenPort = 18081
	ProxyPort     = 8080
)

type Config struct {
	Host               string
	SSHUser            string
	SSHKeyPath         string
	SSHPort            int
	Domain             string
	APIToken           string
	WebhookSecret      string
	GitHubRuntimeToken string
	RunnerMode         string
	AgentBackend       agent.Backend
	RunnerImage        string
	RunnerImageGoose   string
	RunnerImageCodex   string
	ServerListenAddr   string
	ServerDataDir      string
	ServerStatePath    string
	GOARCH             string
	UploadEnvFile      bool
}

type remoteUpload struct {
	LocalPath  string
	RemotePath string
}

type plan struct {
	Version          int      `json:"version"`
	CreatedAt        string   `json:"created_at"`
	Host             string   `json:"host"`
	Domain           string   `json:"domain,omitempty"`
	GOARCH           string   `json:"goarch"`
	AgentBackend     string   `json:"agent_backend"`
	RunnerImage      string   `json:"runner_image"`
	RunnerImageGoose string   `json:"runner_image_goose"`
	RunnerImageCodex string   `json:"runner_image_codex"`
	UploadEnvFile    bool     `json:"upload_env_file"`
	Steps            []string `json:"steps"`
}

//go:embed assets/bootstrap_host.sh assets/Caddyfile.tmpl
var assetsFS embed.FS

func Execute(cfg Config) error {
	if cfg.UploadEnvFile {
		if strings.TrimSpace(cfg.APIToken) == "" {
			return fmt.Errorf("api token is required when uploading /etc/rascal/rascal.env")
		}
		if strings.TrimSpace(cfg.GitHubRuntimeToken) == "" {
			return fmt.Errorf("github runtime token is required when uploading /etc/rascal/rascal.env")
		}
		if strings.TrimSpace(cfg.WebhookSecret) == "" {
			return fmt.Errorf("webhook secret is required when uploading /etc/rascal/rascal.env")
		}
	}
	tmpDir, err := os.MkdirTemp("", "rascal-bootstrap-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil {
			log.Printf("remove temp deploy dir %s: %v", tmpDir, removeErr)
		}
	}()

	binaryPath := filepath.Join(tmpDir, "rascald")
	if err := buildLinuxRascald(binaryPath, cfg.GOARCH); err != nil {
		return err
	}
	runnerBinaryPath := filepath.Join(tmpDir, "rascal-runner")
	if err := buildLinuxRascalRunner(runnerBinaryPath, cfg.GOARCH); err != nil {
		return err
	}

	var envPath string
	if cfg.UploadEnvFile {
		envPath = filepath.Join(tmpDir, "rascal.env")
		if err := os.WriteFile(envPath, []byte(serverEnvFile(cfg)), 0o600); err != nil {
			return fmt.Errorf("write env file: %w", err)
		}
	}

	servicePath := filepath.Join(tmpDir, "rascal@.service")
	if err := os.WriteFile(servicePath, []byte(systemdServiceContent()), 0o644); err != nil {
		return fmt.Errorf("write systemd service: %w", err)
	}

	runnerArchivePath := filepath.Join(tmpDir, "runner.tgz")
	if err := runLocalInDir(repoRootPath(), "tar", "-C", repoRootPath(), "-czf", runnerArchivePath, "runner"); err != nil {
		return fmt.Errorf("package runner assets: %w", err)
	}

	bootstrapHostScript, err := assetsFS.ReadFile("assets/bootstrap_host.sh")
	if err != nil {
		return fmt.Errorf("read embedded bootstrap_host.sh: %w", err)
	}
	bootstrapHostScriptPath := filepath.Join(tmpDir, "bootstrap_host.sh")
	if err := os.WriteFile(bootstrapHostScriptPath, bootstrapHostScript, 0o700); err != nil {
		return fmt.Errorf("write bootstrap_host.sh: %w", err)
	}

	caddyPath := filepath.Join(tmpDir, "Caddyfile")
	caddyfile, err := renderCaddyfile(cfg.Domain)
	if err != nil {
		return fmt.Errorf("render caddyfile: %w", err)
	}
	if err := os.WriteFile(caddyPath, []byte(caddyfile), 0o644); err != nil {
		return fmt.Errorf("write caddyfile: %w", err)
	}

	planPath := filepath.Join(tmpDir, "plan.json")
	data, err := json.MarshalIndent(plan{
		Version:          1,
		Host:             cfg.Host,
		Domain:           strings.TrimSpace(cfg.Domain),
		GOARCH:           strings.TrimSpace(cfg.GOARCH),
		AgentBackend:     string(agent.NormalizeBackend(string(cfg.AgentBackend))),
		RunnerImage:      strings.TrimSpace(cfg.RunnerImage),
		RunnerImageGoose: strings.TrimSpace(cfg.RunnerImageGoose),
		RunnerImageCodex: strings.TrimSpace(cfg.RunnerImageCodex),
		UploadEnvFile:    cfg.UploadEnvFile,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		Steps: []string{
			"prepare_remote_staging",
			"upload_artifacts",
			"bootstrap_host",
			"build_runner_images",
			"install_rascal_files",
			"switch_blue_green_slot",
			"reload_caddy",
		},
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode deploy plan: %w", err)
	}
	if err := os.WriteFile(planPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write deploy plan: %w", err)
	}

	if err := runRemoteScript(cfg, "set -eu\nmkdir -p /tmp/rascal-bootstrap\n"); err != nil {
		return err
	}

	uploads := []remoteUpload{
		{LocalPath: binaryPath, RemotePath: "/tmp/rascal-bootstrap/rascald"},
		{LocalPath: runnerBinaryPath, RemotePath: "/tmp/rascal-bootstrap/rascal-runner"},
		{LocalPath: servicePath, RemotePath: "/tmp/rascal-bootstrap/rascal@.service"},
		{LocalPath: bootstrapHostScriptPath, RemotePath: "/tmp/rascal-bootstrap/bootstrap_host.sh"},
		{LocalPath: runnerArchivePath, RemotePath: "/tmp/rascal-bootstrap/runner.tgz"},
		{LocalPath: caddyPath, RemotePath: "/tmp/rascal-bootstrap/Caddyfile"},
		{LocalPath: planPath, RemotePath: "/tmp/rascal-bootstrap/plan.json"},
	}
	if cfg.UploadEnvFile {
		uploads = append(uploads, remoteUpload{LocalPath: envPath, RemotePath: "/tmp/rascal-bootstrap/rascal.env"})
	}
	for _, up := range uploads {
		if err := runLocal("scp", scpArgs(cfg, up.LocalPath, remoteTarget(cfg, up.RemotePath))...); err != nil {
			return err
		}
	}

	if err := runRemoteScript(cfg, "set -eu\nchmod +x /tmp/rascal-bootstrap/bootstrap_host.sh\n/tmp/rascal-bootstrap/bootstrap_host.sh\n"); err != nil {
		return err
	}
	if err := runRemoteScript(cfg, fmt.Sprintf(strings.TrimSpace(`
set -eu
mkdir -p /opt/rascal /etc/rascal
tar -xzf /tmp/rascal-bootstrap/runner.tgz -C /opt/rascal
install -m 0755 /tmp/rascal-bootstrap/rascal-runner /opt/rascal/runner/rascal-runner
docker build --target goose-runner -t %s /opt/rascal/runner
docker build --target codex-runner -t %s /opt/rascal/runner
install -m 0755 /tmp/rascal-bootstrap/rascald /opt/rascal/rascald
install -m 0644 /tmp/rascal-bootstrap/rascal@.service /etc/systemd/system/rascal@.service
`)+"\n", shellSingleQuote(cfg.RunnerImageGoose), shellSingleQuote(cfg.RunnerImageCodex))); err != nil {
		return err
	}
	if cfg.UploadEnvFile {
		if err := runRemoteScript(cfg, "set -eu\ninstall -m 0600 /tmp/rascal-bootstrap/rascal.env /etc/rascal/rascal.env\n"); err != nil {
			return err
		}
	} else {
		if err := runRemoteScript(cfg, "set -eu\nif [ ! -f /etc/rascal/rascal.env ]; then echo \"missing /etc/rascal/rascal.env\" >&2; exit 1; fi\n"); err != nil {
			return fmt.Errorf("remote env file missing; bootstrap first or run deploy without --skip-env-upload: %w", err)
		}
	}
	if err := runRemoteScript(cfg, fmt.Sprintf(strings.TrimSpace(`
set -eu
cat >/etc/rascal/rascal-blue.env <<'EOF_BLUE'
RASCAL_LISTEN_ADDR=127.0.0.1:%d
RASCAL_SLOT=blue
EOF_BLUE
cat >/etc/rascal/rascal-green.env <<'EOF_GREEN'
RASCAL_LISTEN_ADDR=127.0.0.1:%d
RASCAL_SLOT=green
EOF_GREEN
systemctl daemon-reload
`)+"\n", SlotBluePort, SlotGreenPort)); err != nil {
		return err
	}

	activeSlot, err := detectActiveRemoteSlot(cfg)
	if err != nil {
		return err
	}
	inactiveSlot := otherSlot(activeSlot)
	activePort := portForSlot(activeSlot)
	inactivePort := portForSlot(inactiveSlot)

	if err := runRemoteScript(cfg, fmt.Sprintf("set -eu\nif ! systemctl is-active --quiet 'rascal@%s'; then systemctl enable 'rascal@%s' >/dev/null 2>&1 || true; systemctl restart 'rascal@%s'; fi\n", activeSlot, activeSlot, activeSlot)); err != nil {
		return err
	}
	if err := runRemoteScript(cfg, fmt.Sprintf("set -eu\nsystemctl enable 'rascal@%s' >/dev/null 2>&1 || true\nsystemctl restart 'rascal@%s'\n", inactiveSlot, inactiveSlot)); err != nil {
		return err
	}
	if err := runRemoteScript(cfg, fmt.Sprintf(strings.TrimSpace(`
set -eu
check_http() {
  url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --max-time 5 "$url" >/dev/null 2>&1
    return $?
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -q -T 5 -O - "$url" >/dev/null 2>&1
    return $?
  fi
  return 1
}
ready=0
for _ in $(seq 1 45); do
  if check_http "http://127.0.0.1:%d/readyz"; then
    ready=1
    break
  fi
  sleep 2
done
if [ "$ready" -ne 1 ]; then
  echo "inactive slot %s failed readiness checks" >&2
  systemctl status "rascal@%s" --no-pager || true
  journalctl -u "rascal@%s" -n 80 --no-pager || true
  systemctl stop "rascal@%s" || true
  exit 1
fi
`)+"\n", inactivePort, inactiveSlot, inactiveSlot, inactiveSlot, inactiveSlot)); err != nil {
		return err
	}

	installCaddyScript := "set -eu\nif [ ! -f /etc/caddy/Caddyfile ]; then install -m 0644 /tmp/rascal-bootstrap/Caddyfile /etc/caddy/Caddyfile; fi\n"
	if strings.TrimSpace(cfg.Domain) != "" {
		installCaddyScript = "set -eu\ninstall -m 0644 /tmp/rascal-bootstrap/Caddyfile /etc/caddy/Caddyfile\n"
	}
	if err := runRemoteScript(cfg, installCaddyScript); err != nil {
		return err
	}
	if err := runRemoteScript(cfg, fmt.Sprintf("set -eu\ncat >/etc/caddy/rascal-upstream.caddy <<'EOF_UPSTREAM'\nreverse_proxy 127.0.0.1:%d\nEOF_UPSTREAM\n", inactivePort)); err != nil {
		return err
	}

	if err := runRemoteScript(cfg, "set -eu\nsystemctl enable caddy --now\nsystemctl reload caddy || systemctl restart caddy\n"); err != nil {
		if rollbackErr := rollback(cfg, activeSlot, inactiveSlot, activePort); rollbackErr != nil {
			log.Printf("rollback after caddy reload failure: %v", rollbackErr)
		}
		return fmt.Errorf("failed to reload caddy with new upstream: %w", err)
	}
	if err := verifyProxyReadiness(cfg); err != nil {
		if rollbackErr := rollback(cfg, activeSlot, inactiveSlot, activePort); rollbackErr != nil {
			log.Printf("rollback after proxy readiness failure: %v", rollbackErr)
		}
		return err
	}

	if err := runRemoteScript(cfg, fmt.Sprintf(strings.TrimSpace(`
set -eu
echo %s >/etc/rascal/active_slot
sync
sleep 3
if [ %s != %s ]; then
  systemctl stop --no-block "rascal@%s" || true
  systemctl disable "rascal@%s" >/dev/null 2>&1 || true
fi
systemctl enable "rascal@%s" >/dev/null 2>&1 || true
systemctl is-active --quiet "rascal@%s"
`)+"\n", shellSingleQuote(inactiveSlot), shellSingleQuote(activeSlot), shellSingleQuote(inactiveSlot), activeSlot, activeSlot, inactiveSlot, inactiveSlot)); err != nil {
		return err
	}
	if err := runRemoteScript(cfg, "set -eu\nrm -rf /tmp/rascal-bootstrap\n"); err != nil {
		return err
	}
	return nil
}

func DetectRemoteGOARCH(cfg Config) (string, error) {
	out, err := runLocalCapture("ssh", sshArgs(cfg, "uname -m")...)
	if err != nil {
		return "", fmt.Errorf("run `uname -m` over ssh: %w", err)
	}
	if goarch, ok := GoarchFromUnameMachine(out); ok {
		return goarch, nil
	}
	return "", fmt.Errorf("unsupported remote architecture %q (set --goarch)", strings.TrimSpace(out))
}

func GoarchFromUnameMachine(machine string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(machine)) {
	case "x86_64", "amd64":
		return "amd64", true
	case "aarch64", "arm64":
		return "arm64", true
	default:
		return "", false
	}
}

func GoarchFromHetznerArchitecture(arch string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "x86", "x86_64", "amd64":
		return "amd64", true
	case "arm", "aarch64", "arm64":
		return "arm64", true
	default:
		return "", false
	}
}

func buildLinuxRascald(outputPath, goarch string) error {
	if strings.TrimSpace(goarch) == "" {
		goarch = "amd64"
	}
	cmd := exec.Command("go", "build", "-o", outputPath, "./cmd/rascald")
	cmd.Dir = repoRootPath()
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build rascald: %w", err)
	}
	return nil
}

func buildLinuxRascalRunner(outputPath, goarch string) error {
	if strings.TrimSpace(goarch) == "" {
		goarch = "amd64"
	}
	version, commit, builtAt := resolveRunnerBuildInfo()
	ldflags := fmt.Sprintf("-X main.buildVersion=%s -X main.buildCommit=%s -X main.buildTime=%s", version, commit, builtAt)
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", outputPath, "./cmd/rascal-runner")
	cmd.Dir = repoRootPath()
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build rascal-runner: %w", err)
	}
	return nil
}

func resolveRunnerBuildInfo() (version, commit, builtAt string) {
	version = normalizeBuildMeta(strings.TrimSpace(os.Getenv("RASCAL_BUILD_VERSION")), "dev")
	commit = normalizeBuildMeta(strings.TrimSpace(os.Getenv("RASCAL_BUILD_COMMIT")), "unknown")
	builtAt = normalizeBuildMeta(strings.TrimSpace(os.Getenv("RASCAL_BUILD_TIME")), time.Now().UTC().Format(time.RFC3339))
	return version, commit, builtAt
}

func normalizeBuildMeta(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if strings.Contains(value, " ") {
		value = strings.ReplaceAll(value, " ", "_")
	}
	return value
}

func runLocal(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func runLocalInDir(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func runLocalCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("%s failed: %w", name, err)
		}
		return text, fmt.Errorf("%s failed: %w (%s)", name, err, text)
	}
	return text, nil
}

func runRemoteScript(cfg Config, script string) error {
	cmd := exec.Command("ssh", sshArgs(cfg, "bash -se")...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh failed: %w", err)
	}
	return nil
}

func runRemoteScriptCapture(cfg Config, script string) (string, error) {
	cmd := exec.Command("ssh", sshArgs(cfg, "bash -se")...)
	cmd.Stdin = strings.NewReader(script)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errOut := strings.TrimSpace(stderr.String())
		if errOut == "" {
			errOut = strings.TrimSpace(stdout.String())
		}
		if errOut != "" {
			return "", fmt.Errorf("ssh failed: %w (%s)", err, errOut)
		}
		return "", fmt.Errorf("ssh failed: %w", err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func detectActiveRemoteSlot(cfg Config) (string, error) {
	out, err := runRemoteScriptCapture(cfg, strings.TrimSpace(`
set -eu
slot=''
if [ -f /etc/rascal/active_slot ]; then
  slot="$(tr -d '[:space:]' </etc/rascal/active_slot || true)"
fi
case "$slot" in
  blue|green)
    printf '%s' "$slot"
    exit 0
    ;;
esac
if systemctl is-active --quiet rascal@blue; then
  printf 'blue'
  exit 0
fi
if systemctl is-active --quiet rascal@green; then
  printf 'green'
  exit 0
fi
printf 'blue'
`)+"\n")
	if err != nil {
		return "", fmt.Errorf("detect active slot: %w", err)
	}
	slot := strings.TrimSpace(out)
	if slot != SlotBlue && slot != SlotGreen {
		return "", fmt.Errorf("invalid active slot %q", slot)
	}
	return slot, nil
}

func otherSlot(slot string) string {
	if strings.TrimSpace(slot) == SlotBlue {
		return SlotGreen
	}
	return SlotBlue
}

func portForSlot(slot string) int {
	if strings.TrimSpace(slot) == SlotGreen {
		return SlotGreenPort
	}
	return SlotBluePort
}

func rollback(cfg Config, activeSlot, inactiveSlot string, activePort int) error {
	script := fmt.Sprintf(strings.TrimSpace(`
set -eu
cat >/etc/caddy/rascal-upstream.caddy <<'EOF_ROLLBACK'
reverse_proxy 127.0.0.1:%d
EOF_ROLLBACK
(systemctl reload caddy || systemctl restart caddy) || true
systemctl stop "rascal@%s" || true
systemctl restart "rascal@%s" || true
`)+"\n", activePort, inactiveSlot, activeSlot)
	return runRemoteScript(cfg, script)
}

func verifyProxyReadiness(cfg Config) error {
	domain := strings.TrimSpace(cfg.Domain)
	if domain != "" {
		if err := runRemoteScript(cfg, fmt.Sprintf(strings.TrimSpace(`
set -eu
healthy=0
for _ in $(seq 1 30); do
  if curl -fsS --resolve %s:443:127.0.0.1 https://%s/readyz >/dev/null; then
    healthy=1
    break
  fi
  sleep 1
done
if [ "$healthy" -ne 1 ]; then
  echo "proxy readiness check failed on caddy; rolling back" >&2
  exit 1
fi
`)+"\n", shellSingleQuote(domain), shellSingleQuote(domain))); err != nil {
			return fmt.Errorf("proxy readiness check failed on caddy: %w", err)
		}
		return nil
	}
	checkScript := fmt.Sprintf(strings.TrimSpace(`
set -eu
if grep -Fq ':%d {' /etc/caddy/Caddyfile 2>/dev/null; then
  healthy=0
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 5 http://127.0.0.1:%d/readyz >/dev/null 2>&1; then
      healthy=1
      break
    fi
    sleep 1
  done
  if [ "$healthy" -ne 1 ]; then
    echo "proxy readiness check failed on caddy; rolling back" >&2
    exit 1
  fi
else
  echo "caddy has no :%d site; skipping local proxy probe" >&2
fi
`)+"\n", ProxyPort, ProxyPort, ProxyPort)
	if err := runRemoteScript(cfg, checkScript); err != nil {
		return fmt.Errorf("proxy readiness check failed on caddy: %w", err)
	}
	return nil
}

func sshArgs(cfg Config, remoteCmd string) []string {
	args := []string{"-p", fmt.Sprintf("%d", cfg.SSHPort), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}
	if keyPath := normalizedSSHKeyPath(cfg.SSHKeyPath); keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, fmt.Sprintf("%s@%s", cfg.SSHUser, cfg.Host), remoteCmd)
	return args
}

func scpArgs(cfg Config, source, target string) []string {
	args := []string{"-P", fmt.Sprintf("%d", cfg.SSHPort), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}
	if keyPath := normalizedSSHKeyPath(cfg.SSHKeyPath); keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, source, target)
	return args
}

func normalizedSSHKeyPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	expanded, err := expandPath(path)
	if err != nil {
		return path
	}
	return expanded
}

func remoteTarget(cfg Config, path string) string {
	return fmt.Sprintf("%s@%s:%s", cfg.SSHUser, cfg.Host, path)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func serverEnvFile(cfg Config) string {
	backend := agent.NormalizeBackend(string(cfg.AgentBackend))
	gooseImage := strings.TrimSpace(cfg.RunnerImageGoose)
	if gooseImage == "" {
		gooseImage = firstNonEmpty(strings.TrimSpace(cfg.RunnerImage), defaults.GooseRunnerImageTag)
	}
	codexImage := firstNonEmpty(strings.TrimSpace(cfg.RunnerImageCodex), defaults.CodexRunnerImageTag)

	return fmt.Sprintf(strings.TrimSpace(`
RASCAL_LISTEN_ADDR=%s
RASCAL_DATA_DIR=%s
RASCAL_STATE_PATH=%s
RASCAL_API_TOKEN=%s
RASCAL_GITHUB_TOKEN=%s
RASCAL_GITHUB_WEBHOOK_SECRET=%s
RASCAL_RUNNER_MODE=%s
RASCAL_AGENT_BACKEND=%s
RASCAL_RUNNER_IMAGE_GOOSE=%s
RASCAL_RUNNER_IMAGE_CODEX=%s
RASCAL_RUNNER_MAX_ATTEMPTS=1
RASCAL_AGENT_SESSION_MODE=all
RASCAL_AGENT_SESSION_ROOT=%s
RASCAL_AGENT_SESSION_TTL_DAYS=14
	`)+"\n",
		cfg.ServerListenAddr,
		cfg.ServerDataDir,
		cfg.ServerStatePath,
		cfg.APIToken,
		cfg.GitHubRuntimeToken,
		cfg.WebhookSecret,
		cfg.RunnerMode,
		backend,
		gooseImage,
		codexImage,
		filepath.Join(cfg.ServerDataDir, defaults.AgentSessionDirName),
	)
}

func systemdServiceContent() string {
	return strings.TrimSpace(`
[Unit]
Description=Rascal orchestrator service
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/rascal/rascal.env
EnvironmentFile=-/etc/rascal/rascal-%i.env
ExecStart=/opt/rascal/rascald
Restart=always
RestartSec=3
KillSignal=SIGTERM
KillMode=mixed
TimeoutStopSec=330
User=root
WorkingDirectory=/opt/rascal

[Install]
WantedBy=multi-user.target
`) + "\n"
}

func renderCaddyfile(domain string) (string, error) {
	domain = strings.TrimSpace(domain)
	localProxyBlock := fmt.Sprintf(`
:%d {
  import rascal_common
}
`, ProxyPort)
	if domain != "" {
		localProxyBlock = ""
	}
	domainBlock := ""
	if domain != "" {
		domainBlock = fmt.Sprintf(`
%s {
  import rascal_common
}
`, domain)
	}

	templateBytes, err := assetsFS.ReadFile("assets/Caddyfile.tmpl")
	if err != nil {
		return "", fmt.Errorf("read embedded caddy template: %w", err)
	}
	out := strings.ReplaceAll(string(templateBytes), "{{DOMAIN_BLOCK}}", strings.TrimSpace(domainBlock))
	out = strings.ReplaceAll(out, "{{LOCAL_PROXY_BLOCK}}", strings.TrimSpace(localProxyBlock))
	return strings.TrimSpace(out) + "\n", nil
}

func expandPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func repoRootPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
