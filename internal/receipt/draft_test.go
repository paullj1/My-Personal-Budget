package receipt

import (
	"testing"
	"time"
)

func TestDraftStoreRoundTrip(t *testing.T) {
	s := NewDraftStore(time.Minute, 8)

	id, err := s.Put(Draft{UserID: 1, Alloc: Allocation{Merchant: "Target", TotalCents: 7490}})
	if err != nil {
		t.Fatalf("Put error: %v", err)
	}

	got, ok := s.Get(id, 1)
	if !ok {
		t.Fatal("draft not found")
	}
	if got.Alloc.TotalCents != 7490 {
		t.Fatalf("total_cents: got %d", got.Alloc.TotalCents)
	}

	s.Delete(id)
	if _, ok := s.Get(id, 1); ok {
		t.Fatal("draft survived deletion")
	}
}

// A draft is private to its owner, so a guessed id leaks nothing.
func TestDraftStoreIsScopedToItsOwner(t *testing.T) {
	s := NewDraftStore(time.Minute, 8)
	id, _ := s.Put(Draft{UserID: 1})

	if _, ok := s.Get(id, 2); ok {
		t.Fatal("another user read someone else's draft")
	}
	if _, ok := s.Get(id, 1); !ok {
		t.Fatal("a failed cross-user read consumed the draft")
	}
}

func TestDraftStoreExpires(t *testing.T) {
	s := NewDraftStore(time.Nanosecond, 8)
	id, _ := s.Put(Draft{UserID: 1})
	time.Sleep(time.Millisecond)

	if _, ok := s.Get(id, 1); ok {
		t.Fatal("an expired draft was still readable")
	}
}

// The cap is what keeps abandoned drafts from becoming a slow leak in a process
// running under a soft memory limit.
func TestDraftStoreEvictsOldestOverCap(t *testing.T) {
	const max = 4
	s := NewDraftStore(time.Minute, max)

	ids := make([]string, 0, max+2)
	for i := 0; i < max+2; i++ {
		id, err := s.Put(Draft{UserID: 1})
		if err != nil {
			t.Fatalf("Put error: %v", err)
		}
		ids = append(ids, id)
		// CreatedAt drives eviction order, and the clock's resolution has to be
		// able to tell two consecutive Puts apart.
		time.Sleep(time.Millisecond)
	}

	if len(s.items) > max {
		t.Fatalf("store holds %d drafts, over its cap of %d", len(s.items), max)
	}
	// The newest is the one somebody is looking at.
	if _, ok := s.Get(ids[len(ids)-1], 1); !ok {
		t.Fatal("the newest draft was evicted")
	}
	if _, ok := s.Get(ids[0], 1); ok {
		t.Fatal("the oldest draft survived eviction")
	}
}
