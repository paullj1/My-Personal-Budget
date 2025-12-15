package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration for the Go API.
type Config struct {
	Host              string
	Port              string
	DBURL             string
	DBConnectRetries  int
	DBConnectInterval time.Duration
	AllowedOrigins    []string
	APIToken          string
	JWTSecret         string
	StaticDir         string
	RelyingPartyID    string
	RelyingPartyName  string
}

// FromEnv reads configuration from environment variables with sensible defaults.
func FromEnv() Config {
	host := envDefault("HOST", "0.0.0.0")
	port := envDefault("PORT", "8080")
	dbURL := envDefault("DATABASE_URL", "postgres://postgres:budgetpass@localhost:5432/budget?sslmode=disable")
	allowedOrigins := splitAndTrim(os.Getenv("CORS_ALLOWED_ORIGINS"))
	apiToken := os.Getenv("API_TOKEN")
	jwtSecret := os.Getenv("JWT_SECRET")
	staticDir := envDefault("STATIC_DIR", "./static")
	dbRetries := envInt("DB_CONNECT_RETRIES", 10)
	dbInterval := envDuration("DB_CONNECT_INTERVAL_MS", 500*time.Millisecond)
	rpID := envDefault("RELYING_PARTY_ID", "localhost")
	rpName := envDefault("RELYING_PARTY_NAME", "My Personal Budget")

	return Config{
		Host:              host,
		Port:              port,
		DBURL:             dbURL,
		DBConnectRetries:  dbRetries,
		DBConnectInterval: dbInterval,
		AllowedOrigins:    allowedOrigins,
		APIToken:          apiToken,
		JWTSecret:         jwtSecret,
		StaticDir:         staticDir,
		RelyingPartyID:    rpID,
		RelyingPartyName:  rpName,
	}
}

func envDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return time.Duration(parsed) * time.Millisecond
		}
	}
	return fallback
}

func splitAndTrim(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		clean := strings.TrimSpace(part)
		if clean != "" {
			out = append(out, clean)
		}
	}
	return out
}
