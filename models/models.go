package models

type PaymentRequest struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type KeyState string

const (
	StateProcessing KeyState = "PROCESSING"
	StateComplete   KeyState = "COMPLETE"
)

type CachedEntry struct {
	State        KeyState
	BodyHash     string
	StatusCode   int
	ResponseBody []byte
	CreatedAt    int64
}
