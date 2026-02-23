package config

import "time"

// Config holds all the tuneable values for the gateway.
// Centralizing these here means I'm not hunting through the codebase
// when I need to change a timeout or port number.
type Config struct {
	// Port is the address the HTTP server listens on.
	Port string

	// ProcessingDelay simulates how long a real payment processor takes.
	// The spec asks for a 2-second delay — this is where that lives.
	ProcessingDelay time.Duration

	// KeyTTL is how long an idempotency key lives in the store before expiry.
	// Keeping it configurable so I can set it low during testing.
	KeyTTL time.Duration

	// SweepInterval is how often the background goroutine runs to evict
	// expired keys from memory. No point sweeping every millisecond,
	// but we don't want stale keys hanging around too long either.
	SweepInterval time.Duration
}

// Default returns a Config with sane defaults that satisfy the spec out of the box.
// main.go will call this
func Default() *Config {
	return &Config{
		Port:            ":8080",
		ProcessingDelay: 2 * time.Second,
		KeyTTL:          24 * time.Hour,
		SweepInterval:   10 * time.Minute,
	}
}
