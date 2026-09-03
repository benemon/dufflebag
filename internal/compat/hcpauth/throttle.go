package hcpauth

import (
	"net/http"
	"sync"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"golang.org/x/time/rate"
)

// The token endpoint is the only unauthenticated surface that spends argon2id
// memory, and it spends it whether or not the client id is real: the miss path
// deliberately verifies a dummy hash so a valid client id cannot be told from
// an invalid one by timing (ADR-0018). A few hundred bytes of request therefore
// buys tens of megabytes of heap from a caller holding no credential, and the
// registry has no degraded mode to fall back to (ADR-0005), so exhausting it
// fails every build (duf-39p).
const (
	// verificationMemoryBudget is the heap the endpoint may spend on concurrent
	// verifications. Sized for a 1 GiB pod: a quarter for authentication leaves
	// the rest for the request path, the pool and the runtime.
	verificationMemoryBudget = 256 << 20

	// callerRate and callerBurst bound one source address.
	//
	// THESE ARE ALSO AN AUDIT-AVAILABILITY CONTROL, not only a memory one.
	// Authentication refusals are audited and audit fails closed (ADR-0020), so
	// the rate an anonymous caller may request at is the rate they may force
	// audit writes at — and therefore how fast they can drive the instance
	// toward refusing everything. Raising these for performance widens that,
	// and the audit destination must be sized to absorb the limit set here.
	// The unpaced Terraform v1.14.7/provider v0.112.0 lane issued 30 tokens
	// from one loopback address on 2026-08-01. A burst of 64 admits two whole
	// measured lanes (60 requests) plus four requests of margin for CI workers
	// sharing an address.
	callerRate  = rate.Limit(2)
	callerBurst = 64

	// callerIdle is how long a generation runs before it is rotated out. An
	// address is forgotten after between one and two of these idle, which is
	// close enough for a bucket that refills in seconds.
	callerIdle = 10 * time.Minute

	// maxTrackedCallers bounds ONE generation, so the table holds at most twice
	// this many addresses.
	maxTrackedCallers = 4096
)

// throttle admits token requests, bounding both concurrent verifications and
// the rate at which any one source may ask for them.
//
// Callers are held in TWO GENERATIONS rather than one swept table. Expiry by
// scanning was worse than the amplification it was added to prevent: a table
// that only shed entries idle past callerIdle did not bound anything, and once
// full it walked every entry under the lock on every new address — 50,000
// addresses cost 22.9 seconds of locked CPU, paid before any verification and
// therefore not backstopped by the semaphore (duf-t0s).
type throttle struct {
	permits chan struct{}

	mu        sync.Mutex
	current   map[string]*rate.Limiter
	previous  map[string]*rate.Limiter
	rotatedAt time.Time
	now       func() time.Time
}

func newThrottle(now func() time.Time) *throttle {
	permits := verificationMemoryBudget / identity.MaxVerificationMemoryBytes
	if permits < 1 {
		// A budget smaller than one verification would admit nothing at all,
		// which is a worse failure than serving one request at a time.
		permits = 1
	}
	return &throttle{
		permits:   make(chan struct{}, permits),
		current:   make(map[string]*rate.Limiter),
		previous:  make(map[string]*rate.Limiter),
		rotatedAt: now(),
		now:       now,
	}
}

// allow reports whether this caller key may make a request now. The handler
// derives it from the peer address and uses X-Forwarded-For only when that peer
// is explicitly trusted. X-Forwarded-For from an untrusted peer remains
// deliberately ignored: a client-supplied header cannot bound that client.
func (t *throttle) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	at := t.now()
	// Full or old: either way the oldest generation goes, and everything in it
	// with one assignment rather than one deletion per entry.
	if len(t.current) >= maxTrackedCallers || at.Sub(t.rotatedAt) >= callerIdle {
		t.previous = t.current
		t.current = make(map[string]*rate.Limiter, maxTrackedCallers)
		t.rotatedAt = at
	}

	limiter, ok := t.current[key]
	if !ok {
		// Promoted rather than replaced, so a caller that is being refused does
		// not get a new bucket for having survived a rotation.
		if limiter, ok = t.previous[key]; ok {
			delete(t.previous, key)
		} else {
			limiter = rate.NewLimiter(callerRate, callerBurst)
		}
		t.current[key] = limiter
	}
	return limiter.Allow()
}

// tracked reports how many addresses are held across both generations.
func (t *throttle) tracked() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.current) + len(t.previous)
}

// acquire reserves the memory one verification may spend, reporting false when
// the budget is already committed.
func (t *throttle) acquire() bool {
	select {
	case t.permits <- struct{}{}:
		return true
	default:
		return false
	}
}

func (t *throttle) release() { <-t.permits }

// writeRetry refuses a request that was not admitted.
//
// Retry-After is set because both refusals are transient by construction.
func writeRetry(w http.ResponseWriter, status int, description string) {
	w.Header().Set("Retry-After", "1")
	writeError(w, status, "temporarily_unavailable", description)
}
