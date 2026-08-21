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

	// Receipt scanning. ReceiptOCRURL empty disables the feature entirely: the
	// scan endpoint returns 503 and the UI hides its button, leaving manual
	// itemizing untouched.
	ReceiptOCRURL     string
	ReceiptOCRModel   string
	ReceiptOCRToken   string
	ReceiptOCRNumCtx  int
	ReceiptOCRTimeout time.Duration
	ReceiptMaxEdge    int
	ReceiptMaxBytes   int64
}

// ReceiptScanEnabled reports whether an inference endpoint is configured.
func (c Config) ReceiptScanEnabled() bool {
	return c.ReceiptOCRURL != ""
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
	ocrURL := strings.TrimSuffix(strings.TrimSpace(os.Getenv("RECEIPT_OCR_URL")), "/")
	ocrModel := envDefault("RECEIPT_OCR_MODEL", "qwen3.8:27b")
	ocrToken := os.Getenv("RECEIPT_OCR_TOKEN")
	// Ollama defaults to ~4096 and truncates silently, which looks like a bad
	// model rather than a config error. Keep this well clear of the image tokens.
	ocrNumCtx := envInt("RECEIPT_OCR_NUM_CTX", 32768)
	ocrTimeout := envDuration("RECEIPT_OCR_TIMEOUT_MS", 120*time.Second)
	// Bounds the long edge. A receipt usually fills only part of the frame, so a
	// 1600px long edge leaves its print too small to read reliably; 2048 is the
	// cheapest bound that still reads, since 2048 and native hit the same
	// vision-token ceiling.
	maxEdge := envInt("RECEIPT_MAX_EDGE", 2048)
	maxBytes := int64(envInt("RECEIPT_MAX_IMAGE_BYTES", 16<<20))

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
		ReceiptOCRURL:     ocrURL,
		ReceiptOCRModel:   ocrModel,
		ReceiptOCRToken:   ocrToken,
		ReceiptOCRNumCtx:  ocrNumCtx,
		ReceiptOCRTimeout: ocrTimeout,
		ReceiptMaxEdge:    maxEdge,
		ReceiptMaxBytes:   maxBytes,
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
