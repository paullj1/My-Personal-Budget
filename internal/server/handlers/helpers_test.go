package handlers

import (
	"encoding/base64"
	"testing"

	"my-personal-budget/internal/config"
)

func TestDecodeBase64URL(t *testing.T) {
	value := []byte{1, 2, 3}
	raw := base64.RawURLEncoding.EncodeToString(value)
	padded := base64.URLEncoding.EncodeToString(value)

	decodedRaw, err := decodeBase64URL(raw)
	if err != nil {
		t.Fatalf("raw decode error: %v", err)
	}
	if string(decodedRaw) != string(value) {
		t.Fatalf("expected %v, got %v", value, decodedRaw)
	}

	decodedPadded, err := decodeBase64URL(padded)
	if err != nil {
		t.Fatalf("padded decode error: %v", err)
	}
	if string(decodedPadded) != string(value) {
		t.Fatalf("expected %v, got %v", value, decodedPadded)
	}

	if _, err := decodeBase64URL(""); err == nil {
		t.Fatalf("expected error for empty value")
	}
	if _, err := decodeBase64URL("$$$"); err == nil {
		t.Fatalf("expected error for invalid base64")
	}
}

func TestRpOrigins(t *testing.T) {
	cfg := config.Config{AllowedOrigins: []string{"https://example.com"}}
	origins := rpOrigins(cfg)
	if len(origins) != 1 || origins[0] != "https://example.com" {
		t.Fatalf("expected allowed origins, got %v", origins)
	}

	cfg = config.Config{RelyingPartyID: "budget.local"}
	origins = rpOrigins(cfg)
	if len(origins) != 2 {
		t.Fatalf("expected 2 defaults, got %v", origins)
	}
	if origins[0] != "https://budget.local" || origins[1] != "http://budget.local" {
		t.Fatalf("unexpected defaults: %v", origins)
	}
}
