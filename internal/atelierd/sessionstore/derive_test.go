package sessionstore

import (
	"strconv"
	"testing"
	"time"
)

func fakeClock(t time.Time) Clock { return func() time.Time { return t } }

func fakeULID() ULIDFn {
	var n int
	return func(time.Time) string {
		s := "ulid-" + strconv.Itoa(n)
		n++
		return s
	}
}

// capturingULID records the timestamp Derive stamps into the ULID so tests can
// assert it carries the session's activity time, not wall-clock.
func capturingULID(captured *time.Time) ULIDFn {
	return func(t time.Time) string {
		*captured = t
		return "ulid-captured"
	}
}

func TestDerive_AutoTitleEmitsAITitle(t *testing.T) {
	now := fakeClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC))
	entry := Entry{CliSessionID: "cs-1", Title: "Refactor outbox", TitleSource: "auto"}

	envs, err := Derive(nil, entry, now, fakeULID())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("want 1 envelope, got %d", len(envs))
	}
	if envs[0].Type != "transcript:ai-title" {
		t.Errorf("Type = %q, want transcript:ai-title", envs[0].Type)
	}
	if envs[0].ClaudeSessionID != "cs-1" {
		t.Errorf("ClaudeSessionID = %q, want cs-1", envs[0].ClaudeSessionID)
	}
	if got := envs[0].Payload["title"]; got != "Refactor outbox" {
		t.Errorf("payload title = %v, want Refactor outbox", got)
	}
}

func TestDerive_UserTitleEmitsCustomTitle(t *testing.T) {
	entry := Entry{CliSessionID: "cs-2", Title: "My session", TitleSource: "user"}
	envs, err := Derive(nil, entry, fakeClock(time.Now().UTC()), fakeULID())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(envs) != 1 || envs[0].Type != "transcript:custom-title" {
		t.Fatalf("want one transcript:custom-title, got %#v", envs)
	}
}

func TestDerive_NullAndUnknownSourceTreatedAsAuto(t *testing.T) {
	for _, src := range []string{"", "null", "weird"} {
		entry := Entry{CliSessionID: "cs-x", Title: "T", TitleSource: src}
		envs, err := Derive(nil, entry, fakeClock(time.Now().UTC()), fakeULID())
		if err != nil {
			t.Fatalf("Derive(%q): %v", src, err)
		}
		if len(envs) != 1 || envs[0].Type != "transcript:ai-title" {
			t.Errorf("source %q: want transcript:ai-title, got %#v", src, envs)
		}
	}
}

func TestDerive_UnchangedTitleIsDeduped(t *testing.T) {
	state := &State{CliSessionID: "cs-3", LastTitle: "Same", LastTitleSource: "auto"}
	entry := Entry{CliSessionID: "cs-3", Title: "Same", TitleSource: "auto"}
	envs, err := Derive(state, entry, fakeClock(time.Now().UTC()), fakeULID())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(envs) != 0 {
		t.Errorf("unchanged title should emit nothing, got %#v", envs)
	}
}

func TestDerive_ChangedTitleEmits(t *testing.T) {
	state := &State{CliSessionID: "cs-4", LastTitle: "Old", LastTitleSource: "auto"}
	entry := Entry{CliSessionID: "cs-4", Title: "New", TitleSource: "auto"}
	envs, err := Derive(state, entry, fakeClock(time.Now().UTC()), fakeULID())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(envs) != 1 || envs[0].Payload["title"] != "New" {
		t.Fatalf("changed title should emit New, got %#v", envs)
	}
}

func TestDerive_SourceChangeWithSameTextEmits(t *testing.T) {
	state := &State{CliSessionID: "cs-5", LastTitle: "T", LastTitleSource: "auto"}
	entry := Entry{CliSessionID: "cs-5", Title: "T", TitleSource: "user"}
	envs, err := Derive(state, entry, fakeClock(time.Now().UTC()), fakeULID())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(envs) != 1 || envs[0].Type != "transcript:custom-title" {
		t.Fatalf("a rename keeping the text but flipping source should emit custom-title, got %#v", envs)
	}
}

func TestDerive_EmptiedTitleEmitsEmptyPayload(t *testing.T) {
	state := &State{CliSessionID: "cs-6", LastTitle: "Was here", LastTitleSource: "auto"}
	entry := Entry{CliSessionID: "cs-6", Title: "", TitleSource: "auto"}
	envs, err := Derive(state, entry, fakeClock(time.Now().UTC()), fakeULID())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("emptied title should emit one envelope, got %d", len(envs))
	}
	if got := envs[0].Payload["title"]; got != "" {
		t.Errorf("payload title = %v, want empty string", got)
	}
}

func TestDerive_FirstSightEmptyTitleStaysSilent(t *testing.T) {
	entry := Entry{CliSessionID: "cs-7", Title: "", TitleSource: "auto"}
	envs, err := Derive(nil, entry, fakeClock(time.Now().UTC()), fakeULID())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(envs) != 0 {
		t.Errorf("first sight of an untitled session must emit nothing, got %#v", envs)
	}
}

func TestDerive_StampsULIDWithActivityAt(t *testing.T) {
	activity := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entry := Entry{CliSessionID: "cs-8", Title: "Old session", TitleSource: "auto", ActivityAt: activity}
	var stamped time.Time
	// fakeClock far in the future to prove the stamp is the activity time, not now.
	_, err := Derive(nil, entry, fakeClock(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)), capturingULID(&stamped))
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !stamped.Equal(activity) {
		t.Errorf("ULID stamp = %v, want session activity time %v", stamped, activity)
	}
}

func TestDerive_FallsBackToClockWhenActivityZero(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	entry := Entry{CliSessionID: "cs-9", Title: "No timestamp", TitleSource: "auto"} // ActivityAt zero
	var stamped time.Time
	_, err := Derive(nil, entry, fakeClock(now), capturingULID(&stamped))
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !stamped.Equal(now) {
		t.Errorf("ULID stamp = %v, want clock fallback %v", stamped, now)
	}
}
