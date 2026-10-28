package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	FileName        = "lanes.yaml"
	SupportedSchema = 1
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
}

type Ports struct {
	Block int      `yaml:"block"`
	Names []string `yaml:"names"`
}

type Resource struct {
	Scripts *Scripts `yaml:"scripts"`
}

type Scripts struct {
	Name   string `yaml:"name" json:"name"`
	Create string `yaml:"create" json:"create"`
	Erase  string `yaml:"erase" json:"erase"`
	List   string `yaml:"list" json:"list"`
	Owner  string `yaml:"owner" json:"owner"`
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

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
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
		return errors.New("schema: required (schema: 1)")
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
	for i, r := range c.Resources {
		if r.Scripts == nil {
			return fmt.Errorf("resources[%d]: declares no adapter", i)
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
	return nil
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
