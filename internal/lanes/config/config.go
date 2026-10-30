package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	FileName        = "lanes.yaml"
	SupportedSchema = 2
	DefaultRAMAlert = ByteSize(4 << 30)
	MaxBlockSize    = 1000
)

var DefaultSessions = []string{"claude"}

type Config struct {
	Schema    int        `yaml:"schema"`
	Ports     Ports      `yaml:"ports"`
	Resources []Resource `yaml:"resources"`
	Generate  []Generate `yaml:"generate"`
	Setup     []Step     `yaml:"setup"`
	Sessions  []string   `yaml:"sessions"`
	Shared    []string   `yaml:"shared"`
	Spare     []string   `yaml:"spare"`
	RAMAlert  ByteSize   `yaml:"ram_alert"`
	Env       EnvVars    `yaml:"env"`
}

type Ports struct {
	Block int      `yaml:"block"`
	Names []string `yaml:"names"`
}

type Resource struct {
	Scripts  *Scripts  `yaml:"scripts"`
	Firebase *Firebase `yaml:"firebase"`
	MySQL    *MySQL    `yaml:"mysql"`
}

func (r Resource) adapters() []string {
	var out []string
	if r.Scripts != nil {
		out = append(out, "scripts")
	}
	if r.Firebase != nil {
		out = append(out, "firebase")
	}
	if r.MySQL != nil {
		out = append(out, "mysql")
	}
	return out
}

type Scripts struct {
	Name   string `yaml:"name" json:"name"`
	Create string `yaml:"create" json:"create"`
	Erase  string `yaml:"erase" json:"erase"`
	List   string `yaml:"list" json:"list"`
	Owner  string `yaml:"owner" json:"owner"`
}

const (
	DefaultFirebaseData             = ".emu-data"
	DefaultFirebaseJVMHeap          = ByteSize(512 << 20)
	DefaultFirebaseFunctionsTimeout = Duration(180 * time.Second)
	DefaultFirebaseExportTimeout    = Duration(60 * time.Second)
	MaxFirebaseExportTimeout        = Duration(75 * time.Second)
)

// firebaseCompanions start only beside another emulator: eventarc and tasks
// with functions, the UI with what it shows.
var firebaseCompanions = []string{"eventarc", "tasks", "ui"}

var FirebaseEmulators = []string{"auth", "firestore", "database", "functions", "pubsub", "storage", "eventarc", "tasks", "hosting", "ui"}

type Firebase struct {
	Config           string       `yaml:"config"`
	Data             string       `yaml:"data"`
	Seed             string       `yaml:"seed"`
	Only             []string     `yaml:"only"`
	Test             FirebaseTest `yaml:"test"`
	JVMHeap          ByteSize     `yaml:"jvm_heap"`
	FunctionsTimeout Duration     `yaml:"functions_timeout"`
	ExportTimeout    Duration     `yaml:"export_timeout"`
	Command          string       `yaml:"command"`
}

type FirebaseTest struct {
	Only []string `yaml:"only"`
}

func (c *Config) MySQL() *MySQL {
	for _, r := range c.Resources {
		if r.MySQL != nil {
			return r.MySQL
		}
	}
	return nil
}

func (c *Config) Firebase() *Firebase {
	for _, r := range c.Resources {
		if r.Firebase != nil {
			return r.Firebase
		}
	}
	return nil
}

type Generate struct {
	Template string `yaml:"template"`
}

func (g Generate) Output() string { return strings.TrimSuffix(g.Template, ".tmpl") }

type Step struct {
	Name        string   `yaml:"name"`
	Run         string   `yaml:"run"`
	WhenChanged []string `yaml:"when_changed"`
}

type SchemaError struct{ Got int }

func (e *SchemaError) Error() string {
	return fmt.Sprintf("lanes.yaml uses schema %d, this lanes supports up to %d — run: brew upgrade valian-ca/tools/lanes", e.Got, SupportedSchema)
}

var ErrNotFound = errors.New("no lanes.yaml")

func Load(root string) (*Config, error) {
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s", ErrNotFound, root)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func Parse(data []byte) (*Config, error) {
	// The schema gate runs before the strict decode: a newer schema may carry
	// keys this build does not know, and the user must see "upgrade", not
	// "unknown key".
	var head struct {
		Schema int `yaml:"schema"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return nil, err
	}
	if head.Schema > SupportedSchema {
		return nil, &SchemaError{Got: head.Schema}
	}
	if err := checkSchema2Keys(data, head.Schema); err != nil {
		return nil, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if fb := cfg.Firebase(); fb != nil {
		fb.setDefaults()
	}
	if m := cfg.MySQL(); m != nil {
		m.setDefaults()
	}
	if len(cfg.Sessions) == 0 {
		cfg.Sessions = DefaultSessions
	}
	if cfg.RAMAlert == 0 {
		cfg.RAMAlert = DefaultRAMAlert
	}
	return &cfg, nil
}

var (
	portName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	stepName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

func (c *Config) validate() error {
	if c.Schema < 1 {
		return errors.New("schema: required (schema: 1 or schema: 2)")
	}
	if c.Ports.Block < 1 || c.Ports.Block > MaxBlockSize {
		return fmt.Errorf("ports.block: must be between 1 and %d", MaxBlockSize)
	}
	if len(c.Ports.Names) > c.Ports.Block {
		return fmt.Errorf("ports.names: %d names do not fit in a block of %d", len(c.Ports.Names), c.Ports.Block)
	}
	seen := map[string]bool{}
	for _, n := range c.Ports.Names {
		if !portName.MatchString(n) {
			return fmt.Errorf("ports.names: %q must match %s", n, portName)
		}
		if seen[n] {
			return fmt.Errorf("ports.names: %q is declared twice", n)
		}
		seen[n] = true
	}

	seen = map[string]bool{}
	firebaseAt, mysqlAt := -1, -1
	for i, r := range c.Resources {
		switch kinds := r.adapters(); {
		case len(kinds) == 0:
			return fmt.Errorf("resources[%d]: declares no adapter", i)
		case len(kinds) > 1:
			return fmt.Errorf("resources[%d]: declares %d adapters, %s — one per resource", i, len(kinds), strings.Join(kinds, " and "))
		case r.MySQL != nil:
			if mysqlAt >= 0 {
				return fmt.Errorf("resources[%d].mysql: at most one mysql resource per project (first at resources[%d])", i, mysqlAt)
			}
			mysqlAt = i
			if err := r.MySQL.validate(); err != nil {
				return fmt.Errorf("resources[%d].mysql.%w", i, err)
			}
			continue
		case r.Firebase != nil:
			if firebaseAt >= 0 {
				return fmt.Errorf("resources[%d].firebase: at most one firebase resource per project (first at resources[%d])", i, firebaseAt)
			}
			firebaseAt = i
			if err := r.Firebase.validate(); err != nil {
				return fmt.Errorf("resources[%d].firebase.%w", i, err)
			}
			continue
		}
		s := r.Scripts
		if !stepName.MatchString(s.Name) {
			return fmt.Errorf("resources[%d].scripts.name: required, letters, digits, '.', '_' or '-'", i)
		}
		for key, cmd := range map[string]string{"create": s.Create, "erase": s.Erase, "list": s.List, "owner": s.Owner} {
			if strings.TrimSpace(cmd) == "" {
				return fmt.Errorf("resources[%d].scripts.%s: required", i, key)
			}
		}
		if seen[s.Name] {
			return fmt.Errorf("resources[%d].scripts.name: %q is declared twice", i, s.Name)
		}
		seen[s.Name] = true
	}

	for i, g := range c.Generate {
		if !strings.HasSuffix(g.Template, ".tmpl") {
			return fmt.Errorf("generate[%d].template: must end in .tmpl", i)
		}
		if err := relativeInside(g.Template); err != nil {
			return fmt.Errorf("generate[%d].template: %w", i, err)
		}
	}

	seen = map[string]bool{}
	for i, s := range c.Setup {
		if !stepName.MatchString(s.Name) {
			return fmt.Errorf("setup[%d].name: required, letters, digits, '.', '_' or '-'", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("setup[%d].name: %q is declared twice", i, s.Name)
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Run) == "" {
			return fmt.Errorf("setup[%d].run: required", i)
		}
		for _, f := range s.WhenChanged {
			if err := relativeInside(f); err != nil {
				return fmt.Errorf("setup[%d].when_changed: %w", i, err)
			}
		}
	}
	return c.Env.validate()
}

func (f *Firebase) validate() error {
	if f.Config == "" {
		return errors.New("config: required (e.g. config: firebase.emulator.json)")
	}
	for key, p := range map[string]string{"config": f.Config, "data": f.Data, "seed": f.Seed} {
		if p == "" {
			continue
		}
		if err := relativeInside(p); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if err := f.checkDataDir(); err != nil {
		return err
	}
	for key, list := range map[string][]string{"only": f.Only, "test.only": f.Test.Only} {
		for _, e := range list {
			if !slices.Contains(FirebaseEmulators, e) {
				return fmt.Errorf("%s: %q is not an emulator (one of %s)", key, e, strings.Join(FirebaseEmulators, ", "))
			}
		}
		if len(list) > 0 && !slices.ContainsFunc(list, func(e string) bool { return !slices.Contains(firebaseCompanions, e) }) {
			return fmt.Errorf("%s: %s start no emulator on their own — name at least one other emulator", key, strings.Join(list, ", "))
		}
	}
	if f.ExportTimeout > MaxFirebaseExportTimeout {
		return fmt.Errorf("export_timeout: %s exceeds the %s limit", f.ExportTimeout, MaxFirebaseExportTimeout)
	}
	for key, d := range map[string]Duration{"functions_timeout": f.FunctionsTimeout, "export_timeout": f.ExportTimeout} {
		if d < 0 {
			return fmt.Errorf("%s: must be positive", key)
		}
	}
	if f.JVMHeap < 0 {
		return errors.New("jvm_heap: must be positive")
	}
	return nil
}

// checkDataDir rejects a data directory the firebase stop would destroy: the
// stop renames it aside and removes it, so it must be a dedicated directory,
// never the worktree itself, lanes' or git's own state, the seed, or whatever
// holds the emulator config.
func (f *Firebase) checkDataDir() error {
	d := f.Data
	if d == "" {
		d = DefaultFirebaseData
	}
	c := filepath.Clean(d)
	if c == "." {
		return errors.New("data: must name a directory inside the worktree, not the worktree itself")
	}
	if c == ".git" || c == ".lanes" || strings.HasPrefix(c, ".git/") || strings.HasPrefix(c, ".lanes/") {
		return fmt.Errorf("data: %q is lanes' or git's own directory", d)
	}
	if f.Seed != "" {
		s := filepath.Clean(f.Seed)
		if c == s || strings.HasPrefix(c, s+"/") || strings.HasPrefix(s, c+"/") {
			return fmt.Errorf("data: %q overlaps the seed %q", d, f.Seed)
		}
	}
	cfgDir := filepath.Dir(filepath.Clean(f.Config))
	if cfgDir == c || strings.HasPrefix(cfgDir, c+"/") {
		return fmt.Errorf("data: %q holds the base config", d)
	}
	return nil
}

func (f *Firebase) setDefaults() {
	if f.Data == "" {
		f.Data = DefaultFirebaseData
	}
	if f.JVMHeap == 0 {
		f.JVMHeap = DefaultFirebaseJVMHeap
	}
	if f.FunctionsTimeout == 0 {
		f.FunctionsTimeout = DefaultFirebaseFunctionsTimeout
	}
	if f.ExportTimeout == 0 {
		f.ExportTimeout = DefaultFirebaseExportTimeout
	}
}

func relativeInside(p string) error {
	if p == "" || filepath.IsAbs(p) {
		return fmt.Errorf("%q must be a path relative to the worktree root", p)
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q leaves the worktree", p)
	}
	return nil
}

type ByteSize int64

var byteSize = regexp.MustCompile(`^(\d+)\s*([KMG]B?|B)?$`)

func ParseByteSize(s string) (ByteSize, error) {
	m := byteSize.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return 0, fmt.Errorf("%q is not a size (e.g. 4GB, 512MB)", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	switch strings.TrimSuffix(m[2], "B") {
	case "K":
		n <<= 10
	case "M":
		n <<= 20
	case "G":
		n <<= 30
	}
	return ByteSize(n), nil
}

func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	v, err := ParseByteSize(node.Value)
	if err != nil {
		return err
	}
	*b = v
	return nil
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(strings.TrimSpace(node.Value))
	if err != nil {
		return fmt.Errorf("%q is not a duration (e.g. 90s, 3m)", node.Value)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string {
	if d%Duration(time.Second) == 0 {
		return fmt.Sprintf("%ds", int64(d.Std()/time.Second))
	}
	return d.Std().String()
}

func (b ByteSize) String() string {
	switch {
	case b >= 1<<30 && b%(1<<30) == 0:
		return fmt.Sprintf("%dGB", b>>30)
	case b >= 1<<20 && b%(1<<20) == 0:
		return fmt.Sprintf("%dMB", b>>20)
	case b >= 1<<10 && b%(1<<10) == 0:
		return fmt.Sprintf("%dKB", b>>10)
	}
	return fmt.Sprintf("%dB", int64(b))
}
