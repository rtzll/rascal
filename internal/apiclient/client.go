package apiclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	deployengine "github.com/rtzll/rascal/internal/deploy"
	"github.com/rtzll/rascal/internal/remote"
)

type Client struct {
	BaseURL   string
	Token     string
	HTTP      *http.Client
	Transport string
	SSHHost   string
	SSHUser   string
	SSHKey    string
	SSHPort   int
}

func New(baseURL, token, transport, sshHost, sshUser, sshKey string, sshPort int) Client {
	return Client{
		BaseURL:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:     strings.TrimSpace(token),
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Transport: strings.TrimSpace(transport),
		SSHHost:   strings.TrimSpace(sshHost),
		SSHUser:   strings.TrimSpace(sshUser),
		SSHKey:    strings.TrimSpace(sshKey),
		SSHPort:   sshPort,
	}
}

func DoJSON[T any](client Client, method, path string, payload T) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return client.Do(method, path, bytes.NewReader(data))
}

func (c Client) Do(method, path string, body io.Reader) (*http.Response, error) {
	if strings.EqualFold(c.Transport, "ssh") {
		return c.doOverSSH(method, path, body)
	}
	return c.doOverHTTP(method, path, body)
}

func (c Client) doOverHTTP(method, path string, body io.Reader) (*http.Response, error) {
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequest(method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

func (c Client) doOverSSH(method, path string, body io.Reader) (*http.Response, error) {
	sshHost := strings.TrimSpace(c.SSHHost)
	if sshHost == "" {
		return nil, fmt.Errorf("ssh transport selected but ssh host is missing")
	}

	var payload []byte
	if body != nil {
		data, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		payload = data
	}

	curlArgs := []string{
		"curl", "-sS", "-i", "--raw",
		"-X", remote.ShellQuote(strings.TrimSpace(method)),
		"-H", remote.ShellQuote("Accept: application/json"),
	}
	if c.Token != "" {
		curlArgs = append(curlArgs, "-H", remote.ShellQuote("Authorization: Bearer "+c.Token))
	}
	if len(payload) > 0 {
		curlArgs = append(curlArgs, "-H", remote.ShellQuote("Content-Type: application/json"), "--data-binary", "@-")
	}
	curlCmd := strings.Join(curlArgs, " ")
	remoteCmd := remote.Script(
		"set -eu",
		"slot=''",
		"if [ -f /etc/rascal/active_slot ]; then slot=$(tr -d '[:space:]' </etc/rascal/active_slot); fi",
		"case \"$slot\" in",
		fmt.Sprintf("  %s) port=%d ;;", deployengine.SlotBlue, deployengine.SlotBluePort),
		fmt.Sprintf("  %s) port=%d ;;", deployengine.SlotGreen, deployengine.SlotGreenPort),
		fmt.Sprintf("  *) if systemctl is-active --quiet 'rascal@green'; then port=%d; elif systemctl is-active --quiet 'rascal@blue'; then port=%d; else port=%d; fi ;;", deployengine.SlotGreenPort, deployengine.SlotBluePort, deployengine.SlotBluePort),
		"esac",
		"url=$(printf 'http://127.0.0.1:%s%s' \"$port\" "+remote.ShellQuote(path)+")",
		curlCmd+" \"$url\"",
	)

	cfg := remote.SSHConfig{
		Host:    sshHost,
		User:    strings.TrimSpace(c.SSHUser),
		KeyPath: strings.TrimSpace(c.SSHKey),
		Port:    c.SSHPort,
	}
	cmd := exec.Command("ssh", remote.SSHArgs(cfg, remoteCmd)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(payload) > 0 {
		cmd.Stdin = bytes.NewReader(payload)
	}
	if err := cmd.Run(); err != nil {
		errOut := strings.TrimSpace(stderr.String())
		if errOut == "" {
			errOut = strings.TrimSpace(stdout.String())
		}
		if errOut == "" {
			return nil, fmt.Errorf("ssh request failed: %w", err)
		}
		return nil, fmt.Errorf("ssh request failed: %w (%s)", err, errOut)
	}

	resp, err := ParseRawHTTPResponse(stdout.Bytes(), method)
	if err != nil {
		errOut := strings.TrimSpace(stderr.String())
		if errOut != "" {
			return nil, fmt.Errorf("parse ssh response: %w (%s)", err, errOut)
		}
		return nil, fmt.Errorf("parse ssh response: %w", err)
	}
	return resp, nil
}

func ParseRawHTTPResponse(raw []byte, method string) (*http.Response, error) {
	reader := bufio.NewReader(bytes.NewReader(raw))
	req := &http.Request{Method: method}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, fmt.Errorf("parse raw HTTP response: %w", err)
	}
	return resp, nil
}
