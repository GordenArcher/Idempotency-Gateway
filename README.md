# Idempotency Gateway
A payment processing API that guarantees every transaction is executed **exactly once** — no matter how many times the client retries.

Built in Go. No external dependencies. Zero infrastructure required.

---

## Table of Contents
1. [Architecture Diagram](#architecture-diagram)
2. [Setup Instructions](#setup-instructions)
3. [API Documentation](#api-documentation)
4. [Design Decisions](#design-decisions)
5. [Developer's Choice Feature](#developers-choice-feature)

---

## Architecture Diagram

### Sequence Diagram — Full Request Lifecycle

```
Client                 Middleware                    Store                  Handler
  │                       │                            │                       │
  │                       │                            │                       │
  │  ── POST /process ──▶ │                            │                       │
  │     Idempotency-Key:  │                            │                       │
  │     "key-abc"         │                            │                       │
  │                       │ ── Get("key-abc") ───────▶ │                       │
  │                       │ ◀── nil (not found) ─────  │                       │
  │                       │                            │                       │
  │                       │ ── Set("key-abc",          │                       │
  │                       │    PROCESSING) ──────────▶ │                       │
  │                       │                            │                       │
  │                       │ ────────────────────────────────── ServeHTTP() ──▶ │
  │                       │                            │      (2s delay)       │
  │                       │ ◀───────────────────────────────── 201 Created ─── │
  │                       │                            │                       │
  │                       │ ── Set("key-abc",          │                       │
  │                       │    COMPLETE + body) ─────▶ │                       │
  │ ◀── 201 Created ───── │                            │                       │
  │                       │                            │                       │
  │                       │                            │                       │
  │  ── POST /process ──▶ │        (retry, same key + same body)               │
  │     Idempotency-Key:  │                            │                       │
  │     "key-abc"         │ ── Get("key-abc") ───────▶ │                       │
  │                       │ ◀── COMPLETE, cached ───── │                       │
  │ ◀── 201 +            │                            │                       │
  │     X-Cache-Hit:true  │       (instant, no handler call, no 2s delay)      │
  │                       │                            │                       │
```

### Flowchart — Middleware Decision Logic

```
                    Incoming POST /process-payment
                                │
                    ┌───────────▼───────────┐
                    │  Idempotency-Key       │
                    │  header present?       │
                    └───────────┬───────────┘
                         No ◀──┴──▶ Yes
                         │              │
                    400 Bad         Check Store
                    Request              │
                              ┌──────────┴──────────┐
                              │                     │
                         Not Found              Key Exists
                              │                     │
                     Mark PROCESSING         ┌──────┴───────┐
                     Call Handler       PROCESSING       COMPLETE
                     Cache result           │                │
                     Mark COMPLETE      Wait on         Body hash
                     Return 201         sync.Cond        match?
                                            │           ┌───┴───┐
                                        Wake up        Yes      No
                                        return          │        │
                                        cached      Replay   409 Conflict
                                        result      cached
                                    X-Cache-Hit:   response
                                        true     X-Cache-Hit:
                                                    true
```

---

## Setup Instructions

### Prerequisites
- Go 1.22 or higher

### Run the server

```bash
# Clone the repo
git clone https://github.com/GordenArcher/Idempotency-Gateway.git
cd Idempotency-Gateway

# Start the server
go run .
```

Server starts on `http://localhost:8080`. That's it — no Docker, no database, no environment variables needed.

### Run with a custom port (optional)
The port is set in `config/config.go`. Change `Port: ":8080"` to whatever you need and re-run.

---

## API Documentation

### `POST /process-payment`

Processes a payment. Guaranteed to execute exactly once per unique `Idempotency-Key`.

#### Request

| Part | Key | Value |
|---|---|---|
| Header | `Idempotency-Key` | Any unique string (UUID recommended) |
| Header | `Content-Type` | `application/json` |
| Body | `amount` | Positive number |
| Body | `currency` | Currency code string (e.g. `"GHS"`) |

#### Example Request

```bash
curl -X POST http://localhost:8080/process-payment \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: a1b2c3d4-e5f6-7890-abcd-ef1234567890" \
  -d '{"amount": 100, "currency": "GHS"}'
```

---

### Response Reference

#### `201 Created` — First successful request

```json
{
  "status": "success",
  "message": "Charged 100.00 GHS",
  "amount": 100,
  "currency": "GHS"
}
```

> Takes ~2 seconds (simulated processing delay).

---

#### `201 Created` — Duplicate request (same key, same body)

Same response body as above, but instant. Look for the extra header:

```
X-Cache-Hit: true
```

> Returns immediately — no processing delay.

---

#### `409 Conflict` — Same key, different body

```json
{
  "error": "Idempotency key already used for a different request body."
}
```

Triggered when a client reuses an `Idempotency-Key` with a different `amount` or `currency`.

---

#### `400 Bad Request` — Missing header

```json
{
  "error": "missing Idempotency-Key header"
}
```

---

#### `400 Bad Request` — Invalid body

```json
{
  "error": "amount must be > 0 and currency must not be empty"
}
```

---

### Testing All Scenarios

```bash
#  Scenario 1: First request (processes payment, ~2s delay) 
curl -X POST http://localhost:8080/process-payment \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: test-key-001" \
  -d '{"amount": 100, "currency": "GHS"}'

#  Scenario 2: Duplicate request (instant, X-Cache-Hit: true) 
curl -X POST http://localhost:8080/process-payment \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: test-key-001" \
  -d '{"amount": 100, "currency": "GHS"}'

#  Scenario 3: Same key, different body (409 Conflict)
curl -X POST http://localhost:8080/process-payment \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: test-key-001" \
  -d '{"amount": 500, "currency": "GHS"}'

# Scenario 4: Missing header (400 Bad Request)
curl -X POST http://localhost:8080/process-payment \
  -H "Content-Type: application/json" \
  -d '{"amount": 100, "currency": "GHS"}'

# Scenario 5: Race condition (send two requests simultaneously)
curl -X POST http://localhost:8080/process-payment \
  -H "Idempotency-Key: race-key-001" \
  -H "Content-Type: application/json" \
  -d '{"amount": 250, "currency": "GHS"}' &

curl -X POST http://localhost:8080/process-payment \
  -H "Idempotency-Key: race-key-001" \
  -H "Content-Type: application/json" \
  -d '{"amount": 250, "currency": "GHS"}' &

wait
# Both return 201. Only one 2s delay. Second has X-Cache-Hit: true.
```

---

## Design Decisions

### Why the store is behind an interface
`middleware` and `handlers` only ever call the `store.Store` interface — they have no idea there's a map underneath. This means swapping to Redis or Postgres is one file (`store/redis.go`) and one line change in `main.go`. Everything else stays the same.

### Why `sync.Cond` for race conditions
The naive solution for the in-flight race condition is a polling loop with `time.Sleep`. That wastes CPU and adds latency. A `sync.Cond` (condition variable) is the idiomatic Go solution — Request B parks itself on `cond.Wait()` and consumes zero CPU until Request A calls `cond.Broadcast()` when it finishes. Exact same result, no polling.

### Why SHA-256 for body hashing
Body comparison is how we detect conflicts (same key, different payload). Comparing raw bytes works but storing full request bodies in memory is wasteful — especially for large payloads. A SHA-256 hash is 32 bytes regardless of input size, is collision-resistant, and is in the standard library with no extra imports.

### Why `responseRecorder` wraps the ResponseWriter
The standard `http.ResponseWriter` is write-only — once you write to it, you can't read back what was written. The middleware needs to cache the handler's response so it can replay it on duplicate requests. `responseRecorder` solves this by tee-ing the writes: bytes go to both the real writer (client gets the response) and an internal buffer (we get the bytes to cache).

### Why 201 Created instead of 200 OK
A payment creates a new transaction record. HTTP semantics say `201 Created` is correct for resource creation. Duplicate requests return the same `201` — because we're replaying the original response, not describing the current state.

---

## Developer's Choice Feature

### Automatic Key Expiry (TTL Sweeper)

**What it is:** A background goroutine that periodically scans the in-memory store and evicts idempotency keys older than a configured TTL (default: 24 hours).

**Why it matters:** Without expiry, every key ever used stays in memory forever. In a real payment system processing thousands of transactions per day, this is a slow memory leak that eventually crashes the server. Stripe's idempotency keys expire after 24 hours — this matches that behaviour.

**How it works:**
- `store.NewMemoryStore(ttl)` takes the TTL as a parameter
- `memStore.StartSweeper()` in `main.go` fires a goroutine that ticks every 10 minutes
- On each tick, `sweep()` iterates the map, compares `entry.CreatedAt` against `time.Now()`, and deletes expired entries
- The sweeper logs how many keys it evicted each run

**Configuration:** TTL and sweep interval are in `config/config.go`. Setting `KeyTTL: 1 * time.Minute` is useful for testing expiry without waiting 24 hours.

---

## Project Structure

```
idempotency-gateway/
├── main.go                  # Entry point — wires everything, starts server
├── go.mod                   # Module definition (zero external dependencies)
├── config/
│   └── config.go            # Port, delays, TTL — all tuneable values live here
├── models/
│   └── models.go            # Shared types: PaymentRequest, CachedEntry, KeyState
├── store/
│   ├── store.go             # Store interface (makes future DB swap clean)
│   └── memory.go            # In-memory implementation with RWMutex + sync.Cond
├── middleware/
│   └── idempotency.go       # Core idempotency logic — intercepts every request
└── handlers/
    └── payment.go           # Payment handler — stays clean, knows nothing about keys
```
