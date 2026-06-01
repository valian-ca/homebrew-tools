// Package ulid generates monotonic ULIDs for atelierd events.
//
// Wraps github.com/oklog/ulid/v2 with crypto/rand + MonotonicEntropy
// initialised once per process — guarantees lexicographic ordering of
// ULIDs created within the same millisecond, which the outbox watcher
// relies on for ship ordering.
package ulid

import (
	cryptorand "crypto/rand"
	"sync"
	"time"

	oklogulid "github.com/oklog/ulid/v2"
)

var (
	entropy     *oklogulid.MonotonicEntropy
	entropyOnce sync.Once
	mu          sync.Mutex
)

func initEntropy() {
	entropyOnce.Do(func() {
		entropy = oklogulid.Monotonic(cryptorand.Reader, 0)
	})
}

// New generates a new ULID for the current wall-clock time.
func New() string {
	initEntropy()
	mu.Lock()
	defer mu.Unlock()
	return oklogulid.MustNew(oklogulid.Timestamp(time.Now()), entropy).String()
}

// NewAt generates a ULID stamped with t. It deliberately uses a non-monotonic
// entropy source: the shared MonotonicEntropy errors when a timestamp predates
// the last one it saw, and NewAt is called with historical times (a session's
// real lastActivityAt) that routinely move backward relative to wall-clock.
// Ship-order monotonicity is irrelevant for these events — they are gated
// downstream per-field by ULID watermark, not by arrival order.
func NewAt(t time.Time) string {
	return oklogulid.MustNew(oklogulid.Timestamp(t.UTC()), cryptorand.Reader).String()
}

// Timestamp extracts the millisecond timestamp embedded in the ULID prefix.
// Returns (zero, error) if s is not a valid ULID.
func Timestamp(s string) (time.Time, error) {
	id, err := oklogulid.Parse(s)
	if err != nil {
		return time.Time{}, err
	}
	return oklogulid.Time(id.Time()).UTC(), nil
}
