package worker

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGitHubCLIEnv(t *testing.T) {
	t.Parallel()

	got := githubCLIEnv("token-123")
	if len(got) != 1 || got[0] != "GH_TOKEN=token-123" {
		t.Fatalf("githubCLIEnv() = %#v", got)
	}
	if got := githubCLIEnv(""); got != nil {
		t.Fatalf("githubCLIEnv(\"\") = %#v, want nil", got)
	}
}

func TestGitHubRemoteEnv(t *testing.T) {
	t.Parallel()

	got := gitHubRemoteEnv("token-123")
	wantHeader := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:token-123"))
	want := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=" + wantHeader,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("gitHubRemoteEnv() = %#v, want %#v", got, want)
	}
}
