package oauth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// PendingRequest is a validated authorization request waiting on the user.
//
// It is held server-side rather than round-tripped through the browser so the
// consent screen cannot be handed different parameters than the ones that were
// checked -- the redirect URI in particular.
type PendingRequest struct {
	ClientID            string
	ClientName          string
	ClientURI           string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	Resource            string
	ExpiresAt           time.Time
}

// PendingStore keeps authorization requests between /oauth/authorize and the
// user's decision. In-process is the right scope: these live for seconds, and a
// restart mid-consent just means the client retries.
type PendingStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]PendingRequest
}

// NewPendingStore returns a store whose entries expire after ttl.
func NewPendingStore(ttl time.Duration) *PendingStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &PendingStore{ttl: ttl, items: make(map[string]PendingRequest)}
}

// Put stores a request and returns its opaque id.
func (s *PendingStore) Put(req PendingRequest) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	req.ExpiresAt = time.Now().Add(s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.items[id] = req
	return id, nil
}

// Peek returns a request without consuming it, so the consent screen can render
// before the user has decided.
func (s *PendingStore) Peek(id string) (PendingRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.items[id]
	if !ok || time.Now().After(req.ExpiresAt) {
		delete(s.items, id)
		return PendingRequest{}, false
	}
	return req, true
}

// Consume returns a request and removes it, so one consent yields one code.
func (s *PendingStore) Consume(id string) (PendingRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.items[id]
	delete(s.items, id)
	if !ok || time.Now().After(req.ExpiresAt) {
		return PendingRequest{}, false
	}
	return req, true
}

// sweepLocked drops expired entries. Called on write, which is often enough for
// a map that only ever holds in-flight consents.
func (s *PendingStore) sweepLocked() {
	now := time.Now()
	for id, req := range s.items {
		if now.After(req.ExpiresAt) {
			delete(s.items, id)
		}
	}
}
