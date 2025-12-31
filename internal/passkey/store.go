package passkey

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"

	"github.com/go-webauthn/webauthn/webauthn"
)

type ChallengeStore struct {
	mu             sync.Mutex
	regChallenges  map[string]string
	authChallenges map[string]string
	regSessions    map[string]webauthn.SessionData
	authSessions   map[string]webauthn.SessionData
	authSessionsByID map[string]webauthn.SessionData
}

func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{
		regChallenges:  make(map[string]string),
		authChallenges: make(map[string]string),
		regSessions:    make(map[string]webauthn.SessionData),
		authSessions:   make(map[string]webauthn.SessionData),
		authSessionsByID: make(map[string]webauthn.SessionData),
	}
}

func (s *ChallengeStore) NewRegistrationChallenge(email string) (string, error) {
	chal, err := randomBase64(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.regChallenges[email] = chal
	s.mu.Unlock()
	return chal, nil
}

func (s *ChallengeStore) ConsumeRegistration(email, challenge string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expected, ok := s.regChallenges[email]
	if !ok || expected != challenge {
		return false
	}
	delete(s.regChallenges, email)
	return true
}

func (s *ChallengeStore) NewAuthChallenge(email string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chal, err := randomBase64(32)
	if err != nil {
		return "", err
	}
	s.authChallenges[email] = chal
	return chal, nil
}

func (s *ChallengeStore) ConsumeAuth(email, challenge string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expectedChallenge, ok := s.authChallenges[email]
	if !ok || expectedChallenge != challenge {
		return false
	}
	delete(s.authChallenges, email)
	return true
}

func (s *ChallengeStore) SaveRegistrationSession(email string, data webauthn.SessionData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regSessions[email] = data
}

func (s *ChallengeStore) ConsumeRegistrationSession(email string) (webauthn.SessionData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.regSessions[email]
	if ok {
		delete(s.regSessions, email)
	}
	return data, ok
}

func (s *ChallengeStore) SaveAuthSession(email string, data webauthn.SessionData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authSessions[email] = data
}

func (s *ChallengeStore) ConsumeAuthSession(email string) (webauthn.SessionData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.authSessions[email]
	if ok {
		delete(s.authSessions, email)
	}
	return data, ok
}

func (s *ChallengeStore) SaveAuthSessionByID(data webauthn.SessionData) (string, error) {
	id, err := randomBase64(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authSessionsByID[id] = data
	return id, nil
}

func (s *ChallengeStore) ConsumeAuthSessionByID(id string) (webauthn.SessionData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.authSessionsByID[id]
	if ok {
		delete(s.authSessionsByID, id)
	}
	return data, ok
}

func randomBase64(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
