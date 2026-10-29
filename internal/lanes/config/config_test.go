package config

import (
	"errors"
	"strings"
	"testing"
)

const minimal = `
schema: 1
ports:
  block: 10
  names: [web, admin]
`

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Sessions, ","); got != "claude" {
		t.Errorf("sessions = %q, want claude", got)
	}
	if cfg.RAMAlert != DefaultRAMAlert {
		t.Errorf("ram_alert = %s, want %s", cfg.RAMAlert, DefaultRAMAlert)
	}
}

func TestParseFull(t *testing.T) {
	cfg, err := Parse([]byte(`
schema: 1
ports: {block: 20, names: [web]}
resources:
  - scripts: {name: store, create: c, erase: e, list: l, owner: o}
generate:
  - template: config/app.env.tmpl
setup:
  - {name: deps, run: ./install.sh, when_changed: [deps.lock]}
sessions: [claude, opencode]
shared: [GradleDaemon]
spare: [htop]
ram_alert: 512MB
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RAMAlert != 512<<20 {
		t.Errorf("ram_alert = %d", cfg.RAMAlert)
	}
	if cfg.Generate[0].Output() != "config/app.env" {
		t.Errorf("output = %q", cfg.Generate[0].Output())
	}
	if cfg.Resources[0].Scripts.Owner != "o" {
		t.Errorf("scripts = %+v", cfg.Resources[0].Scripts)
	}
}

func TestParseRefusesNewerSchemaBeforeUnknownKeys(t *testing.T) {
	_, err := Parse([]byte("schema: 2\nbrand_new_key: true\n"))
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a SchemaError", err)
	}
	if !strings.Contains(err.Error(), "brew upgrade valian-ca/tools/lanes") {
		t.Errorf("message %q does not say to upgrade the formula", err)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key":       minimal + "portz: 1\n",
		"no schema":         "ports: {block: 1}\n",
		"block too small":   "schema: 1\nports: {block: 1, names: [a, b]}\n",
		"bad port name":     "schema: 1\nports: {block: 2, names: [Web]}\n",
		"duplicate port":    "schema: 1\nports: {block: 2, names: [a, a]}\n",
		"empty resource":    minimal + "resources: [{}]\n",
		"missing command":   minimal + "resources: [{scripts: {name: s, create: c, erase: e, list: l}}]\n",
		"template suffix":   minimal + "generate: [{template: app.env}]\n",
		"template escapes":  minimal + "generate: [{template: ../app.env.tmpl}]\n",
		"step without run":  minimal + "setup: [{name: a}]\n",
		"duplicate step":    minimal + "setup: [{name: a, run: x}, {name: a, run: y}]\n",
		"trigger escapes":   minimal + "setup: [{name: a, run: x, when_changed: [/etc/passwd]}]\n",
		"bad size":          minimal + "ram_alert: lots\n",
		"unknown adapter":   minimal + "resources: [{firebase: {}}]\n",
		"unknown scripts k": minimal + "resources: [{scripts: {name: s, create: c, erase: e, list: l, owner: o, oops: 1}}]\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatal("Parse accepted an invalid lanes.yaml")
			}
		})
	}
}

func TestByteSize(t *testing.T) {
	for in, want := range map[string]ByteSize{"4GB": 4 << 30, "512mb": 512 << 20, "1MB": 1 << 20, "100": 100, "8K": 8 << 10} {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if DefaultRAMAlert.String() != "4GB" {
		t.Errorf("String() = %q", DefaultRAMAlert.String())
	}
}
