package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/GordenArcher/Idempotency-Gateway/config"
	"github.com/GordenArcher/Idempotency-Gateway/models"
)

type PaymentHandler struct {
	cfg *config.Config
}

func NewPaymentHandler(cfg *config.Config) *PaymentHandler {
	return &PaymentHandler{cfg: cfg}
}

// ProcessPayment handles POST /process-payment.
// By the time a request reaches this function, the idempotency middleware
// has already done its job, so this handler can assume it's always
// dealing with a genuinely new, first-time request.
// It doesn't need to know anything about keys or caching.
func (h *PaymentHandler) ProcessPayment(w http.ResponseWriter, r *http.Request) {

	var req models.PaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "invalid request body, expected {amount, currency}",
		})
		return
	}

	if req.Amount <= 0 || req.Currency == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "amount must be > 0 and currency must not be empty",
		})
		return
	}

	if req.Currency != "GHS" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "unsupported currency — only GHS is allowed",
		})
		return
	}

	time.Sleep(h.cfg.ProcessingDelay)

	response := map[string]interface{}{
		"status":   "success",
		"message":  fmt.Sprintf("Charged %.2f %s", req.Amount, req.Currency),
		"amount":   req.Amount,
		"currency": req.Currency,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}
