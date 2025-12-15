package auth

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GenerateToken returns a signed JWT for the given user id.
func GenerateToken(userID int64, secret string, ttl time.Duration) (string, error) {
	if secret == "" {
		return "", errors.New("JWT secret is required")
	}
	claims := jwt.RegisteredClaims{
		Subject:   formatSubject(userID),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseToken validates a JWT and returns the user id.
func ParseToken(tokenStr, secret string) (int64, error) {
	if secret == "" {
		return 0, errors.New("JWT secret is required")
	}
	token, err := jwt.ParseWithClaims(tokenStr, &jwt.RegisteredClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return 0, err
	}

	claims, ok := token.Claims.(*jwt.RegisteredClaims)
	if !ok || !token.Valid {
		return 0, errors.New("invalid token")
	}
	return parseSubject(claims.Subject)
}

func formatSubject(id int64) string {
	return strconv.FormatInt(id, 10)
}

func parseSubject(sub string) (int64, error) {
	return strconv.ParseInt(sub, 10, 64)
}
