package config

import (
	"testing"
	"time"
)

func TestFromEnv(t *testing.T) {
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("PORT", "9999")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.example, https://b.example")
	t.Setenv("API_TOKEN", "token")
	t.Setenv("JWT_SECRET", "secret")
	t.Setenv("STATIC_DIR", "/tmp/static")
	t.Setenv("DB_CONNECT_RETRIES", "5")
	t.Setenv("DB_CONNECT_INTERVAL_MS", "250")
	t.Setenv("RELYING_PARTY_ID", "example.com")
	t.Setenv("RELYING_PARTY_NAME", "Budget")

	cfg := FromEnv()
	if cfg.Host != "127.0.0.1" || cfg.Port != "9999" {
		t.Fatalf("unexpected host/port: %s:%s", cfg.Host, cfg.Port)
	}
	if cfg.DBURL != "postgres://example" {
		t.Fatalf("unexpected db url: %s", cfg.DBURL)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "https://a.example" || cfg.AllowedOrigins[1] != "https://b.example" {
		t.Fatalf("unexpected origins: %v", cfg.AllowedOrigins)
	}
	if cfg.APIToken != "token" || cfg.JWTSecret != "secret" || cfg.StaticDir != "/tmp/static" {
		t.Fatalf("unexpected tokens or static dir")
	}
	if cfg.DBConnectRetries != 5 || cfg.DBConnectInterval != 250*time.Millisecond {
		t.Fatalf("unexpected db retry config: %d/%s", cfg.DBConnectRetries, cfg.DBConnectInterval)
	}
	if cfg.RelyingPartyID != "example.com" || cfg.RelyingPartyName != "Budget" {
		t.Fatalf("unexpected relying party: %s/%s", cfg.RelyingPartyID, cfg.RelyingPartyName)
	}
}

func TestFromEnv_Defaults(t *testing.T) {
	t.Setenv("HOST", "")
	t.Setenv("PORT", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	t.Setenv("API_TOKEN", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("STATIC_DIR", "")
	t.Setenv("DB_CONNECT_RETRIES", "")
	t.Setenv("DB_CONNECT_INTERVAL_MS", "")
	t.Setenv("RELYING_PARTY_ID", "")
	t.Setenv("RELYING_PARTY_NAME", "")

	cfg := FromEnv()
	if cfg.Host == "" || cfg.Port == "" {
		t.Fatalf("expected defaults for host/port")
	}
	if cfg.DBConnectRetries == 0 || cfg.DBConnectInterval == 0 {
		t.Fatalf("expected default db retry config")
	}
	if cfg.RelyingPartyID == "" || cfg.RelyingPartyName == "" {
		t.Fatalf("expected defaults for relying party")
	}
}
