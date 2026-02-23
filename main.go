package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/GordenArcher/Idempotency-Gateway/config"
	"github.com/GordenArcher/Idempotency-Gateway/handlers"
	"github.com/GordenArcher/Idempotency-Gateway/middleware"
	"github.com/GordenArcher/Idempotency-Gateway/store"
)

func main() {
	cfg := config.Default()

	// This is the in-memory map that tracks every idempotency key we've seen.
	memStore := store.NewMemoryStore(cfg.KeyTTL)

	memStore.StartSweeper()

	paymentHandler := handlers.NewPaymentHandler(cfg)

	mux := http.NewServeMux()

	var startTime = time.Now()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		response := map[string]any{
			"message":        "Idempotency Gateway is running",
			"version":        "v1.0.0",
			"uptime_seconds": int(time.Since(startTime).Seconds()),
			"timestamp":      time.Now().UTC(),

			"next_steps": []string{
				"1. Send a POST request to /process-payment",
				"2. Include the 'Idempotency-Key' header with a unique value",
				"3. Provide a JSON body with payment details (e.g. amount, currency, reference)",
				"4. Repeat the same request with the same key to test idempotency",
				"5. Use a different payload with the same key to test conflict handling (409)",
			},

			"example_headers": map[string]string{
				"Content-Type":    "application/json",
				"Idempotency-Key": "abc123-unique-key",
			},
		}

		json.NewEncoder(w).Encode(response)
	})

	// The idempotency middleware wraps the payment handler.
	// Every request to /process-payment goes through the middleware first,
	// and only reaches the handler if it's a genuine first-time request.
	mux.Handle(
		"POST /process-payment",
		middleware.Idempotency(memStore, http.HandlerFunc(paymentHandler.ProcessPayment)),
	)

	log.Printf("[server] idempotency gateway running on %s", cfg.Port)
	if err := http.ListenAndServe(cfg.Port, mux); err != nil {
		log.Fatalf("[server] failed to start: %v", err)
	}
}
