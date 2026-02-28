package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

func TestVerifySignatureSHA256(t *testing.T) {
	t.Parallel()

	secret := []byte("top-secret")
	payload := []byte(`{"hello":"world"}`)

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !VerifySignatureSHA256(secret, payload, sig) {
		t.Fatal("expected signature to verify")
	}
	if VerifySignatureSHA256(secret, payload, fmt.Sprintf("sha256=%s", stringsRepeat("0", 64))) {
		t.Fatal("expected invalid signature to fail")
	}
}

func stringsRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
