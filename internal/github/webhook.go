package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

func DeliveryID(h http.Header) string {
	return strings.TrimSpace(h.Get("X-GitHub-Delivery"))
}

func EventType(h http.Header) string {
	return strings.TrimSpace(h.Get("X-GitHub-Event"))
}

// SignatureSHA256 returns a GitHub-style X-Hub-Signature-256 header value.
func SignatureSHA256(secret, payload []byte) string {
	if len(secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignatureSHA256 validates GitHub's X-Hub-Signature-256 header.
func VerifySignatureSHA256(secret, payload []byte, signatureHeader string) bool {
	if len(secret) == 0 {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(signatureHeader), "=", 2)
	if len(parts) != 2 || parts[0] != "sha256" || parts[1] == "" {
		return false
	}

	sig, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)
	return hmac.Equal(sig, expected)
}
