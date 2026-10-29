package config

import (
	"errors"
	"strings"
	"testing"
	"time"
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
		"unknown adapter":   minimal + "resources: [{firestore: {}}]\n",
		"firebase config":   minimal + "resources: [{firebase: {}}]\n",
		"firebase escapes":  minimal + "resources: [{firebase: {config: ../f.json}}]\n",
		"firebase only":     minimal + "resources: [{firebase: {config: f.json, only: [firestor]}}]\n",
		"firebase test":     minimal + "resources: [{firebase: {config: f.json, test: {only: [hub]}}}]\n",
		"only companions":   minimal + "resources: [{firebase: {config: f.json, only: [eventarc, ui]}}]\n",
		"test only tasks":   minimal + "resources: [{firebase: {config: f.json, test: {only: [tasks]}}}]\n",
		"firebase timeout":  minimal + "resources: [{firebase: {config: f.json, functions_timeout: soon}}]\n",
		"two adapters":      minimal + "resources: [{scripts: {name: s, create: c, erase: e, list: l, owner: o}, firebase: {config: f.json}}]\n",
		"unknown scripts k": minimal + "resources: [{scripts: {name: s, create: c, erase: e, list: l, owner: o, oops: 1}}]\n",
		"data is worktree":  minimal + "resources: [{firebase: {config: f.json, data: .}}]\n",
		"data is worktree/": minimal + "resources: [{firebase: {config: f.json, data: ./}}]\n",
		"data is git":       minimal + "resources: [{firebase: {config: f.json, data: .git}}]\n",
		"data is lanes dir": minimal + "resources: [{firebase: {config: f.json, data: .lanes/emu}}]\n",
		"data is seed":      minimal + "resources: [{firebase: {config: f.json, data: seed, seed: seed}}]\n",
		"data holds config": minimal + "resources: [{firebase: {config: app/f.json, data: app}}]\n",
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

func TestParseFirebaseDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal + "resources: [{firebase: {config: firebase.emulator.json}}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	fb := cfg.Firebase()
	if fb == nil {
		t.Fatal("no firebase resource")
	}
	if fb.Data != ".emu-data" || fb.JVMHeap != 512<<20 || fb.FunctionsTimeout.Std() != 180*time.Second || fb.ExportTimeout.Std() != 60*time.Second {
		t.Errorf("defaults = %+v", fb)
	}
}

func TestParseFirebaseDataDirAccepted(t *testing.T) {
	if _, err := Parse([]byte(minimal + "resources: [{firebase: {config: firebase.emulator.json, data: .emu-data, seed: seed}}]\n")); err != nil {
		t.Fatalf("dedicated data dir rejected: %v", err)
	}
}

func TestParseFirebaseFull(t *testing.T) {
	cfg, err := Parse([]byte(minimal + `
resources:
  - firebase:
      config: app/firebase.emulator.json
      data: .data
      seed: seed
      only: [firestore, auth]
      test: {only: [firestore]}
      jvm_heap: 1GB
      functions_timeout: 3m
      export_timeout: 75s
      command: npx firebase
`))
	if err != nil {
		t.Fatal(err)
	}
	fb := cfg.Firebase()
	if fb.JVMHeap != 1<<30 || fb.FunctionsTimeout.Std() != 3*time.Minute || fb.ExportTimeout.Std() != 75*time.Second || fb.Test.Only[0] != "firestore" {
		t.Errorf("firebase = %+v", fb)
	}
}

func TestParseFirebaseLimits(t *testing.T) {
	cases := map[string]string{
		"resources: [{firebase: {config: a.json}}, {firebase: {config: b.json}}]": "resources[1].firebase: at most one firebase resource per project (first at resources[0])",
		"resources: [{firebase: {config: a.json, export_timeout: 90s}}]":          "resources[0].firebase.export_timeout: 90s exceeds the 75s limit",
	}
	for doc, want := range cases {
		_, err := Parse([]byte(minimal + doc + "\n"))
		if err == nil || err.Error() != want {
			t.Errorf("Parse(%s) = %v, want %q", doc, err, want)
		}
	}
}
