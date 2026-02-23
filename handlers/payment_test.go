package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GordenArcher/Idempotency-Gateway/config"
)

// testConfig returns a config with zero delay so tests don't wait 2 seconds each.
// This is exactly why we put ProcessingDelay in config instead of hardcoding it.
func testConfig() *config.Config {
	return &config.Config{
		Port:            ":8080",
		ProcessingDelay: 0, // no sleep in tests
		KeyTTL:          24 * time.Hour,
		SweepInterval:   10 * time.Minute,
	}
}

func TestProcessPayment_ValidRequest_Returns201(t *testing.T) {
	handler := NewPaymentHandler(testConfig())

	body := `{"amount": 100, "currency": "GHS"}`
	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ProcessPayment(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201 Created, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	if resp["status"] != "success" {
		t.Errorf("expected status 'success', got %v", resp["status"])
	}

	msg, ok := resp["message"].(string)
	if !ok {
		t.Fatal("expected 'message' field in response")
	}
	if !strings.Contains(msg, "100.00") || !strings.Contains(msg, "GHS") {
		t.Errorf("unexpected message format: %s", msg)
	}
}

func TestProcessPayment_ResponseContainsAmountAndCurrency(t *testing.T) {
	// The response should echo back the amount and currency
	// so the client can confirm exactly what was charged.
	handler := NewPaymentHandler(testConfig())

	body := `{"amount": 250.50, "currency": "GHS"}`
	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ProcessPayment(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["amount"] != 250.50 {
		t.Errorf("expected amount 250.50, got %v", resp["amount"])
	}
	if resp["currency"] != "GHS" {
		t.Errorf("expected currency GHS, got %v", resp["currency"])
	}
}

func TestProcessPayment_InvalidJSON_Returns400(t *testing.T) {
	handler := NewPaymentHandler(testConfig())

	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(`not json at all`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ProcessPayment(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestProcessPayment_ZeroAmount_Returns400(t *testing.T) {
	// A payment of 0 makes no sense, should be rejected.
	handler := NewPaymentHandler(testConfig())

	body := `{"amount": 0, "currency": "GHS"}`
	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.ProcessPayment(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for zero amount, got %d", w.Code)
	}
}

func TestProcessPayment_NegativeAmount_Returns400(t *testing.T) {
	// Negative amounts shouldn't be allowed through.
	handler := NewPaymentHandler(testConfig())

	body := `{"amount": -50, "currency": "GHS"}`
	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.ProcessPayment(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for negative amount, got %d", w.Code)
	}
}

func TestProcessPayment_MissingCurrency_Returns400(t *testing.T) {
	// Currency is required, we need to know what denomination to charge in.
	handler := NewPaymentHandler(testConfig())

	body := `{"amount": 100}`
	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.ProcessPayment(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing currency, got %d", w.Code)
	}
}

func TestProcessPayment_EmptyBody_Returns400(t *testing.T) {
	handler := NewPaymentHandler(testConfig())

	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(""))
	w := httptest.NewRecorder()

	handler.ProcessPayment(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty body, got %d", w.Code)
	}
}

func TestProcessPayment_ContentTypeIsJSON(t *testing.T) {
	handler := NewPaymentHandler(testConfig())

	body := `{"amount": 100, "currency": "GHS"}`
	req := httptest.NewRequest(http.MethodPost, "/process-payment", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler.ProcessPayment(w, req)

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("expected Content-Type: application/json, got %s", contentType)
	}
}
