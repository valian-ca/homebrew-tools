package firebase

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
)

const demoBase = `{
  "firestore": {"rules": "firestore.rules"},
  "storage": {"rules": "storage.rules"},
  "functions": {"source": "functions"},
  "emulators": {
    "auth": {"port": 9099},
    "firestore": {"port": 8080},
    "functions": {"port": 5001},
    "pubsub": {"port": 8085},
    "ui": {"enabled": true},
    "singleProjectMode": true
  }
}`

var allPorts = []string{
	"auth", "firestore", "firestore_ws", "functions", "pubsub", "eventarc", "tasks", "hub", "ui", "logging",
	"test_auth", "test_firestore", "test_firestore_ws", "test_functions", "test_pubsub", "test_eventarc", "test_tasks", "test_hub",
}

func base(t *testing.T, data string) Base {
	t.Helper()
	b, err := ParseBase([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlanStack(t *testing.T) {
	p := NewPlan(base(t, demoBase), &config.Firebase{}, Stack)
	want := []string{"auth", "firestore", "functions", "pubsub", "eventarc", "tasks", "firestore_ws", "hub", "ui", "logging"}
	if got := p.Started(); !slices.Equal(got, want) {
		t.Errorf("Started() = %v, want %v", got, want)
	}
	if got := strings.Join(p.Only(), ","); got != "auth,firestore,functions,pubsub" {
		t.Errorf("Only() = %s", got)
	}
}

func TestPlanTestSeries(t *testing.T) {
	p := NewPlan(base(t, demoBase), &config.Firebase{}, Test)
	if p.UI || slices.Contains(p.Started(), "logging") {
		t.Errorf("the test series starts the UI: %v", p.Started())
	}
	if !slices.Contains(p.RequiredPorts(), "test_hub") || slices.Contains(p.RequiredPorts(), "hub") {
		t.Errorf("RequiredPorts() = %v", p.RequiredPorts())
	}
	only := NewPlan(base(t, demoBase), &config.Firebase{Test: config.FirebaseTest{Only: []string{"firestore"}}}, Test)
	if got := only.RequiredPorts(); !slices.Equal(got, []string{"test_firestore", "test_firestore_ws", "test_hub"}) {
		t.Errorf("test.only firestore requires %v", got)
	}
}

func TestPlanOnlyAndUI(t *testing.T) {
	p := NewPlan(base(t, demoBase), &config.Firebase{Only: []string{"pubsub"}}, Stack)
	if p.UI {
		t.Error("the UI starts for pubsub alone, which it does not show")
	}
	disabled := NewPlan(base(t, `{"emulators": {"firestore": {}, "ui": {"enabled": false}}}`), &config.Firebase{}, Stack)
	if disabled.UI {
		t.Error("the UI starts though ui.enabled is false")
	}
	if !NewPlan(base(t, `{"emulators": {"auth": {}}}`), &config.Firebase{}, Stack).UI {
		t.Error("the UI does not start for auth without a ui block")
	}
}

func TestRender(t *testing.T) {
	b := base(t, demoBase)
	ports := Ports(allPorts, 40000)
	out, err := Render(b, NewPlan(b, &config.Firebase{}, Stack), ports)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["storage"]; ok {
		t.Error("the storage key stays though storage does not start")
	}
	if got["functions"].(map[string]any)["source"] != "functions" {
		t.Errorf("functions = %v", got["functions"])
	}
	emu := got["emulators"].(map[string]any)
	fs := emu["firestore"].(map[string]any)
	if fs["host"] != "0.0.0.0" || fs["port"] != float64(40001) || fs["websocketPort"] != float64(40002) {
		t.Errorf("firestore = %v", fs)
	}
	if emu["hub"].(map[string]any)["port"] != float64(40007) || emu["singleProjectMode"] != true {
		t.Errorf("emulators = %v", emu)
	}
	if ui := emu["ui"].(map[string]any); ui["port"] != float64(40008) || ui["enabled"] != true {
		t.Errorf("ui = %v", ui)
	}

	test, err := Render(b, NewPlan(b, &config.Firebase{Test: config.FirebaseTest{Only: []string{"firestore"}}}, Test), ports)
	if err != nil {
		t.Fatal(err)
	}
	var tgot struct {
		Functions any            `json:"functions"`
		Emulators map[string]any `json:"emulators"`
	}
	if err := json.Unmarshal(test, &tgot); err != nil {
		t.Fatal(err)
	}
	if tgot.Functions != nil || tgot.Emulators["auth"] != nil || tgot.Emulators["logging"] != nil {
		t.Errorf("the test config keeps emulators outside test.only: %s", test)
	}
	if ui := tgot.Emulators["ui"].(map[string]any); ui["enabled"] != false {
		t.Errorf("test ui = %v", ui)
	}
	if fs := tgot.Emulators["firestore"].(map[string]any); fs["port"] != float64(40011) {
		t.Errorf("test firestore = %v", fs)
	}
}

func TestRenderNamesMissingPorts(t *testing.T) {
	b := base(t, demoBase)
	names := slices.DeleteFunc(slices.Clone(allPorts), func(n string) bool { return n == "hub" || n == "logging" })
	_, err := Render(b, NewPlan(b, &config.Firebase{}, Stack), Ports(names, 40000))
	if err == nil || err.Error() != "ports.names is missing hub, logging (required by the firebase resource)" {
		t.Errorf("err = %v", err)
	}
}

func TestParseBaseRefusesInvalid(t *testing.T) {
	for _, doc := range []string{"{", "[]", `{"emulators": []}`, "null"} {
		if _, err := ParseBase([]byte(doc)); err == nil {
			t.Errorf("ParseBase(%q) accepted it", doc)
		}
	}
}

func TestVars(t *testing.T) {
	b := base(t, `{"emulators": {"firestore": {}, "auth": {}, "database": {}, "storage": {}, "pubsub": {}, "functions": {}}}`)
	names := []string{"auth", "firestore", "firestore_ws", "database", "functions", "pubsub", "storage", "eventarc", "tasks", "hub", "ui", "logging"}
	v := Vars(NewPlan(b, &config.Firebase{}, Stack), Ports(names, 41000), "/w/firebase.lanes.json").Map()
	want := map[string]string{
		"FIREBASE_AUTH_EMULATOR_HOST":     "127.0.0.1:41000",
		"FIRESTORE_EMULATOR_HOST":         "127.0.0.1:41001",
		"FIREBASE_DATABASE_EMULATOR_HOST": "127.0.0.1:41003",
		"PUBSUB_EMULATOR_HOST":            "127.0.0.1:41005",
		"FIREBASE_STORAGE_EMULATOR_HOST":  "127.0.0.1:41006",
		"CLOUD_TASKS_EMULATOR_HOST":       "127.0.0.1:41008",
		"FIREBASE_EMULATOR_HUB":           "127.0.0.1:41009",
		"LANES_FIREBASE_CONFIG":           "/w/firebase.lanes.json",
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	if _, ok := v["GCLOUD_PROJECT"]; ok || len(v) != len(want) {
		t.Errorf("vars = %v", v)
	}
	for k := range v {
		if !slices.Contains(env.Fixed, k) {
			t.Errorf("%s is missing from env.Fixed", k)
		}
	}
}

func TestCheckSocket(t *testing.T) {
	if err := CheckSocket("/tmp/lanes-501-abcdef12"); err != nil {
		t.Errorf("a lane TMPDIR fails: %v", err)
	}
	long := "/tmp/" + strings.Repeat("d", 70)
	err := CheckSocket(long)
	if err == nil {
		t.Fatal("a 106-byte socket path passed")
	}
	for _, want := range []string{SocketPath(long), "106 bytes", "104-byte"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not name %q", err, want)
		}
	}
}

func TestBinary(t *testing.T) {
	root := t.TempDir()
	fb := &config.Firebase{Config: "app/firebase.emulator.json"}
	if _, err := Binary(root, fb); err == nil || !strings.Contains(err.Error(), "install the project's dependencies") {
		t.Errorf("err = %v", err)
	}
	bin := filepath.Join(root, "node_modules", ".bin", "firebase")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := Binary(root, fb); err != nil || !slices.Equal(got, []string{bin}) {
		t.Errorf("Binary = %v, %v", got, err)
	}
	fb.Command = "npx firebase"
	if got, _ := Binary(root, fb); !slices.Equal(got, []string{"/bin/sh", "-c", `npx firebase "$@"`, "firebase"}) {
		t.Errorf("Binary with command = %v", got)
	}
}
