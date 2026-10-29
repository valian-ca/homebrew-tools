package firebase

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
)

func mkExport(t *testing.T, dir, marker string) {
	t.Helper()
	mkPartial(t, dir, marker)
	if err := os.WriteFile(filepath.Join(dir, metadataFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkPartial(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readMarker(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSettleData(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, data string)
		want  string
	}{
		{
			name: "complete next replaces the current export",
			setup: func(t *testing.T, data string) {
				mkExport(t, data+".next", "next")
				mkExport(t, data, "current")
			},
			want: "next",
		},
		{
			name: "incomplete next is dropped and the current export kept",
			setup: func(t *testing.T, data string) {
				mkPartial(t, data+".next", "next")
				mkExport(t, data, "current")
			},
			want: "current",
		},
		{
			name: "missing data is restored from a complete old",
			setup: func(t *testing.T, data string) {
				mkExport(t, data+".old", "old")
			},
			want: "old",
		},
		{
			name: "missing data stays missing when old is incomplete",
			setup: func(t *testing.T, data string) {
				mkPartial(t, data+".old", "old")
			},
			want: "",
		},
		{
			name:  "nothing at all",
			setup: func(t *testing.T, data string) {},
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "data")
			tc.setup(t, data)
			if err := settleData(data); err != nil {
				t.Fatalf("settleData() error = %v", err)
			}
			for _, suffix := range []string{".next", ".old"} {
				if exists(data + suffix) {
					t.Errorf("%s still exists", data+suffix)
				}
			}
			if tc.want == "" {
				if exists(data) {
					t.Errorf("%s exists, want absent", data)
				}
				return
			}
			if got := readMarker(t, data); got != tc.want {
				t.Errorf("data marker = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSwapWithoutData(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	mkExport(t, data+".next", "next")

	if err := swap(data); err != nil {
		t.Fatalf("swap() error = %v", err)
	}
	if got := readMarker(t, data); got != "next" {
		t.Errorf("data marker = %q, want %q", got, "next")
	}
	if exists(data + ".old") {
		t.Errorf("%s left behind", data+".old")
	}
	if exists(data + ".next") {
		t.Errorf("%s still exists", data+".next")
	}
}

func TestImportFrom(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")

	t.Run("complete data is imported", func(t *testing.T) {
		mkExport(t, data, "current")
		fb := &config.Firebase{Seed: "seed"}
		if got := importFrom(root, fb, data); got != data {
			t.Errorf("importFrom() = %q, want %q", got, data)
		}
		if err := os.RemoveAll(data); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("incomplete data falls back to the seed", func(t *testing.T) {
		mkPartial(t, data, "current")
		fb := &config.Firebase{Seed: "seed"}
		want := filepath.Join(root, "seed")
		if got := importFrom(root, fb, data); got != want {
			t.Errorf("importFrom() = %q, want %q", got, want)
		}
		if err := os.RemoveAll(data); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("incomplete data without seed imports nothing", func(t *testing.T) {
		mkPartial(t, data, "current")
		fb := &config.Firebase{}
		if got := importFrom(root, fb, data); got != "" {
			t.Errorf("importFrom() = %q, want empty", got)
		}
	})
}

func testFirebase() *config.Firebase {
	return &config.Firebase{
		JVMHeap:          512 << 20,
		FunctionsTimeout: config.Duration(180 * time.Second),
	}
}

func TestCaps(t *testing.T) {
	t.Run("defaults from the config", func(t *testing.T) {
		got, err := Caps(testFirebase(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"JAVA_TOOL_OPTIONS=-Xmx512m", "FUNCTIONS_DISCOVERY_TIMEOUT=180"} {
			if !slices.Contains(got, want) {
				t.Errorf("Caps() = %v, missing %q", got, want)
			}
		}
	})

	t.Run("heap override", func(t *testing.T) {
		got, err := Caps(testFirebase(), []string{"LANES_FIREBASE_JVM_HEAP=700MB"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(got, "JAVA_TOOL_OPTIONS=-Xmx700m") {
			t.Errorf("Caps() = %v, missing -Xmx700m", got)
		}
	})

	t.Run("existing JAVA_TOOL_OPTIONS is kept after the heap", func(t *testing.T) {
		got, err := Caps(testFirebase(), []string{"JAVA_TOOL_OPTIONS=-Dfoo=1"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(got, "JAVA_TOOL_OPTIONS=-Xmx512m -Dfoo=1") {
			t.Errorf("Caps() = %v, missing merged JAVA_TOOL_OPTIONS", got)
		}
	})

	timeouts := []struct {
		value string
		want  string
	}{
		{"10", "FUNCTIONS_DISCOVERY_TIMEOUT=10"},
		{"90s", "FUNCTIONS_DISCOVERY_TIMEOUT=90"},
		{"1500ms", "FUNCTIONS_DISCOVERY_TIMEOUT=2"},
	}
	for _, tc := range timeouts {
		t.Run("timeout "+tc.value, func(t *testing.T) {
			got, err := Caps(testFirebase(), []string{"LANES_FIREBASE_FUNCTIONS_TIMEOUT=" + tc.value})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(got, tc.want) {
				t.Errorf("Caps() = %v, missing %q", got, tc.want)
			}
		})
	}

	invalid := []struct {
		name    string
		environ []string
	}{
		{"LANES_FIREBASE_FUNCTIONS_TIMEOUT", []string{"LANES_FIREBASE_FUNCTIONS_TIMEOUT=soon"}},
		{"LANES_FIREBASE_FUNCTIONS_TIMEOUT", []string{"LANES_FIREBASE_FUNCTIONS_TIMEOUT=0"}},
		{"LANES_FIREBASE_FUNCTIONS_TIMEOUT", []string{"LANES_FIREBASE_FUNCTIONS_TIMEOUT=-5s"}},
		{"LANES_FIREBASE_JVM_HEAP", []string{"LANES_FIREBASE_JVM_HEAP=lots"}},
	}
	for _, tc := range invalid {
		t.Run("invalid "+tc.environ[0], func(t *testing.T) {
			_, err := Caps(testFirebase(), tc.environ)
			if err == nil {
				t.Fatal("Caps() error = nil, want error")
			}
			if !strings.HasPrefix(err.Error(), tc.name) {
				t.Errorf("Caps() error = %q, want prefix %q", err, tc.name)
			}
		})
	}
}

func TestXmx(t *testing.T) {
	cases := []struct {
		in   config.ByteSize
		want string
	}{
		{1 << 30, "1024m"},
		{1536 << 10, "1536k"},
		{1000, "1000"},
	}
	for _, tc := range cases {
		if got := xmx(tc.in); got != tc.want {
			t.Errorf("xmx(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
