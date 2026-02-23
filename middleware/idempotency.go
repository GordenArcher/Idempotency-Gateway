package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/GordenArcher/Idempotency-Gateway/models"
	"github.com/GordenArcher/Idempotency-Gateway/store"
)

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

// WriteHeader intercepts the status code before it goes out to the client.
func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

// Write intercepts the body bytes, writes to both the buffer (for caching)
// and the real ResponseWriter (so the client still gets a response).
func (rr *responseRecorder) Write(b []byte) (int, error) {
	rr.body.Write(b)
	return rr.ResponseWriter.Write(b)
}

// Idempotency returns an HTTP middleware that wraps any handler with idempotency logic.
// This is the core of the whole project, everything flows through here.
//
// The Flow we follow:
//  1. No Idempotency-Key header > reject immediately
//  2. Key not seen before > process normally, cache the result
//  3. Key seen, still PROCESSING > block until it's done, return cached result
//  4. Key seen, COMPLETE, same body > return cached result instantly
//  5. Key seen, COMPLETE, different body > reject with 409
func Idempotency(s *store.MemoryStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// I extract and validate the Idempotency-Key header
		// Without this header we have no way to deduplicate, reject the request.
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			http.Error(w, `{"error": "missing Idempotency-Key header"}`, http.StatusBadRequest)
			return
		}

		// I need to hash the body to detect conflicts (same key, different payload).
		// Reading the body also drains the reader, so I need to restore it
		// afterwards so the actual handler can read it too.
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error": "failed to read request body"}`, http.StatusInternalServerError)
			return
		}
		// Restore the body so the downstream handler can read it normally
		r.Body = io.NopCloser(bytes.NewBuffer(rawBody))

		// Hash the raw body bytes, this is what we compare on duplicate requests
		bodyHash := hashBody(rawBody)

		//I check the store
		existing := s.Get(idempotencyKey)

		if existing != nil {
			// Key exists ? figure out which scenario we're in

			if existing.State == models.StateProcessing {
				// Race condition handling
				// Another request with this key is currently in-flight.
				// We don't process again, we don't reject, we just wait.
				// WaitForComplete parks this goroutine until the other one finishes.
				completed := s.WaitForComplete(idempotencyKey)
				if completed != nil {
					replayResponse(w, completed)
					return
				}
			}

			// Key is COMPLETE, check if the body matches
			if existing.BodyHash != bodyHash {
				// Conflict detection
				// Same key, different payload, this is either a bug or fraud.
				// The system eeject it hard.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "Idempotency key already used for a different request body.",
				})
				return
			}

			// Duplicate request, same body
			// This is the happy-path duplicate, just replay the cached response.
			replayResponse(w, existing)
			return
		}

		// First time we've seen this key
		// Mark it as PROCESSING immediately so any concurrent duplicate requests
		// know to wait rather than start their own processing.
		s.Set(idempotencyKey, &models.CachedEntry{
			State:     models.StateProcessing,
			BodyHash:  bodyHash,
			CreatedAt: time.Now().Unix(),
		})

		// Wrap the ResponseWriter so we can capture what the handler sends back
		recorder := &responseRecorder{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		// Call the actual payment handler endpoint
		// The 2-second simulated delay happens inside here.
		next.ServeHTTP(recorder, r)

		// Cache the result
		// Now that the handler is done, save what it returned so future
		// duplicate requests can get the exact same response replayed.
		s.Set(idempotencyKey, &models.CachedEntry{
			State:        models.StateComplete,
			BodyHash:     bodyHash,
			StatusCode:   recorder.statusCode,
			ResponseBody: recorder.body.Bytes(),
			CreatedAt:    time.Now().Unix(),
		})
	})
}

// replayResponse sends back the exact same response we cached from the first request.
// Sets X-Cache-Hit: true so the client knows this was a replayed response.
func replayResponse(w http.ResponseWriter, entry *models.CachedEntry) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache-Hit", "true")
	w.WriteHeader(entry.StatusCode)
	w.Write(entry.ResponseBody)
}

func hashBody(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}
