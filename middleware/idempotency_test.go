package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GordenArcher/Idempotency-Gateway/config"
	"github.com/GordenArcher/Idempotency-Gateway/handlers"
	"github.com/GordenArcher/Idempotency-Gateway/store"
)

// testServer wires the full stack: store → middleware → handler.
// ProcessingDelay is set to 0 so tests don't sit around waiting 2 seconds.
// Passed a custom delay only when I'm testing the race condition scenario.
func testServer(processingDelay time.Duration) (*store.MemoryStore, http.Handler) {
	cfg := &config.Config{
		ProcessingDelay: processingDelay,
		KeyTTL:          24 * time.Hour,
	}
	memStore := store.NewMemoryStore(cfg.KeyTTL)
	handler := handlers.NewPaymentHandler(cfg)
	wrapped := Idempotency(memStore, http.HandlerFunc(handler.ProcessPayment))
	return memStore, wrapped
}

// makeRequest fires a POST /process-payment with the given key and body.
func makeRequest(handler http.Handler, idempotencyKey, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestFirstRequest_Returns201(t *testing.T) {
	_, h := testServer(0)

	w := makeRequest(h, "key-first-001", `{"amount": 100, "currency": "GHS"}`)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d — body: %s", w.Code, w.Body.String())
	}
}

func TestFirstRequest_ResponseContainsChargeMessage(t *testing.T) {
	// The response body must include the charge message per the spec.
	_, h := testServer(0)

	w := makeRequest(h, "key-first-002", `{"amount": 100, "currency": "GHS"}`)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	msg, ok := resp["message"].(string)
	if !ok || !strings.Contains(msg, "GHS") {
		t.Errorf("expected charge message containing 'GHS', got: %v", resp["message"])
	}
}

func TestFirstRequest_NoCacheHitHeader(t *testing.T) {
	// First requests are never from cache — X-Cache-Hit should not be set.
	_, h := testServer(0)

	w := makeRequest(h, "key-first-003", `{"amount": 100, "currency": "GHS"}`)

	if w.Header().Get("X-Cache-Hit") == "true" {
		t.Error("first request should not have X-Cache-Hit: true")
	}
}

func TestDuplicateRequest_Returns201WithCacheHit(t *testing.T) {
	// A retry with the same key and body must return the same status code
	// as the first request, plus X-Cache-Hit: true.
	_, h := testServer(0)

	body := `{"amount": 200, "currency": "GHS"}`
	key := "key-dup-001"

	// First request — processes normally
	first := makeRequest(h, key, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first request failed with %d: %s", first.Code, first.Body.String())
	}

	second := makeRequest(h, key, body)

	if second.Code != http.StatusCreated {
		t.Errorf("expected duplicate to return 201, got %d", second.Code)
	}
	if second.Header().Get("X-Cache-Hit") != "true" {
		t.Error("expected X-Cache-Hit: true on duplicate request")
	}
}

func TestDuplicateRequest_ReturnsSameBodyAsFirstRequest(t *testing.T) {
	// The exact same response body must be replayed, not re-generated.
	// This matters because amounts, transaction IDs, etc. must be identical.
	_, h := testServer(0)

	body := `{"amount": 300, "currency": "GHS"}`
	key := "key-dup-002"

	first := makeRequest(h, key, body)
	second := makeRequest(h, key, body)

	if first.Body.String() != second.Body.String() {
		t.Errorf("expected identical response bodies:\nfirst:  %s\nsecond: %s",
			first.Body.String(), second.Body.String())
	}
}

func TestDuplicateRequest_IsInstant(t *testing.T) {
	// Cache hits should return immediately — the 2-second processing delay
	// must NOT run on duplicate requests. We verify this with timing.
	_, h := testServer(200 * time.Millisecond)

	body := `{"amount": 100, "currency": "GHS"}`
	key := "key-dup-003"

	// First request takes ~200ms
	makeRequest(h, key, body)

	// Second request should be instant (well under 200ms)
	start := time.Now()
	second := makeRequest(h, key, body)
	elapsed := time.Since(start)

	if second.Header().Get("X-Cache-Hit") != "true" {
		t.Error("expected cache hit on second request")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("duplicate request took %v — expected near-instant response (cache hit should skip the delay)", elapsed)
	}
}

func TestMultipleRetries_AllReturnCacheHit(t *testing.T) {
	// Three or more retries should all be cache hits, not just the second.
	_, h := testServer(0)

	body := `{"amount": 50, "currency": "GHS"}`
	key := "key-dup-004"

	makeRequest(h, key, body)

	for i := 0; i < 5; i++ {
		w := makeRequest(h, key, body)
		if w.Header().Get("X-Cache-Hit") != "true" {
			t.Errorf("retry #%d did not get X-Cache-Hit: true", i+1)
		}
	}
}

func TestConflict_SameKeyDifferentBody_Returns409(t *testing.T) {
	// Reusing an idempotency key with a different payload must be rejected.
	// This protects against accidental and malicious amount tampering.
	_, h := testServer(0)

	key := "key-conflict-001"

	makeRequest(h, key, `{"amount": 100, "currency": "GHS"}`)
	conflict := makeRequest(h, key, `{"amount": 500, "currency": "GHS"}`)

	if conflict.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict, got %d — body: %s", conflict.Code, conflict.Body.String())
	}
}

func TestConflict_ErrorMessageIsCorrect(t *testing.T) {
	// The spec defines the exact error message, we need to match it.
	_, h := testServer(0)

	key := "key-conflict-002"
	makeRequest(h, key, `{"amount": 100, "currency": "GHS"}`)
	w := makeRequest(h, key, `{"amount": 999, "currency": "GHS"}`)

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("conflict response is not valid JSON: %v", err)
	}

	expected := "Idempotency key already used for a different request body."
	if resp["error"] != expected {
		t.Errorf("expected error message %q, got %q", expected, resp["error"])
	}
}

func TestConflict_DifferentCurrencySameAmount_Returns409(t *testing.T) {
	_, h := testServer(0)

	key := "key-conflict-003"
	makeRequest(h, key, `{"amount": 100, "currency": "GHS"}`)
	w := makeRequest(h, key, `{"amount": 100, "currency": "USD"}`)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 for different currency, got %d", w.Code)
	}
}

// --- Missing header ---

func TestMissingIdempotencyKey_Returns400(t *testing.T) {
	// Requests without the header cannot be deduplicated, we reject them.
	_, h := testServer(0)

	w := makeRequest(h, "", `{"amount": 100, "currency": "GHS"}`)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing Idempotency-Key, got %d", w.Code)
	}
}

func TestMissingIdempotencyKey_ErrorMessageMentionsHeader(t *testing.T) {
	_, h := testServer(0)

	w := makeRequest(h, "", `{"amount": 100, "currency": "GHS"}`)

	if !strings.Contains(w.Body.String(), "Idempotency-Key") {
		t.Errorf("error message should mention 'Idempotency-Key', got: %s", w.Body.String())
	}
}

func TestRaceCondition_ConcurrentSameKey_ProcessedOnce(t *testing.T) {
	// Two requests arrive at the exact same time with the same key.
	// Only ONE should trigger the actual processing, the second must wait
	// and get the cached result. Neither should get a 409 Conflict.
	//
	// We verify "processed once" by counting handler invocations with a counter.
	var processingCount int
	var mu sync.Mutex

	// Build a custom handler that counts how many times it's actually called.
	countingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		processingCount++
		mu.Unlock()

		// Simulate the 2-second processing window where the second request arrives
		time.Sleep(150 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"status":"success","message":"Charged 100.00 GHS"}`))
	})

	memStore := store.NewMemoryStore(24 * time.Hour)
	wrapped := Idempotency(memStore, countingHandler)

	body := `{"amount": 100, "currency": "GHS"}`
	key := "race-key-001"

	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = makeRequest(wrapped, key, body)
		}(i)
	}

	wg.Wait()

	// Both should succeed
	for i, w := range results {
		if w.Code != http.StatusCreated {
			t.Errorf("request %d got %d, expected 201", i, w.Code)
		}
	}

	// But the handler should have only been called ONCE
	if processingCount != 1 {
		t.Errorf("expected handler to be called exactly 1 time, was called %d times — double processing occurred!", processingCount)
	}

	// One of the two responses should be a cache hit
	cacheHits := 0
	for _, w := range results {
		if w.Header().Get("X-Cache-Hit") == "true" {
			cacheHits++
		}
	}
	if cacheHits != 1 {
		t.Errorf("expected exactly 1 cache hit among 2 concurrent requests, got %d", cacheHits)
	}
}

func TestRaceCondition_SecondRequestGetsFirstResult(t *testing.T) {
	// The response returned to the waiting request must be
	// identical to what the first request got not a new response.
	_, h := testServer(100 * time.Millisecond)

	body := `{"amount": 100, "currency": "GHS"}`
	key := "race-key-002"

	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = makeRequest(h, key, body)
		}(i)
	}
	wg.Wait()

	if results[0].Body.String() != results[1].Body.String() {
		t.Errorf("concurrent requests returned different bodies:\nreq0: %s\nreq1: %s",
			results[0].Body.String(), results[1].Body.String())
	}
}

func TestDifferentKeys_TreatedIndependently(t *testing.T) {
	// Two different idempotency keys should be completely independent —
	// using one shouldn't affect the other in any way.
	_, h := testServer(0)

	w1 := makeRequest(h, "key-A", `{"amount": 100, "currency": "GHS"}`)
	w2 := makeRequest(h, "key-B", `{"amount": 200, "currency": "GHS"}`)

	if w1.Code != http.StatusCreated {
		t.Errorf("key-A: expected 201, got %d", w1.Code)
	}
	if w2.Code != http.StatusCreated {
		t.Errorf("key-B: expected 201, got %d", w2.Code)
	}

	// Neither should be a cache hit both are first requests
	if w1.Header().Get("X-Cache-Hit") == "true" {
		t.Error("key-A was incorrectly treated as a cache hit")
	}
	if w2.Header().Get("X-Cache-Hit") == "true" {
		t.Error("key-B was incorrectly treated as a cache hit")
	}
}

func TestResponseRecorder_CapturesStatusCode(t *testing.T) {
	// The responseRecorder must capture whatever status code the handler writes.
	// If it doesn't, we'd cache the wrong status and replay it incorrectly.
	fakeWriter := httptest.NewRecorder()
	recorder := &responseRecorder{
		ResponseWriter: fakeWriter,
		statusCode:     http.StatusOK,
	}

	recorder.WriteHeader(http.StatusCreated)

	if recorder.statusCode != http.StatusCreated {
		t.Errorf("expected recorder to capture 201, got %d", recorder.statusCode)
	}
	if fakeWriter.Code != http.StatusCreated {
		t.Errorf("expected underlying writer to also get 201, got %d", fakeWriter.Code)
	}
}

func TestResponseRecorder_CapturesBody(t *testing.T) {
	// The recorder must copy body bytes to its internal buffer
	// AND pass them through to the real ResponseWriter.
	fakeWriter := httptest.NewRecorder()
	recorder := &responseRecorder{
		ResponseWriter: fakeWriter,
		statusCode:     http.StatusOK,
	}

	payload := []byte(`{"status":"success"}`)
	recorder.Write(payload)

	if recorder.body.String() != string(payload) {
		t.Errorf("recorder buffer: expected %s, got %s", payload, recorder.body.String())
	}
	if fakeWriter.Body.String() != string(payload) {
		t.Errorf("underlying writer: expected %s, got %s", payload, fakeWriter.Body.String())
	}
}

func TestHashBody_SameInputSameHash(t *testing.T) {
	// The same body must always produce the same hash.
	body := []byte(`{"amount":100,"currency":"GHS"}`)
	h1 := hashBody(body)
	h2 := hashBody(body)

	if h1 != h2 {
		t.Error("same input produced different hashes — hashBody must be deterministic")
	}
}

func TestHashBody_DifferentInputDifferentHash(t *testing.T) {
	// Different bodies must produce different hashes, collision detection.
	h1 := hashBody([]byte(`{"amount":100,"currency":"GHS"}`))
	h2 := hashBody([]byte(`{"amount":500,"currency":"GHS"}`))

	if h1 == h2 {
		t.Error("different inputs produced the same hash, conflict detection would be broken")
	}
}

func TestHashBody_EmptyBody_DoesNotPanic(t *testing.T) {
	// empty body should produce a valid hash, not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("hashBody panicked on empty input: %v", r)
		}
	}()

	h := hashBody([]byte{})
	if h == "" {
		t.Error("expected non-empty hash for empty input")
	}
}
