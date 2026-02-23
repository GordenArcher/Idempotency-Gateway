package main

import (
	"bytes"
	"encoding/json"
	"html/template"
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
				"1. Send a POST request to /process-payment (via API clients like Postman or curl)",
				"2. Include the 'Idempotency-Key' header with a unique value",
				"3. Provide a JSON body with payment details (amount, currency)",
				"4. Repeat the same request with the same key to test idempotency",
				"5. Use a different payload with the same key to test conflict handling (409)",
				"6. OR open the HTML UI at /ui",
				"7. Fill in the amount, currency, and idempotency key in the form",
				"8. Click 'Process Payment' to test the middleware and see the response visually",
				"9. Reuse the same key in the form to see cached responses",
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

	tmpl := template.Must(template.ParseFiles("templates/index.html"))

	// HTML form page
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		tmpl.Execute(w, nil)
	})

	mux.HandleFunc("/process", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.ParseForm()
		amount := r.FormValue("amount")
		currency := r.FormValue("currency")
		key := r.FormValue("idempotency_key")

		reqBody := map[string]interface{}{
			"amount":   amount,
			"currency": currency,
		}
		jsonBytes, _ := json.Marshal(reqBody)

		req, _ := http.NewRequest("POST", "/process-payment", bytes.NewReader(jsonBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)

		rec := &responseRecorder{}

		handler := middleware.Idempotency(memStore, http.HandlerFunc(paymentHandler.ProcessPayment))
		handler.ServeHTTP(rec, req)

		tmpl.Execute(w, map[string]interface{}{
			"Response": string(rec.Body),
			"Amount":   amount,
			"Currency": currency,
			"Key":      key,
		})
	})

	log.Printf("[server] idempotency gateway running on %s", cfg.Port)
	if err := http.ListenAndServe(cfg.Port, mux); err != nil {
		log.Fatalf("[server] failed to start: %v", err)
	}
}

type responseRecorder struct {
	Body []byte
	Code int
}

func (r *responseRecorder) Header() http.Header { return http.Header{} }
func (r *responseRecorder) Write(b []byte) (int, error) {
	r.Body = append(r.Body, b...)
	return len(b), nil
}
func (r *responseRecorder) WriteHeader(statusCode int) { r.Code = statusCode }
