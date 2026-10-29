package firebase

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
)

const (
	StackConfigName = "firebase.lanes.json"
	TestConfigName  = "firebase.lanes.test.json"
	ListenHost      = "0.0.0.0"
	SDKHost         = "127.0.0.1"
	TestPrefix      = "test_"
	MaxSocketPath   = 104
)

var products = []string{"auth", "firestore", "database", "functions", "pubsub", "storage", "eventarc", "tasks", "hosting"}

var uiProducts = []string{"auth", "database", "firestore", "functions", "storage"}

var startedByFunctions = []string{"eventarc", "tasks"}

var sdkVars = map[string]string{
	"firestore": "FIRESTORE_EMULATOR_HOST",
	"auth":      "FIREBASE_AUTH_EMULATOR_HOST",
	"storage":   "FIREBASE_STORAGE_EMULATOR_HOST",
	"database":  "FIREBASE_DATABASE_EMULATOR_HOST",
	"pubsub":    "PUBSUB_EMULATOR_HOST",
	"tasks":     "CLOUD_TASKS_EMULATOR_HOST",
}

type Series bool

const (
	Stack Series = false
	Test  Series = true
)

func (s Series) Port(name string) string {
	if s == Test {
		return TestPrefix + name
	}
	return name
}

func (s Series) ConfigName() string {
	if s == Test {
		return TestConfigName
	}
	return StackConfigName
}

func (s Series) String() string {
	if s == Test {
		return "test series"
	}
	return "stack"
}

type Base struct {
	top       map[string]json.RawMessage
	emulators map[string]json.RawMessage
}

func ParseBase(data []byte) (Base, error) {
	var b Base
	if err := json.Unmarshal(data, &b.top); err != nil {
		return Base{}, err
	}
	if b.top == nil {
		return Base{}, errors.New("not a JSON object")
	}
	if raw, ok := b.top["emulators"]; ok {
		if err := json.Unmarshal(raw, &b.emulators); err != nil || b.emulators == nil {
			return Base{}, errors.New("emulators: not a JSON object")
		}
	}
	return b, nil
}

func LoadBase(root string, fb *config.Firebase) (Base, error) {
	path := filepath.Join(root, fb.Config)
	data, err := os.ReadFile(path)
	if err != nil {
		return Base{}, fmt.Errorf("firebase: %w", err)
	}
	b, err := ParseBase(data)
	if err != nil {
		return Base{}, fmt.Errorf("firebase: %s is not a valid firebase config: %w", fb.Config, err)
	}
	return b, nil
}

func (b Base) uiDisabled() bool {
	var ui struct {
		Enabled *bool `json:"enabled"`
	}
	raw, ok := b.emulators["ui"]
	return ok && json.Unmarshal(raw, &ui) == nil && ui.Enabled != nil && !*ui.Enabled
}

type Plan struct {
	Series   Series
	Products []string
	UI       bool
}

func NewPlan(b Base, fb *config.Firebase, s Series) Plan {
	chosen := fb.Only
	if s == Test && len(fb.Test.Only) > 0 {
		chosen = fb.Test.Only
	}
	var want []string
	if len(chosen) > 0 {
		want = chosen
	} else {
		for name := range b.emulators {
			want = append(want, name)
		}
	}
	if slices.Contains(want, "functions") {
		want = append(want, startedByFunctions...)
	}
	p := Plan{Series: s}
	for _, name := range products {
		if slices.Contains(want, name) {
			p.Products = append(p.Products, name)
		}
	}
	p.UI = s == Stack && !b.uiDisabled() && slices.ContainsFunc(p.Products, func(n string) bool { return slices.Contains(uiProducts, n) })
	return p
}

func (p Plan) Starts(name string) bool { return slices.Contains(p.UnprefixedPorts(), name) }

func (p Plan) UnprefixedPorts() []string {
	out := slices.Clone(p.Products)
	if slices.Contains(p.Products, "firestore") {
		out = append(out, "firestore_ws")
	}
	out = append(out, "hub", "logging")
	if p.UI {
		out = append(out, "ui")
	}
	return out
}

func (p Plan) RequiredPorts() []string {
	var out []string
	for _, n := range p.UnprefixedPorts() {
		out = append(out, p.Series.Port(n))
	}
	return out
}

func (p Plan) Only() []string {
	return slices.DeleteFunc(slices.Clone(p.Products), func(n string) bool { return slices.Contains(startedByFunctions, n) })
}

func Missing(required, named []string) []string {
	var out []string
	for _, r := range required {
		if !slices.Contains(named, r) {
			out = append(out, r)
		}
	}
	return out
}

func MissingError(missing []string) error {
	return fmt.Errorf("ports.names is missing %s (required by the firebase resource)", strings.Join(missing, ", "))
}

func Ports(names []string, blockStart int) map[string]int {
	m := make(map[string]int, len(names))
	for i, n := range names {
		m[n] = blockStart + i
	}
	return m
}

// Render drops the top-level keys of the products that do not start:
// firebase-tools starts an emulator for each of them, under --config alone.
func Render(b Base, p Plan, ports map[string]int) ([]byte, error) {
	if missing := Missing(p.RequiredPorts(), keys(ports)); len(missing) > 0 {
		return nil, MissingError(missing)
	}
	port := func(n string) int { return ports[p.Series.Port(n)] }
	top := make(map[string]any, len(b.top))
	for k, v := range b.top {
		if slices.Contains(products, k) && !slices.Contains(p.Products, k) {
			continue
		}
		top[k] = v
	}
	emu := map[string]any{}
	for k, v := range b.emulators {
		if !slices.Contains(products, k) && !slices.Contains([]string{"hub", "ui", "logging"}, k) {
			emu[k] = v
		}
	}
	for _, n := range p.Products {
		block := object(b.emulators[n])
		block["host"] = ListenHost
		block["port"] = port(n)
		if n == "firestore" {
			block["websocketPort"] = port("firestore_ws")
		}
		emu[n] = block
	}
	emu["hub"] = map[string]any{"host": ListenHost, "port": port("hub")}
	// firebase-tools 15 starts the logging emulator in every session, UI or
	// not: its START_LOGGING_EMULATOR default is the string "false".
	emu["logging"] = map[string]any{"host": ListenHost, "port": port("logging")}
	ui := object(b.emulators["ui"])
	if p.UI {
		ui["host"] = ListenHost
		ui["port"] = port("ui")
	} else {
		ui = map[string]any{"enabled": false}
	}
	emu["ui"] = ui
	top["emulators"] = emu
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func object(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	if raw != nil {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func keys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Vars are the series' emulator variables for the SDKs, on 127.0.0.1: the
// emulators listen on every interface, and the hub locator always says
// 127.0.0.1.
func Vars(p Plan, ports map[string]int, configPath string) env.Vars {
	addr := func(n string) string { return SDKHost + ":" + strconv.Itoa(ports[p.Series.Port(n)]) }
	var v env.Vars
	for _, n := range p.Products {
		if name, ok := sdkVars[n]; ok {
			v = append(v, env.Var{Name: name, Value: addr(n)})
		}
	}
	v = append(v, env.Var{Name: "FIREBASE_EMULATOR_HUB", Value: addr("hub")})
	return append(v, env.Var{Name: "LANES_FIREBASE_CONFIG", Value: configPath})
}

// PortVars points each firebase PORT_<NAME> of the stack at the series' port,
// so a command under `lanes test-env` reads the test series where it reads
// the stack's.
func PortVars(p Plan, ports map[string]int) env.Vars {
	var v env.Vars
	for _, n := range p.UnprefixedPorts() {
		if port, ok := ports[p.Series.Port(n)]; ok {
			v = append(v, env.Var{Name: env.PortVar(n), Value: strconv.Itoa(port)})
		}
	}
	return v
}

func ConfigPath(root string, fb *config.Firebase, s Series) string {
	return filepath.Join(root, filepath.Dir(fb.Config), s.ConfigName())
}

// SocketPath is the longest socket path the emulators open under tmpdir: a
// functions worker's, fire_emu_ and 16 hex digits.
func SocketPath(tmpdir string) string {
	return filepath.Join(tmpdir, "fire_emu_"+strings.Repeat("0", 16)+".sock")
}

func CheckSocket(tmpdir string) error {
	p := SocketPath(tmpdir)
	if len(p) > MaxSocketPath {
		return fmt.Errorf("firebase: socket path %s is %d bytes, over the %d-byte macOS limit — use a shorter TMPDIR", p, len(p), MaxSocketPath)
	}
	return nil
}

func Binary(root string, fb *config.Firebase) ([]string, error) {
	if fb.Command != "" {
		return []string{"/bin/sh", "-c", fb.Command + ` "$@"`, "firebase"}, nil
	}
	var searched []string
	dir := filepath.Join(root, filepath.Dir(fb.Config))
	for {
		bin := filepath.Join(dir, "node_modules", ".bin", "firebase")
		searched = append(searched, filepath.Dir(bin))
		if info, err := os.Stat(bin); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return []string{bin}, nil
		}
		if dir == root || !strings.HasPrefix(dir, root+"/") {
			break
		}
		dir = filepath.Dir(dir)
	}
	return nil, fmt.Errorf("firebase-tools not found in node_modules (searched %s): install the project's dependencies", strings.Join(searched, ", "))
}
