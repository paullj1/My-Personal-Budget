package passkey

import (
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestRegistrationChallengeLifecycle(t *testing.T) {
	store := NewChallengeStore()
	chal, err := store.NewRegistrationChallenge("user@example.com")
	if err != nil {
		t.Fatalf("NewRegistrationChallenge error: %v", err)
	}
	if chal == "" {
		t.Fatalf("expected challenge string")
	}
	if !store.ConsumeRegistration("user@example.com", chal) {
		t.Fatalf("expected challenge to validate")
	}
	if store.ConsumeRegistration("user@example.com", chal) {
		t.Fatalf("expected challenge to be single-use")
	}
}

func TestAuthChallengeLifecycle(t *testing.T) {
	store := NewChallengeStore()
	chal, err := store.NewAuthChallenge("user@example.com")
	if err != nil {
		t.Fatalf("NewAuthChallenge error: %v", err)
	}
	if chal == "" {
		t.Fatalf("expected challenge string")
	}
	if !store.ConsumeAuth("user@example.com", chal) {
		t.Fatalf("expected auth challenge to validate")
	}
	if store.ConsumeAuth("user@example.com", chal) {
		t.Fatalf("expected auth challenge to be single-use")
	}
}

func TestSessionLifecycle(t *testing.T) {
	store := NewChallengeStore()
	session := webauthn.SessionData{Challenge: "abc"}
	store.SaveRegistrationSession("user@example.com", session)
	if _, ok := store.ConsumeRegistrationSession("user@example.com"); !ok {
		t.Fatalf("expected registration session")
	}
	if _, ok := store.ConsumeRegistrationSession("user@example.com"); ok {
		t.Fatalf("expected registration session to be single-use")
	}

	store.SaveAuthSession("user@example.com", session)
	if _, ok := store.ConsumeAuthSession("user@example.com"); !ok {
		t.Fatalf("expected auth session")
	}
	if _, ok := store.ConsumeAuthSession("user@example.com"); ok {
		t.Fatalf("expected auth session to be single-use")
	}
}

func TestAuthSessionByID(t *testing.T) {
	store := NewChallengeStore()
	session := webauthn.SessionData{Challenge: "abc"}
	id, err := store.SaveAuthSessionByID(session)
	if err != nil {
		t.Fatalf("SaveAuthSessionByID error: %v", err)
	}
	if id == "" {
		t.Fatalf("expected session id")
	}
	if _, ok := store.ConsumeAuthSessionByID(id); !ok {
		t.Fatalf("expected session by id")
	}
	if _, ok := store.ConsumeAuthSessionByID(id); ok {
		t.Fatalf("expected session by id to be single-use")
	}
}
