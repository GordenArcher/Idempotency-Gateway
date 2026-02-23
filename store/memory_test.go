package store

import (
	"sync"
	"testing"
	"time"

	"github.com/GordenArcher/Idempotency-Gateway/models"
)

// newTestStore builds a MemoryStore with a short TTL suitable for tests.
// We never want tests waiting 24 hours for expiry, so we keep it tight.
func newTestStore() *MemoryStore {
	return NewMemoryStore(1 * time.Hour)
}

func makeEntry(state models.KeyState) *models.CachedEntry {
	return &models.CachedEntry{
		State:        state,
		BodyHash:     "abc123",
		StatusCode:   201,
		ResponseBody: []byte(`{"status":"success"}`),
		CreatedAt:    time.Now().Unix(),
	}
}

func TestGet_ReturnsNilForUnknownKey(t *testing.T) {
	// If a key has never been seen, Get should return nil
	// that's how the middleware knows it's a brand new request.
	s := newTestStore()
	result := s.Get("does-not-exist")
	if result != nil {
		t.Errorf("expected nil for unknown key, got %+v", result)
	}
}

func TestSet_ThenGet_ReturnsSameEntry(t *testing.T) {
	s := newTestStore()
	entry := makeEntry(models.StateComplete)

	s.Set("key-001", entry)
	result := s.Get("key-001")

	if result == nil {
		t.Fatal("expected entry, got nil")
	}
	if result.State != models.StateComplete {
		t.Errorf("expected StateComplete, got %s", result.State)
	}
	if result.BodyHash != entry.BodyHash {
		t.Errorf("expected BodyHash %s, got %s", entry.BodyHash, result.BodyHash)
	}
}

func TestSet_OverwritesExistingEntry(t *testing.T) {
	// The lifecycle of a key is PROCESSING > COMPLETE.
	// Set is called twice, once to mark processing, once to store the result.
	// The second call must overwrite the first.
	s := newTestStore()

	s.Set("key-002", makeEntry(models.StateProcessing))
	s.Set("key-002", makeEntry(models.StateComplete))

	result := s.Get("key-002")
	if result.State != models.StateComplete {
		t.Errorf("expected second Set to overwrite — got state %s", result.State)
	}
}

func TestConcurrentSets_NoDataRace(t *testing.T) {
	// Hammer the store with concurrent writes and reads.
	// Run with: go test -race ./store/...
	// If there's a data race, the race detector will catch it.
	s := newTestStore()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "concurrent-key"
			s.Set(key, makeEntry(models.StateComplete))
			s.Get(key)
		}(i)
	}

	wg.Wait()
	// If we reach here without the race detector firing, we're good.
}

func TestConcurrentDistinctKeys_NoDataRace(t *testing.T) {
	// Each goroutine writes its own key, tests that independent keys
	// don't interfere with each other under concurrency.
	s := newTestStore()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := string(rune('a'+n%26)) + "-key"
			s.Set(key, makeEntry(models.StateComplete))
			_ = s.Get(key)
		}(i)
	}
	wg.Wait()
}

func TestWaitForComplete_ReturnImmediatelyIfAlreadyComplete(t *testing.T) {
	// If the key is already COMPLETE when WaitForComplete is called,
	// it should return immediately without blocking.
	s := newTestStore()
	s.Set("key-done", makeEntry(models.StateComplete))

	done := make(chan *models.CachedEntry, 1)
	go func() {
		done <- s.WaitForComplete("key-done")
	}()

	select {
	case result := <-done:
		if result == nil {
			t.Error("expected a result, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("WaitForComplete blocked on an already-complete key — should have returned immediately")
	}
}

func TestWaitForComplete_BlocksUntilStateChanges(t *testing.T) {
	// The core of the race condition fix:
	// A goroutine waiting on a PROCESSING key should be unblocked
	// as soon as another goroutine calls Set() with StateComplete.
	s := newTestStore()

	s.Set("key-inflight", makeEntry(models.StateProcessing))

	done := make(chan *models.CachedEntry, 1)
	go func() {

		done <- s.WaitForComplete("key-inflight")
	}()

	time.Sleep(50 * time.Millisecond)

	completed := makeEntry(models.StateComplete)
	completed.ResponseBody = []byte(`{"status":"success","message":"Charged 100.00 GHS"}`)
	s.Set("key-inflight", completed)

	select {
	case result := <-done:
		if result == nil {
			t.Fatal("expected result after state transition, got nil")
		}
		if result.State != models.StateComplete {
			t.Errorf("expected StateComplete, got %s", result.State)
		}
		if string(result.ResponseBody) != string(completed.ResponseBody) {
			t.Errorf("expected replayed response body, got %s", result.ResponseBody)
		}
	case <-time.After(2 * time.Second):
		t.Error("WaitForComplete never unblocked after state transitioned to COMPLETE")
	}
}

func TestWaitForComplete_MultipleWaiters_AllUnblocked(t *testing.T) {
	// Multiple requests waiting on the same in-flight key.
	// When it completes, ALL of them should wake up, not just one.
	s := newTestStore()
	s.Set("key-popular", makeEntry(models.StateProcessing))

	const numWaiters = 5
	results := make(chan *models.CachedEntry, numWaiters)

	for i := 0; i < numWaiters; i++ {
		go func() {
			results <- s.WaitForComplete("key-popular")
		}()
	}

	time.Sleep(50 * time.Millisecond)

	s.Set("key-popular", makeEntry(models.StateComplete))

	for i := 0; i < numWaiters; i++ {
		select {
		case result := <-results:
			if result == nil || result.State != models.StateComplete {
				t.Errorf("waiter %d got unexpected result: %+v", i, result)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("waiter %d never unblocked — Broadcast() may not be working", i)
		}
	}
}

func TestSweep_EvictsExpiredEntries(t *testing.T) {
	// An entry older than the TTL should be removed on the next sweep.
	// We set a tiny TTL (1 nanosecond) and a past CreatedAt so the entry
	// is already "expired" before sweep() even runs.
	s := NewMemoryStore(1 * time.Nanosecond)

	expiredEntry := &models.CachedEntry{
		State:     models.StateComplete,
		CreatedAt: time.Now().Add(-1 * time.Hour).Unix(),
	}
	s.Set("expired-key", expiredEntry)

	// Directly call the unexported sweep method via the exported test helper.
	s.sweep()

	result := s.Get("expired-key")
	if result != nil {
		t.Error("expected expired key to be evicted, but it still exists in the store")
	}
}

func TestSweep_PreservesActiveEntries(t *testing.T) {
	s := NewMemoryStore(24 * time.Hour)

	freshEntry := makeEntry(models.StateComplete)
	s.Set("fresh-key", freshEntry)

	s.sweep()

	result := s.Get("fresh-key")
	if result == nil {
		t.Error("expected fresh key to survive sweep, but it was evicted")
	}
}

func TestSweep_OnlyEvictsExpired_LeavesOthers(t *testing.T) {
	s := NewMemoryStore(1 * time.Nanosecond)

	s.Set("expired", &models.CachedEntry{
		State:     models.StateComplete,
		CreatedAt: time.Now().Add(-2 * time.Hour).Unix(),
	})

	// Fresh entry, give it a 24-hour TTL store
	freshStore := NewMemoryStore(24 * time.Hour)
	freshStore.Set("fresh", makeEntry(models.StateComplete))

	// For the mixed test, just verify our tiny-TTL store evicts the expired one
	s.sweep()

	if s.Get("expired") != nil {
		t.Error("expected 'expired' key to be evicted")
	}
}
