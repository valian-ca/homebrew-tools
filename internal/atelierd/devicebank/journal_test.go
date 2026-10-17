package devicebank

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

func readJournal(t *testing.T) []Transition {
	t.Helper()
	f, err := os.Open(paths.DeviceTransitions())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Transition
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var tr Transition
		if err := json.Unmarshal(sc.Bytes(), &tr); err != nil {
			t.Fatalf("journal line %q: %v", sc.Text(), err)
		}
		out = append(out, tr)
	}
	return out
}

func TestTransitionsAreJournaled(t *testing.T) {
	useFake(t)
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-1", PlatformIOS)
	pause(t, "sess-1")
	lease(t, "sess-1", PlatformIOS)
	if err := Release(context.Background(), "sess-1", ""); err != nil {
		t.Fatal(err)
	}

	var reasons []string
	for _, tr := range readJournal(t) {
		reasons = append(reasons, tr.Reason)
	}
	want := []string{"lease", "pause", "resume", "release", "recycle-off", "recycle-done"}
	if strings.Join(reasons, ",") != strings.Join(want, ",") {
		t.Fatalf("journal reasons = %v, want %v", reasons, want)
	}
	j := readJournal(t)
	if !j[0].On || !j[0].Leased || j[0].Session != "sess-1" || j[0].To != StateOn {
		t.Fatalf("a lease line is on and leased, got %+v", j[0])
	}
	if !j[3].On || j[3].Leased {
		t.Fatalf("a release line is on without a lease until power-off, got %+v", j[3])
	}
	if j[4].On || j[5].To != StateOff {
		t.Fatalf("the recycle ends off, got %+v then %+v", j[4], j[5])
	}
}

// readmeRecipe extracts the documented jq command so the test runs exactly
// what the README tells the reader to run.
func readmeRecipe(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "jq ") && strings.Contains(line, "device-transitions.jsonl") {
			start, end := strings.Index(line, "'"), strings.LastIndex(line, "'")
			if start >= 0 && end > start {
				return line[start+1 : end]
			}
		}
	}
	t.Fatal("README has no jq recipe over device-transitions.jsonl")
	return ""
}

func TestJqRecipeComputesUnleasedOn(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	fixture := strings.Join([]string{
		`{"at":"2026-10-16T01:00:00.000000000Z","device":"atelier-android-1","platform":"android","from":"off","to":"on","session":"s1","reason":"lease","on":true,"leased":true}`,
		`{"at":"2026-10-16T01:10:00.000000000Z","device":"atelier-android-1","platform":"android","from":"on","to":"recycling","reason":"release","on":true,"leased":false}`,
		`{"at":"2026-10-16T01:10:05.000000000Z","device":"atelier-android-1","platform":"android","from":"recycling","to":"recycling","reason":"recycle-off","on":false,"leased":false}`,
		`{"at":"2026-10-16T02:00:00.000000000Z","device":"atelier-ios-1","platform":"ios","from":"free","to":"on","reason":"migrate","on":true,"leased":false}`,
		`{"at":"2026-10-16T02:05:00.500000000Z","device":"atelier-ios-1","platform":"ios","from":"on","to":"off","reason":"reap-unleased-on","on":false,"leased":false}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "device-transitions.jsonl")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("jq", "-rs", readmeRecipe(t), path).CombinedOutput()
	if err != nil {
		t.Fatalf("jq recipe failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "atelier-ios-1 on without lease for 300s from 2026-10-16T02:00:00.000000000Z" {
		t.Fatalf("recipe output = %q", got)
	}
}
