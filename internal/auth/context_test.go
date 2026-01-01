package auth

import (
	"context"
	"testing"
)

func TestUserIDContext(t *testing.T) {
	if UserIDFromContext(nil) != nil {
		t.Fatalf("expected nil for nil context")
	}

	ctx := context.Background()
	if UserIDFromContext(ctx) != nil {
		t.Fatalf("expected nil for empty context")
	}

	ctx = WithUserID(ctx, 42)
	userID := UserIDFromContext(ctx)
	if userID == nil || *userID != 42 {
		t.Fatalf("expected user id 42, got %v", userID)
	}
}
