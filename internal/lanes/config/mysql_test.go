package config

import (
	"slices"
	"strings"
	"testing"
)

const schema2 = `
schema: 2
ports:
  block: 10
  names: [web]
`

func TestParseMySQLDefaults(t *testing.T) {
	cfg, err := Parse([]byte(schema2 + `
resources:
  - mysql:
      databases:
        dev: app_{lane}
        test: app_test_{lane}
`))
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.MySQL()
	if m.Host != "127.0.0.1" || m.Port != 3306 || m.User != "root" || m.Password != "" || m.Start != "" || !slices.Equal(m.Listeners, []string{"mysqld"}) {
		t.Errorf("defaults = %+v", m)
	}
	want := Databases{{Key: "dev", Template: "app_{lane}"}, {Key: "test", Template: "app_test_{lane}"}}
	if !slices.Equal(m.Databases, want) {
		t.Errorf("databases = %+v, want %+v in declaration order", m.Databases, want)
	}
	if got := m.Databases[1].Name("feat_x_abc123"); got != "app_test_feat_x_abc123" {
		t.Errorf("name = %q", got)
	}
}

func TestParseMySQLFull(t *testing.T) {
	cfg, err := Parse([]byte(schema2 + `
resources:
  - mysql:
      host: localhost
      port: 3307
      user: app
      password: secret
      start: docker compose up -d --wait
      listeners: [com.docker.backend]
      databases: {main: 'x_{lane}'}
`))
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.MySQL()
	if m.Host != "localhost" || m.Port != 3307 || m.User != "app" || m.Password != "secret" || m.Start == "" || m.Listeners[0] != "com.docker.backend" {
		t.Errorf("mysql = %+v", m)
	}
}

func TestParseMySQLErrors(t *testing.T) {
	long := strings.Repeat("a", 34) + "_{lane}"
	cases := map[string]string{
		"resources: [{mysql: {databases: {dev: 'a_{lane}'}}}, {mysql: {databases: {dev: 'b_{lane}'}}}]": "resources[1].mysql: at most one mysql resource per project (first at resources[0])",
		"resources: [{mysql: {databases: {dev: app}}}]":                                                 `resources[0].mysql.databases.dev: "app" must contain {lane}`,
		"resources: [{mysql: {databases: {dev: '" + long + "'}}}]":                                      `resources[0].mysql.databases.dev: "` + long + `" can make a 66-character name with a 31-character lane ID, over MySQL's 64-character limit`,
		"resources: [{mysql: {databases: {dev-db: 'a_{lane}'}}}]":                                       "resources[0].mysql.databases.dev-db: the key must be letters, digits or '_'",
		"resources: [{mysql: {databases: {}}}]":                                                         "resources[0].mysql.databases: required, at least one (e.g. dev: app_{lane})",
		"resources: [{mysql: {}}]":                                                                      "resources[0].mysql.databases: required, at least one (e.g. dev: app_{lane})",
		"resources: [{mysql: {port: 70000, databases: {dev: 'a_{lane}'}}}]":                             "resources[0].mysql.port: 70000 is not a TCP port (1-65535)",
		"resources: [{mysql: {listeners: [], databases: {dev: 'a_{lane}'}}}]":                           "resources[0].mysql.listeners: must name at least one process (default [mysqld])",
		"resources: [{mysql: {databases: {dev: 'a_{lane}', test: 'a_{lane}'}}}]":                        "resources[0].mysql.databases.test: same template as databases.dev",
		"resources: [{mysql: {databases: {dev: 'a_{lane}'}}, firebase: {config: f.json}}]":              "resources[0]: declares 2 adapters, firebase and mysql — one per resource",
		"resources: [{mysql: {host: db.prod.internal, databases: {dev: 'a_{lane}'}}}]":                  `resources[0].mysql.host: "db.prod.internal" is not this Mac — use localhost or a loopback address such as 127.0.0.1`,
		"resources: [{mysql: {host: 10.0.0.5, databases: {dev: 'a_{lane}'}}}]":                          `resources[0].mysql.host: "10.0.0.5" is not this Mac — use localhost or a loopback address such as 127.0.0.1`,
		"resources: [{mysql: {databases: {dev: '{lane}_db'}}}]":                                         `resources[0].mysql.databases.dev: "{lane}_db" must start with a fixed prefix before {lane} (e.g. app_{lane}), or reconcile could take other databases on the server for a lane's`,
	}
	for doc, want := range cases {
		_, err := Parse([]byte(schema2 + doc + "\n"))
		if err == nil || err.Error() != want {
			t.Errorf("Parse(%s)\n got %v\nwant %s", doc, err, want)
		}
	}
}

func TestParseMySQLLongestTemplateFits(t *testing.T) {
	fits := strings.Repeat("a", 32) + "_{lane}"
	if _, err := Parse([]byte(schema2 + "resources: [{mysql: {databases: {dev: '" + fits + "'}}}]\n")); err != nil {
		t.Fatalf("a template that makes 64 characters at most was refused: %v", err)
	}
}

func TestParseSchema1RefusesSchema2Keys(t *testing.T) {
	cases := map[string]string{
		minimal + "resources: [{mysql: {databases: {dev: 'a_{lane}'}}}]\n": "resources[0].mysql: requires schema: 2",
		minimal + "env:\n  A: b\n": "env: requires schema: 2",
	}
	for doc, want := range cases {
		_, err := Parse([]byte(doc))
		if err == nil || err.Error() != want {
			t.Errorf("Parse(%q) = %v, want %q", doc, err, want)
		}
	}
}

func TestParseSchema1StillReads(t *testing.T) {
	cfg, err := Parse([]byte(minimal + "resources: [{scripts: {name: s, create: c, erase: e, list: l, owner: o}}]\n"))
	if err != nil || cfg.Schema != 1 || cfg.MySQL() != nil || len(cfg.Env) != 0 {
		t.Fatalf("a schema-1 lanes.yaml: %+v, %v", cfg, err)
	}
}

func TestParseEnvKeepsOrder(t *testing.T) {
	cfg, err := Parse([]byte(schema2 + `
env:
  ZED: "{{.PORT_WEB}}"
  ALPHA: x{{.ZED}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Env) != 2 || cfg.Env[0].Name != "ZED" || cfg.Env[1].Name != "ALPHA" || cfg.Env[1].Template != "x{{.ZED}}" {
		t.Fatalf("env = %+v", cfg.Env)
	}
}

func TestParseEnvErrors(t *testing.T) {
	for _, name := range []string{"TMPDIR", "LANE_ID", "PORT_WEB", "LANES_HOST", "LANES_MYSQL_DATABASE_DEV", "FIRESTORE_EMULATOR_HOST", "FIREBASE_AUTH_EMULATOR_HOST"} {
		_, err := Parse([]byte(schema2 + "env:\n  " + name + ": x\n"))
		if err == nil || !strings.HasPrefix(err.Error(), "env."+name+": redefines a variable lanes sets") {
			t.Errorf("env.%s: %v", name, err)
		}
	}
	for doc, prefix := range map[string]string{
		"env:\n  1BAD: x\n":         "env.1BAD: not a variable name",
		"env:\n  URL: '{{.PORT_'\n": "env.URL: template:",
		"env:\n  URL: [a]\n":        "env.URL: must be a template string",
		"env: [a]\n":                "env: must map each variable name",
	} {
		_, err := Parse([]byte(schema2 + doc))
		if err == nil || !strings.Contains(err.Error(), prefix) {
			t.Errorf("Parse(%q) = %v, want %q", doc, err, prefix)
		}
	}
}
