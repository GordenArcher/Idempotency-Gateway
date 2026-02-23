package store

import (
	"log"
	"sync"
	"time"

	"github.com/GordenArcher/Idempotency-Gateway/models"
)

type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]*models.CachedEntry
	cond *sync.Cond // used to wake up requests that are waiting on a PROCESSING key
	ttl  time.Duration
}

func NewMemoryStore(ttl time.Duration) *MemoryStore {
	ms := &MemoryStore{
		data: make(map[string]*models.CachedEntry),
		ttl:  ttl,
	}
	ms.cond = sync.NewCond(&ms.mu)
	return ms
}

func (ms *MemoryStore) Get(key string) *models.CachedEntry {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.data[key]
}

func (ms *MemoryStore) Set(key string, entry *models.CachedEntry) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.data[key] = entry

	// Wake up everyone waiting on this key.
	// They'll re-check the state and see it's now COMPLETE.
	ms.cond.Broadcast()
}

// WaitForComplete blocks the calling goroutine until the entry for the given key
// transitions out of PROCESSING state (i.e., becomes COMPLETE).
// This is how we handle the bonus race condition scenario:
// Request B calls this and sleeps here while Request A is still processing.
// When Request A finishes and calls Set(), the Broadcast() above wakes Request B up.
func (ms *MemoryStore) WaitForComplete(key string) *models.CachedEntry {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	// Loop because spurious wakeups are a thing with condition variables.
	// We keep waiting until the state is actually COMPLETE.
	for {
		entry, exists := ms.data[key]
		if !exists || entry.State == models.StateComplete {
			return entry
		}
		// Park this goroutine until someone calls Broadcast()
		ms.cond.Wait()
	}
}

// StartSweeper launches a background goroutine that runs on a ticker
// and evicts entries that have outlived their TTL.
// This is the "Developer's Choice" feature —
// without this, every key ever used would stay in memory forever.
func (ms *MemoryStore) StartSweeper() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			ms.sweep()
		}
	}()
}

// sweep does the actual eviction work.
// Takes a write lock, iterates the map, and deletes anything older than TTL.
func (ms *MemoryStore) sweep() {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	now := time.Now().Unix()
	evicted := 0

	for key, entry := range ms.data {
		ageInSeconds := now - entry.CreatedAt
		if ageInSeconds > int64(ms.ttl.Seconds()) {
			delete(ms.data, key)
			evicted++
		}
	}

	if evicted > 0 {
		log.Printf("sweeper evicted %d expired idempotency keys", evicted)
	}
}
