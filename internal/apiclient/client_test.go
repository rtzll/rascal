package apiclient

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewTrimsInputs(t *testing.T) {
	client := New(" https://rascal.example.com/ ", " token ", " ssh ", " host ", " user ", " ~/.ssh/key ", 2200)

	if client.BaseURL != "https://rascal.example.com" {
		t.Fatalf("BaseURL = %q, want trimmed URL", client.BaseURL)
	}
	if client.Token != "token" || client.Transport != "ssh" || client.SSHHost != "host" || client.SSHUser != "user" || client.SSHKey != "~/.ssh/key" || client.SSHPort != 2200 {
		t.Fatalf("New() trimmed fields incorrectly: %#v", client)
	}
	if client.HTTP == nil {
		t.Fatalf("New() should initialize HTTP client")
	}
}

func TestDoOverHTTPSetsAuthHeadersAndBody(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotContentType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotBody = string(body)
		w.WriteHeader(http.StatusAccepted)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}))
	defer server.Close()

	client := New(server.URL+"/", "token-123", "http", "", "", "", 0)
	resp, err := client.Do(http.MethodPost, "/v1/runs", bytes.NewBufferString(`{"run":1}`))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/runs" {
		t.Fatalf("request = %s %s, want POST /v1/runs", gotMethod, gotPath)
	}
	if gotAuth != "Bearer token-123" {
		t.Fatalf("Authorization = %q, want bearer token", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody != `{"run":1}` {
		t.Fatalf("body = %q, want request JSON", gotBody)
	}
}

func TestDoOverSSHRequiresHost(t *testing.T) {
	client := New("https://rascal.example.com", "token", "ssh", "", "root", "~/.ssh/id_ed25519", 22)

	resp, err := client.doOverSSH(http.MethodGet, "/v1/runs", nil)
	if resp != nil {
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		}()
	}
	if err == nil {
		t.Fatalf("Do() error = nil, want ssh host error")
	}
	if got := err.Error(); got != "ssh transport selected but ssh host is missing" {
		t.Fatalf("Do() error = %q, want missing host message", got)
	}
}

func TestParseRawHTTPResponse(t *testing.T) {
	raw := []byte("HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nContent-Length: 12\r\n\r\n{\"ok\":true}\n")

	resp, err := ParseRawHTTPResponse(raw, http.MethodPost)
	if err != nil {
		t.Fatalf("ParseRawHTTPResponse() error = %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", resp.Header.Get("Content-Type"))
	}
	if string(body) != "{\"ok\":true}\n" {
		t.Fatalf("body = %q, want JSON payload", string(body))
	}
}
