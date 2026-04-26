package ulid

import (
	"regexp"
	"testing"
	"time"
)

var crockford = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

func TestNewProducesValidULID(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := New()
		if !crockford.MatchString(s) {
			t.Fatalf("New() = %q, does not match Crockford-base32 26-char regex", s)
		}
	}
}

func TestNewIsMonotonic(t *testing.T) {
	prev := ""
	for i := 0; i < 1000; i++ {
		s := New()
		if s <= prev {
			t.Fatalf("ULIDs not monotonic at iter %d: prev=%q, current=%q", i, prev, s)
		}
		prev = s
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	before := time.Now().UTC().Truncate(time.Millisecond)
	s := New()
	after := time.Now().UTC().Truncate(time.Millisecond)
	got, err := Timestamp(s)
	if err != nil {
		t.Fatalf("Timestamp(%q) returned error: %v", s, err)
	}
	if got.Before(before) || got.After(after.Add(time.Millisecond)) {
		t.Errorf("Timestamp(%q) = %v, want in [%v, %v]", s, got, before, after)
	}
}

func TestTimestampOnInvalidULID(t *testing.T) {
	if _, err := Timestamp("not-a-ulid"); err == nil {
		t.Errorf("Timestamp on invalid input should return error")
	}
}
