package receipt

import (
	"crypto/rand"
	"encoding/base64"
	"sort"
	"sync"
	"time"
)

// Draft is an allocated receipt held between extraction and commit.
//
// It exists so a caller never has to send the computed amounts back. The cents
// this package worked out stay on this side; the caller returns only its budget
// choices, and a client that miscopies a number cannot corrupt the ledger by
// doing so.
type Draft struct {
	UserID      int64
	Alloc       Allocation
	Extraction  Extraction
	Suggestions map[string]int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// DraftStore keeps drafts in memory, bounded in both age and count.
//
// In-process is the right scope: a draft is worthless once reviewed, and losing
// one to a restart costs a re-scan. The count bound matters more than it looks
// on the small host this runs on -- a long grocery receipt is a few hundred
// lines of struct, and an abandoned-draft leak would be a slow memory leak in a
// process already running under a soft memory limit.
type DraftStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	items map[string]Draft
}

// DefaultDraftTTL is how long a draft waits to be reviewed.
const DefaultDraftTTL = 30 * time.Minute

// NewDraftStore returns a store holding at most max drafts for ttl each.
func NewDraftStore(ttl time.Duration, max int) *DraftStore {
	if ttl <= 0 {
		ttl = DefaultDraftTTL
	}
	if max <= 0 {
		max = 64
	}
	return &DraftStore{ttl: ttl, max: max, items: make(map[string]Draft)}
}

// Put stores a draft and returns its id.
func (s *DraftStore) Put(d Draft) (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := "draft_" + base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	d.CreatedAt = now
	d.ExpiresAt = now.Add(s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	s.items[id] = d
	return id, nil
}

// Get returns a draft belonging to userID. A draft is never visible to anyone
// else, so a guessed id leaks nothing.
func (s *DraftStore) Get(id string, userID int64) (Draft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.items[id]
	if !ok || time.Now().After(d.ExpiresAt) {
		delete(s.items, id)
		return Draft{}, false
	}
	if d.UserID != userID {
		return Draft{}, false
	}
	return d, true
}

// Delete drops a draft, called once it has been committed.
func (s *DraftStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
}

// sweepLocked drops expired drafts, then the oldest survivors if the store is
// still over its cap. Evicting the oldest is right here: the newest draft is the
// one somebody is looking at.
func (s *DraftStore) sweepLocked(now time.Time) {
	for id, d := range s.items {
		if now.After(d.ExpiresAt) {
			delete(s.items, id)
		}
	}
	if len(s.items) < s.max {
		return
	}
	ids := make([]string, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.items[ids[i]].CreatedAt.Before(s.items[ids[j]].CreatedAt)
	})
	for _, id := range ids[:len(s.items)-s.max+1] {
		delete(s.items, id)
	}
}
