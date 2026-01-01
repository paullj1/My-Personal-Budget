package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestGenerateAndParseToken(t *testing.T) {
	secret := "secret"
	token, err := GenerateToken(99, secret, time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken error: %v", err)
	}

	userID, err := ParseToken(token, secret)
	if err != nil {
		t.Fatalf("ParseToken error: %v", err)
	}
	if userID != 99 {
		t.Fatalf("expected user id 99, got %d", userID)
	}
}

func TestGenerateToken_RequiresSecret(t *testing.T) {
	if _, err := GenerateToken(1, "", time.Minute); err == nil {
		t.Fatalf("expected error for empty secret")
	}
}

func TestParseToken_RequiresSecret(t *testing.T) {
	if _, err := ParseToken("token", ""); err == nil {
		t.Fatalf("expected error for empty secret")
	}
}

func TestParseToken_InvalidSubject(t *testing.T) {
	secret := "secret"
	claims := jwt.RegisteredClaims{
		Subject:   "not-a-number",
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("SignedString error: %v", err)
	}

	if _, err := ParseToken(tokenStr, secret); err == nil {
		t.Fatalf("expected error for invalid subject")
	}
}

func TestParseToken_RejectsNonHMAC(t *testing.T) {
	secret := "secret"
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("GenerateKey error: %v", err)
	}
	claims := jwt.RegisteredClaims{
		Subject:   "1",
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenStr, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("SignedString error: %v", err)
	}

	if _, err := ParseToken(tokenStr, secret); err == nil {
		t.Fatalf("expected error for non-HMAC signing method")
	}
}
