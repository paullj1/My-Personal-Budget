package auth

import (
	"context"
)

type contextKey string

const userIDKey contextKey = "userID"

// WithUserID attaches the authenticated user ID to the context.
func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserIDFromContext returns the user ID if present.
func UserIDFromContext(ctx context.Context) *int64 {
	if ctx == nil {
		return nil
	}
	if val, ok := ctx.Value(userIDKey).(int64); ok {
		return &val
	}
	return nil
}
