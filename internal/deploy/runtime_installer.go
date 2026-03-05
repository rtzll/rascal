package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type runtimeArtifacts struct {
	RunnerBinaryPath  string
	RunnerArchivePath string
	InstallDockerPath string
}

type baseArtifacts struct {
	RascaldRemotePath string
	ServiceRemotePath string
}

type runtimeInstaller interface {
	Kind() string
	PlanSteps() []string
	PrepareArtifacts(cfg Config, tmpDir string) (runtimeArtifacts, error)
	Uploads(art runtimeArtifacts) []remoteUpload
	EnsureDependenciesScript(cfg Config, art runtimeArtifacts) (string, error)
	InstallScript(cfg Config, base baseArtifacts, art runtimeArtifacts) (string, error)
}

func runtimeInstallerFor(cfg Config) (runtimeInstaller, error) {
	runtime := strings.ToLower(strings.TrimSpace(cfg.RunnerRuntime))
	switch runtime {
	case "", "docker":
		return dockerRuntimeInstaller{}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q for deploy", cfg.RunnerRuntime)
	}
}

type dockerRuntimeInstaller struct{}

func (dockerRuntimeInstaller) Kind() string {
	return "docker"
}

func (dockerRuntimeInstaller) PlanSteps() []string {
	return []string{"ensure_dependencies", "build_runner_image"}
}

func (dockerRuntimeInstaller) PrepareArtifacts(cfg Config, tmpDir string) (runtimeArtifacts, error) {
	runnerBinaryPath := filepath.Join(tmpDir, "rascal-runner")
	if err := buildLinuxRascalRunner(runnerBinaryPath, cfg.GOARCH); err != nil {
		return runtimeArtifacts{}, err
	}
	runnerArchivePath := filepath.Join(tmpDir, "runner.tgz")
	if err := runLocalInDir(repoRootPath(), "tar", "-C", repoRootPath(), "-czf", runnerArchivePath, "runner"); err != nil {
		return runtimeArtifacts{}, fmt.Errorf("package runner assets: %w", err)
	}
	installDocker, err := assetsFS.ReadFile("assets/install_docker.sh")
	if err != nil {
		return runtimeArtifacts{}, fmt.Errorf("read embedded install_docker.sh: %w", err)
	}
	installDockerPath := filepath.Join(tmpDir, "install_docker.sh")
	if err := os.WriteFile(installDockerPath, installDocker, 0o700); err != nil {
		return runtimeArtifacts{}, fmt.Errorf("write install_docker.sh: %w", err)
	}
	return runtimeArtifacts{
		RunnerBinaryPath:  runnerBinaryPath,
		RunnerArchivePath: runnerArchivePath,
		InstallDockerPath: installDockerPath,
	}, nil
}

func (dockerRuntimeInstaller) Uploads(art runtimeArtifacts) []remoteUpload {
	return []remoteUpload{
		{LocalPath: art.RunnerBinaryPath, RemotePath: "/tmp/rascal-bootstrap/rascal-runner"},
		{LocalPath: art.RunnerArchivePath, RemotePath: "/tmp/rascal-bootstrap/runner.tgz"},
		{LocalPath: art.InstallDockerPath, RemotePath: "/tmp/rascal-bootstrap/install_docker.sh"},
	}
}

func (dockerRuntimeInstaller) EnsureDependenciesScript(_ Config, _ runtimeArtifacts) (string, error) {
	return "set -eu\nchmod +x /tmp/rascal-bootstrap/install_docker.sh\n/tmp/rascal-bootstrap/install_docker.sh\n", nil
}

func (dockerRuntimeInstaller) InstallScript(cfg Config, base baseArtifacts, _ runtimeArtifacts) (string, error) {
	return fmt.Sprintf(strings.TrimSpace(`
set -eu
mkdir -p /opt/rascal /etc/rascal
tar -xzf /tmp/rascal-bootstrap/runner.tgz -C /opt/rascal
install -m 0755 /tmp/rascal-bootstrap/rascal-runner /opt/rascal/runner/rascal-runner
docker build -t %s /opt/rascal/runner
install -m 0755 %s /opt/rascal/rascald
install -m 0644 %s /etc/systemd/system/rascal@.service
`)+"\n", shellSingleQuote(cfg.RunnerArtifactRef), shellSingleQuote(base.RascaldRemotePath), shellSingleQuote(base.ServiceRemotePath)), nil
}
