package adopt

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	testutil.Hermetic(t)
	dir := testutil.RealTemp(t)
	testutil.Git(t, dir, "init", "-q", "-b", "main")
	for p, c := range files {
		testutil.Write(t, filepath.Join(dir, p), c)
	}
	testutil.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	if len(files) > 0 {
		testutil.Git(t, dir, "add", "-A")
		testutil.Git(t, dir, "commit", "-q", "-m", "files")
	}
	return dir
}

func plan(t *testing.T, dir string) *Result {
	t.Helper()
	res, err := Plan(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func initRepo(t *testing.T, dir string) (*Result, string) {
	t.Helper()
	res := plan(t, dir)
	if err := res.Apply(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	res.WriteReport(&out, false)
	return res, out.String()
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func status(t *testing.T, dir string) string {
	t.Helper()
	return testutil.Git(t, dir, "status", "--porcelain", "--untracked-files=all")
}

func parse(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(readFile(t, dir, "lanes.yaml")))
	if err != nil {
		t.Fatalf("the written lanes.yaml does not parse: %v\n%s", err, readFile(t, dir, "lanes.yaml"))
	}
	return cfg
}

type hookGroup struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout *int   `json:"timeout"`
	} `json:"hooks"`
}

func hooks(t *testing.T, dir string) map[string][]hookGroup {
	t.Helper()
	var s struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(readFile(t, dir, SettingsFile)), &s); err != nil {
		t.Fatal(err)
	}
	return s.Hooks
}

func lanesTimeout(t *testing.T, groups []hookGroup, name string) (*int, bool) {
	t.Helper()
	for _, g := range groups {
		for _, h := range g.Hooks {
			if strings.Contains(h.Command, "lanes hook "+name) {
				return h.Timeout, true
			}
		}
	}
	return nil, false
}

func unmarshalYAML(text string, v any) error { return yaml.Unmarshal([]byte(text), v) }

func keysOf[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

const composeMySQL = `services:
  db:
    image: mysql:8.4
    ports:
      - "${DB_PORT:-3306}:3306"
    environment:
      MYSQL_ROOT_PASSWORD: secret
`

func TestMySQLFromCompose(t *testing.T) {
	dir := repo(t, map[string]string{"pilote/docker-compose.yaml": composeMySQL})
	_, report := initRepo(t, dir)
	y := readFile(t, dir, "lanes.yaml")
	for _, want := range []string{
		"  - mysql:\n      port: 3306\n      user: root\n      password: secret\n      start: docker compose -f pilote/docker-compose.yaml up -d --wait db\n      listeners: [com.docker.backend]\n      # lanes init: databases",
		"      databases: {}\n",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("lanes.yaml lacks %q:\n%s", want, y)
		}
	}
	if !strings.Contains(report, "lanes.yaml resources[0].mysql.databases") {
		t.Errorf("the report does not name the databases:\n%s", report)
	}
	_, err := config.Parse([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "resources[0].mysql.databases: required") {
		t.Fatalf("an unfilled databases key parses: %v", err)
	}
	filled := strings.Replace(y, "databases: {}", "databases:\n        dev: app_{lane}", 1)
	cfg, err := config.Parse([]byte(filled))
	if err != nil {
		t.Fatalf("filled in, lanes.yaml does not parse: %v\n%s", err, filled)
	}
	m := cfg.MySQL()
	if m.Port != 3306 || m.User != "root" || m.Password != "secret" || !slices.Equal(m.Listeners, []string{"com.docker.backend"}) {
		t.Errorf("mysql resource %+v", m)
	}
	if strings.Contains(report, "a mysql resource, if the project's MySQL server runs without docker-compose") {
		t.Errorf("the report asks for a MySQL server it found:\n%s", report)
	}
}

func TestComposeVariants(t *testing.T) {
	cases := []struct {
		name, compose string
		want, report  []string
	}{
		{"long syntax", "services:\n  db:\n    image: docker.io/library/mysql:8\n    ports:\n      - target: 3306\n        published: 3307\n    environment:\n      MYSQL_ROOT_PASSWORD: pw\n",
			[]string{"port: 3307", "password: pw"}, nil},
		{"host ip", "services:\n  db:\n    image: mysql\n    ports: [\"127.0.0.1:3308:3306/tcp\"]\n    environment: [MYSQL_ROOT_PASSWORD=pw]\n",
			[]string{"port: 3308", "password: pw"}, nil},
		{"variable without default", "services:\n  db:\n    image: mysql:8\n    ports: [\"${DB_PORT}:3306\"]\n    environment:\n      MYSQL_ROOT_PASSWORD: pw\n",
			[]string{"# lanes init: port —"}, []string{"resources[0].mysql.port"}},
		{"mariadb password", "services:\n  db:\n    image: bitnami/mariadb:11\n    ports: [\"3306:3306\"]\n    environment:\n      MARIADB_ROOT_PASSWORD: maria\n",
			[]string{"password: maria"}, nil},
		{"empty password allowed", "services:\n  db:\n    image: mysql:8\n    ports: [\"3306:3306\"]\n    environment:\n      MYSQL_ALLOW_EMPTY_PASSWORD: \"yes\"\n",
			[]string{`password: ""`}, nil},
		{"interpolated password", "services:\n  db:\n    image: mysql:8\n    ports: [\"3306:3306\"]\n    environment:\n      MYSQL_ROOT_PASSWORD: ${ROOT_PW}\n",
			[]string{"# lanes init: password —"}, []string{"resources[0].mysql.password"}},
		{"two services", "services:\n  a:\n    image: mysql:8\n    ports: [\"3310:3306\"]\n    environment: {MYSQL_ROOT_PASSWORD: a}\n  b:\n    image: mariadb\n    ports: [\"3311:3306\"]\n",
			[]string{"port: 3310"}, []string{"another MySQL service"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := repo(t, map[string]string{"compose.yaml": c.compose})
			_, report := initRepo(t, dir)
			y := readFile(t, dir, "lanes.yaml")
			for _, w := range c.want {
				if !strings.Contains(y, w) {
					t.Errorf("lanes.yaml lacks %q:\n%s", w, y)
				}
			}
			for _, w := range c.report {
				if !strings.Contains(report, w) {
					t.Errorf("the report lacks %q:\n%s", w, report)
				}
			}
			for _, line := range strings.Split(y, "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "${") {
					t.Errorf("lanes.yaml copies an interpolated value: %s", line)
				}
			}
		})
	}
}

func TestPostgresIsReportedNotWritten(t *testing.T) {
	dir := repo(t, map[string]string{"backend/functions/docker-compose.yml": "services:\n  db:\n    image: postgres:17-alpine\n    ports:\n      - ${DB_PORT:-5432}:5432\n"})
	_, report := initRepo(t, dir)
	if y := readFile(t, dir, "lanes.yaml"); strings.Contains(strings.ToLower(y), "postgres") || strings.Contains(y, "  - mysql:") {
		t.Errorf("lanes.yaml declares a database resource:\n%s", y)
	}
	if !strings.Contains(report, "Postgres: adapter not available in this lanes") {
		t.Errorf("report:\n%s", report)
	}
}

func TestDevServers(t *testing.T) {
	dir := repo(t, map[string]string{
		"frontend/package.json": `{"name": "front", "scripts": {"dev": "vite", "storybook": "storybook dev -p 6006", "build": "vite build", "preview": "vite preview", "sb-build": "storybook build", "old-sb": "build-storybook", "test": "vitest"}}`,
		"admin/package.json":    `{"scripts": {"dev": "NODE_ENV=development vite --port=5174", "both": "vite --port 5175 & tsc -w", "first": "vite & tsc -w", "serve": "tsc && vite serve"}}`,
		"package.json":          `{"name": "@acme/root", "scripts": {"web": "start-storybook"}}`,
		"pnpm-lock.yaml":        "",
	})
	_, report := initRepo(t, dir)
	pcYAML := readFile(t, dir, "process-compose.yaml")
	cfg := parse(t, dir)
	want := map[string]string{
		"admin-dev":          `NODE_ENV=development pnpm exec -- vite --port "$PORT_ADMIN_DEV"`,
		"admin-both":         `pnpm run both`,
		"admin-first":        `pnpm run first`,
		"admin-serve":        `pnpm run serve --port "$PORT_ADMIN_SERVE"`,
		"frontend-dev":       `pnpm run dev --port "$PORT_FRONTEND_DEV"`,
		"frontend-storybook": `pnpm exec -- storybook dev --port "$PORT_FRONTEND_STORYBOOK"`,
		"root-web":           `pnpm run web --port "$PORT_ROOT_WEB"`,
	}
	var procs struct {
		Processes map[string]struct {
			Command    string `yaml:"command"`
			WorkingDir string `yaml:"working_dir"`
			Disabled   bool   `yaml:"disabled"`
			Readiness  struct {
				Exec struct {
					Command string `yaml:"command"`
				} `yaml:"exec"`
				FailureThreshold int `yaml:"failure_threshold"`
			} `yaml:"readiness_probe"`
		} `yaml:"processes"`
	}
	if err := unmarshalYAML(pcYAML, &procs); err != nil {
		t.Fatal(err)
	}
	if len(procs.Processes) != len(want) {
		t.Errorf("processes %v, want %v", keysOf(procs.Processes), keysOf(want))
	}
	for name, cmd := range want {
		p, ok := procs.Processes[name]
		if !ok {
			t.Errorf("no process %s:\n%s", name, pcYAML)
			continue
		}
		if p.Command != cmd || !p.Disabled {
			t.Errorf("%s: %q disabled=%v, want %q", name, p.Command, p.Disabled, cmd)
		}
		portVar := "$PORT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if p.Readiness.Exec.Command != `nc -z localhost "`+portVar+`"` || p.Readiness.FailureThreshold < 60 {
			t.Errorf("%s: readiness %+v", name, p.Readiness)
		}
		if port := strings.ReplaceAll(name, "-", "_"); !slices.Contains(cfg.Ports.Names, port) {
			t.Errorf("ports.names %v lacks %s", cfg.Ports.Names, port)
		}
	}
	if procs.Processes["frontend-dev"].WorkingDir != "frontend" || procs.Processes["root-web"].WorkingDir != "" {
		t.Errorf("working dirs: %+v", procs.Processes)
	}
	if strings.Contains(procs.Processes["frontend-storybook"].Command, "6006") {
		t.Error("the storybook process keeps its fixed port")
	}
	for _, w := range []string{"process-compose.yaml frontend-storybook: rewritten", "process-compose.yaml admin-dev: rewritten", "processes.admin-both.command", "processes.admin-first.command"} {
		if !strings.Contains(report, w) {
			t.Errorf("the report lacks %q:\n%s", w, report)
		}
	}
	if !strings.Contains(pcYAML, "# lanes init: both sets its own port") {
		t.Errorf("no comment on the composite script:\n%s", pcYAML)
	}
}

func TestDevServerPackageManagers(t *testing.T) {
	dir := repo(t, map[string]string{
		"a/package.json":      `{"scripts": {"dev": "vite"}}`,
		"a/package-lock.json": "{}",
		"b/package.json":      `{"scripts": {"dev": "vite"}}`,
		"b/yarn.lock":         "",
		"c/package.json":      `{"scripts": {"dev": "vite"}}`,
	})
	initRepo(t, dir)
	pcYAML := readFile(t, dir, "process-compose.yaml")
	for _, w := range []string{`npm run dev -- --port "$PORT_A_DEV"`, `yarn run dev --port "$PORT_B_DEV"`, `npm run dev -- --port "$PORT_C_DEV"`} {
		if !strings.Contains(pcYAML, w) {
			t.Errorf("process-compose.yaml lacks %q:\n%s", w, pcYAML)
		}
	}
}

func TestSameBasenamePackages(t *testing.T) {
	dir := repo(t, map[string]string{
		"apps/web/package.json":     `{"scripts": {"dev": "vite"}}`,
		"packages/web/package.json": `{"scripts": {"dev": "vite"}}`,
		"package.json":              `{"scripts": {"dev": "vite"}}`,
	})
	initRepo(t, dir)
	cfg := parse(t, dir)
	for _, w := range []string{"apps_web_dev", "packages_web_dev", "app_dev"} {
		if !slices.Contains(cfg.Ports.Names, w) {
			t.Errorf("ports.names %v lacks %s", cfg.Ports.Names, w)
		}
	}
}

func TestSetupSteps(t *testing.T) {
	dir := repo(t, map[string]string{"pnpm-lock.yaml": "", "mobile/pubspec.lock": ""})
	initRepo(t, dir)
	cfg := parse(t, dir)
	want := []config.Step{
		{Name: "flutter-mobile", Run: "cd mobile && flutter pub get", WhenChanged: []string{"mobile/pubspec.lock"}},
		{Name: "pnpm", Run: "pnpm install --frozen-lockfile", WhenChanged: []string{"pnpm-lock.yaml"}},
	}
	if !reflect.DeepEqual(cfg.Setup, want) {
		t.Errorf("setup %+v, want %+v", cfg.Setup, want)
	}
	h := hooks(t, dir)
	for event, name := range map[string]string{"SessionStart": "session-start", "CwdChanged": "cwd-changed", "PostToolUse": "cwd-changed"} {
		if to, ok := lanesTimeout(t, h[event], name); !ok || to == nil || *to != PrepTimeout {
			t.Errorf("%s: timeout %v", event, to)
		}
	}
	if to, _ := lanesTimeout(t, h["SessionEnd"], "session-end"); to == nil || *to != SessionEndTimeout {
		t.Errorf("SessionEnd timeout %v", to)
	}
}

func TestIgnoredLockfileGivesNoStep(t *testing.T) {
	dir := repo(t, map[string]string{".gitignore": "vendor/\n", "vendor/package-lock.json": "{}"})
	initRepo(t, dir)
	if cfg := parse(t, dir); len(cfg.Setup) != 0 {
		t.Errorf("setup %+v from an ignored lockfile", cfg.Setup)
	}
}

func TestBlockRounding(t *testing.T) {
	dir := repo(t, map[string]string{
		"firebase.json":    `{"emulators": {"auth": {}, "firestore": {}}}`,
		"web/package.json": `{"scripts": {"dev": "vite", "admin": "vite --mode admin"}}`,
	})
	initRepo(t, dir)
	cfg := parse(t, dir)
	if len(cfg.Ports.Names) != 13 || cfg.Ports.Block != 20 {
		t.Fatalf("%d names, block %d: %v", len(cfg.Ports.Names), cfg.Ports.Block, cfg.Ports.Names)
	}
	if !slices.Equal(cfg.Ports.Names[:2], []string{"web_dev", "web_admin"}) || cfg.Ports.Names[2] != "auth" || !strings.HasPrefix(cfg.Ports.Names[12], "test_") {
		t.Errorf("order %v", cfg.Ports.Names)
	}
	for n, want := range map[int]int{0: 10, 1: 10, 10: 10, 11: 20, 19: 20, 20: 20, 21: 30} {
		if got := blockSize(n); got != want {
			t.Errorf("blockSize(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestFirebasePorts(t *testing.T) {
	dir := repo(t, map[string]string{
		"firebase.json":          `{"emulators": {"auth": {}}}`,
		"firebase.emulator.json": `{"emulators": {"firestore": {}, "functions": {}, "ui": {"enabled": false}}}`,
		"sub/firebase.json":      `{"emulators": {"storage": {}}}`,
		"other/firebase.json":    `{"hosting": {}}`,
		"package.json":           `{"devDependencies": {"firebase-tools": "15"}}`,
	})
	_, report := initRepo(t, dir)
	cfg := parse(t, dir)
	fb := cfg.Firebase()
	if fb == nil || fb.Config != "firebase.emulator.json" {
		t.Fatalf("firebase %+v", fb)
	}
	if slices.Contains(cfg.Ports.Names, "ui") {
		t.Errorf("ui named although ui.enabled is false: %v", cfg.Ports.Names)
	}
	data, _ := os.ReadFile(filepath.Join(dir, fb.Config))
	base, _ := firebase.ParseBase(data)
	for _, s := range []firebase.Series{firebase.Stack, firebase.Test} {
		if missing := firebase.Missing(firebase.NewPlan(base, fb, s).RequiredPorts(), cfg.Ports.Names); len(missing) > 0 {
			t.Errorf("%s lacks %v", s, missing)
		}
	}
	for _, w := range []string{"firebase.json: another Firebase config", "sub/firebase.json: another Firebase config"} {
		if !strings.Contains(report, w) {
			t.Errorf("the report lacks %q:\n%s", w, report)
		}
	}
	if strings.Contains(report, "other/firebase.json") || strings.Contains(report, "firebase-tools is in no package.json") {
		t.Errorf("report:\n%s", report)
	}
}

func TestFirebaseWithoutEmulatorsIsSkipped(t *testing.T) {
	dir := repo(t, map[string]string{"firebase.json": `{"hosting": {"public": "dist"}}`})
	_, report := initRepo(t, dir)
	if cfg := parse(t, dir); cfg.Firebase() != nil {
		t.Errorf("firebase resource from a config without emulators")
	}
	if !strings.Contains(report, "Nothing detected") {
		t.Errorf("report:\n%s", report)
	}
}

func TestFirebaseToolsMissing(t *testing.T) {
	dir := repo(t, map[string]string{"firebase.json": `{"emulators": {"auth": {}}}`})
	_, report := initRepo(t, dir)
	if !strings.Contains(report, "firebase-tools is in no package.json") {
		t.Errorf("report:\n%s", report)
	}
}

func TestRefusesExistingFiles(t *testing.T) {
	for _, name := range []string{"lanes.yaml", "process-compose.yaml"} {
		t.Run(name, func(t *testing.T) {
			dir := repo(t, map[string]string{name: "x: 1\n", "package-lock.json": "{}"})
			_, err := Plan(context.Background(), dir)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Plan: %v", err)
			}
			if s := status(t, dir); s != "" {
				t.Errorf("git status after a refusal:\n%s", s)
			}
		})
	}
	t.Run("second run", func(t *testing.T) {
		dir := repo(t, map[string]string{"firebase.json": `{"emulators": {"auth": {}}}`})
		initRepo(t, dir)
		before := status(t, dir)
		if _, err := Plan(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "lanes.yaml already exists") {
			t.Fatalf("second Plan: %v", err)
		}
		if after := status(t, dir); after != before {
			t.Errorf("the second run changed the tree:\n%s\n---\n%s", before, after)
		}
	})
	t.Run("invalid settings", func(t *testing.T) {
		dir := repo(t, map[string]string{SettingsFile: "{ not json", "package-lock.json": "{}"})
		if _, err := Plan(context.Background(), dir); err == nil || !strings.Contains(err.Error(), SettingsFile) {
			t.Fatalf("Plan: %v", err)
		}
		if s := status(t, dir); s != "" {
			t.Errorf("git status after a refusal:\n%s", s)
		}
	})
	t.Run("not a repository", func(t *testing.T) {
		if _, err := Plan(context.Background(), testutil.RealTemp(t)); err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
			t.Fatalf("Plan: %v", err)
		}
	})
}

func TestMergesSettingsAndEnvrc(t *testing.T) {
	settings := `{
  "permissions": {"allow": ["Bash(go test *)"], "deny": []},
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "./check.sh"}]}]
  },
  "model": "opus"
}
`
	dir := repo(t, map[string]string{SettingsFile: settings, EnvrcFile: "use asdf\ndotenv_if_exists .env.local"})
	initRepo(t, dir)
	var before, after map[string]any
	_ = json.Unmarshal([]byte(settings), &before)
	got := readFile(t, dir, SettingsFile)
	if err := json.Unmarshal([]byte(got), &after); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"permissions", "model"} {
		if !reflect.DeepEqual(before[k], after[k]) {
			t.Errorf("%s changed: %v → %v", k, before[k], after[k])
		}
	}
	if !reflect.DeepEqual(before["hooks"].(map[string]any)["PreToolUse"], after["hooks"].(map[string]any)["PreToolUse"]) {
		t.Errorf("PreToolUse changed:\n%s", got)
	}
	if strings.Index(got, `"permissions"`) > strings.Index(got, `"hooks"`) || strings.Index(got, `"hooks"`) > strings.Index(got, `"model"`) {
		t.Errorf("key order lost:\n%s", got)
	}
	h := hooks(t, dir)
	for _, ev := range []string{"SessionStart", "CwdChanged", "PostToolUse", "SessionEnd"} {
		if len(h[ev]) != 1 {
			t.Errorf("%s: %+v", ev, h[ev])
		}
	}
	if h["PostToolUse"][0].Matcher != "EnterWorktree|ExitWorktree" {
		t.Errorf("PostToolUse matcher %q", h["PostToolUse"][0].Matcher)
	}
	if to, _ := lanesTimeout(t, h["SessionStart"], "session-start"); to != nil {
		t.Errorf("SessionStart has a timeout without setup steps: %v", *to)
	}
	envrc := readFile(t, dir, EnvrcFile)
	if envrc != "use asdf\ndotenv_if_exists .env.local\n"+EnvrcLine+"\n" {
		t.Errorf(".envrc:\n%q", envrc)
	}
}

func TestExistingLanesHookNotDuplicated(t *testing.T) {
	settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "lanes hook session-start"}]}]}}`
	dir := repo(t, map[string]string{SettingsFile: settings, EnvrcFile: EnvrcLine + "\n", "package-lock.json": "{}"})
	res, report := initRepo(t, dir)
	h := hooks(t, dir)
	if len(h["SessionStart"]) != 1 || len(h["CwdChanged"]) != 1 {
		t.Errorf("hooks %+v", h)
	}
	if !strings.Contains(report, "hooks.SessionStart: its lanes hook has no timeout") {
		t.Errorf("report:\n%s", report)
	}
	if slices.ContainsFunc(res.Files, func(f File) bool { return f.Path == EnvrcFile }) || strings.Contains(report, "direnv allow") {
		t.Errorf("an .envrc already wired is rewritten:\n%s", report)
	}
}

func TestFormerHarness(t *testing.T) {
	settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "TOKEN=s3cr3t ./scripts/worktree-up.sh"}]}]}}`
	envrc := "export DB_PORT=3307\nexport DATABASE_URL=mysql://app:s3cr3t@localhost:3307/app\nexport FIRESTORE_EMULATOR_HOST=127.0.0.1:8080\nexport EDITOR=vim\n"
	dir := repo(t, map[string]string{SettingsFile: settings, EnvrcFile: envrc})
	_, report := initRepo(t, dir)
	for _, w := range []string{
		".envrc:1: sets DB_PORT",
		".envrc:2: sets DATABASE_URL",
		".envrc:3: sets FIRESTORE_EMULATOR_HOST",
		".claude/settings.json hooks.SessionStart[0].hooks[0]: a SessionStart hook that is not lanes'",
	} {
		if !strings.Contains(report, w) {
			t.Errorf("the report lacks %q:\n%s", w, report)
		}
	}
	for _, leak := range []string{"EDITOR", "s3cr3t", "3307", "worktree-up.sh"} {
		if strings.Contains(report, leak) {
			t.Errorf("the report shows %q:\n%s", leak, report)
		}
	}
	if got := readFile(t, dir, EnvrcFile); !strings.HasPrefix(got, envrc) {
		t.Errorf("the harness lines were touched:\n%s", got)
	}
	if !strings.Contains(readFile(t, dir, SettingsFile), "./scripts/worktree-up.sh") {
		t.Error("the harness hook was removed")
	}
}

func TestGitignore(t *testing.T) {
	fb := `{"emulators": {"auth": {}}}`
	t.Run("data already ignored", func(t *testing.T) {
		dir := repo(t, map[string]string{"firebase.json": fb, ".gitignore": "node_modules\n/.emu-data/\n.emu-data.next\n.emu-data.old"})
		initRepo(t, dir)
		want := "node_modules\n/.emu-data/\n.emu-data.next\n.emu-data.old\n\n# lanes\nfirebase.lanes.json\nfirebase.lanes.test.json\n"
		if got := readFile(t, dir, ".gitignore"); got != want {
			t.Errorf(".gitignore:\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("created", func(t *testing.T) {
		dir := repo(t, map[string]string{"firebase.json": fb})
		initRepo(t, dir)
		want := "# lanes\nfirebase.lanes.json\nfirebase.lanes.test.json\n.emu-data\n.emu-data.next\n.emu-data.old\n"
		if got := readFile(t, dir, ".gitignore"); got != want {
			t.Errorf(".gitignore:\n%q", got)
		}
	})
	t.Run("no firebase", func(t *testing.T) {
		dir := repo(t, map[string]string{".gitignore": "x\n"})
		initRepo(t, dir)
		if got := readFile(t, dir, ".gitignore"); got != "x\n" {
			t.Errorf(".gitignore changed:\n%q", got)
		}
	})
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := repo(t, map[string]string{"firebase.json": `{"emulators": {"auth": {}}}`, EnvrcFile: "use asdf\n", "package-lock.json": "{}"})
	res := plan(t, dir)
	var out bytes.Buffer
	res.WriteDryRun(&out)
	res.WriteReport(&out, true)
	if s := status(t, dir); s != "" {
		t.Fatalf("a dry run changed the tree:\n%s", s)
	}
	o := out.String()
	for _, w := range []string{"== lanes.yaml (write) ==\nschema: 2\n", "== process-compose.yaml (write) ==", "== .claude/settings.json (write) ==\n{\n", "== .envrc (append) ==\n" + EnvrcLine + "\n", "== .gitignore (write) ==\n# lanes\n", "nothing written", "To fill in"} {
		if !strings.Contains(o, w) {
			t.Errorf("the dry run lacks %q:\n%s", w, o)
		}
	}
	if strings.Contains(o, "use asdf") {
		t.Errorf("the dry run shows the whole .envrc:\n%s", o)
	}
}

func TestNothingDetected(t *testing.T) {
	dir := repo(t, nil)
	_, report := initRepo(t, dir)
	y := readFile(t, dir, "lanes.yaml")
	var keys map[string]any
	if err := unmarshalYAML(y, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys["schema"] == nil || keys["ports"] == nil {
		t.Errorf("lanes.yaml keys %v:\n%s", keysOf(keys), y)
	}
	cfg := parse(t, dir)
	if cfg.Ports.Block != 10 || len(cfg.Ports.Names) != 0 {
		t.Errorf("ports %+v", cfg.Ports)
	}
	if pcYAML := readFile(t, dir, "process-compose.yaml"); !strings.Contains(pcYAML, "version: \"0.5\"") || !strings.Contains(pcYAML, "processes: {}") {
		t.Errorf("process-compose.yaml:\n%s", pcYAML)
	}
	h := hooks(t, dir)
	for event, name := range map[string]string{"SessionStart": "session-start", "CwdChanged": "cwd-changed", "PostToolUse": "cwd-changed"} {
		if to, ok := lanesTimeout(t, h[event], name); !ok || to != nil {
			t.Errorf("%s: present %v timeout %v", event, ok, to)
		}
	}
	if readFile(t, dir, EnvrcFile) != EnvrcLine+"\n" {
		t.Error(".envrc")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err == nil {
		t.Error(".gitignore written without a firebase resource")
	}
	if !strings.Contains(report, "Nothing detected") {
		t.Errorf("report:\n%s", report)
	}
}

func TestRunsFromSubdirectory(t *testing.T) {
	dir := repo(t, map[string]string{"pkg/main.go": "package main\n"})
	res := plan(t, filepath.Join(dir, "pkg"))
	if res.Root != dir {
		t.Fatalf("root %s, want %s", res.Root, dir)
	}
}

func TestEnvrcWithoutFinalNewline(t *testing.T) {
	dir := repo(t, map[string]string{EnvrcFile: "use asdf"})
	initRepo(t, dir)
	if got := readFile(t, dir, EnvrcFile); got != "use asdf\n"+EnvrcLine+"\n" {
		t.Errorf(".envrc %q", got)
	}
}

func TestDevCommand(t *testing.T) {
	for text, dev := range map[string]bool{
		"vite": true, "vite dev": true, "vite serve": true, "vite --host": true, "node_modules/.bin/vite": true,
		"storybook dev -p 6006": true, "start-storybook -p 6006": true,
		"vite build": false, "vite preview": false, "vite optimize": false, "storybook build": false,
		"build-storybook": false, "vitest": false, "vitest run": false, "invite": false,
	} {
		if _, ok := devCommand(text); ok != dev {
			t.Errorf("devCommand(%q) = %v, want %v", text, ok, dev)
		}
	}
}

func TestShortPort(t *testing.T) {
	for in, want := range map[string][2]string{
		"3306":                             {"", "3306"},
		"3307:3306":                        {"3307", "3306"},
		"${DB_PORT:-3306}:3306":            {"${DB_PORT:-3306}", "3306"},
		"127.0.0.1:3308:3306/tcp":          {"3308", "3306"},
		"[::1]:3309:3306":                  {"3309", "3306"},
		"${HOST:-0.0.0.0}:${P}:${C:-3306}": {"${P}", "${C:-3306}"},
	} {
		h, c := splitShortPort(in)
		if h != want[0] || c != want[1] {
			t.Errorf("splitShortPort(%q) = %q, %q", in, h, c)
		}
	}
}
