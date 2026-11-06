package adopt

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

func TestFirebaseAndMySQL(t *testing.T) {
	dir := repo(t, map[string]string{
		"firebase.json": `{"emulators": {"auth": {}}}`,
		"package.json":  `{"devDependencies": {"firebase-tools": "15"}}`,
		"compose.yaml":  composeMySQL,
	})
	_, report := initRepo(t, dir)
	if !strings.Contains(report, "lanes.yaml resources[1].mysql.databases") || strings.Contains(report, "a mysql resource, if the project's MySQL server runs without docker-compose") {
		t.Errorf("report:\n%s", report)
	}
	y := readFile(t, dir, "lanes.yaml")
	if _, err := config.Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "resources[1].mysql.databases: required") {
		t.Fatalf("unfilled: %v", err)
	}
	cfg, err := config.Parse([]byte(strings.Replace(y, "databases: {}", "databases:\n        dev: app_{lane}", 1)))
	if err != nil || cfg.Firebase() == nil || cfg.MySQL() == nil {
		t.Fatalf("filled: %v %+v", err, cfg)
	}
}

func TestAlreadyWired(t *testing.T) {
	var settings string
	for _, f := range plan(t, repo(t, nil)).Files {
		if f.Path == SettingsFile {
			settings = string(f.Content)
		}
	}
	dir := repo(t, map[string]string{
		SettingsFile:    settings,
		EnvrcFile:       EnvrcLine + "\n",
		".gitignore":    "firebase.lanes.json\nfirebase.lanes.test.json\n.emu-data\n.emu-data.next\n.emu-data.old\n",
		"firebase.json": `{"emulators": {"auth": {}}}`,
	})
	res, report := initRepo(t, dir)
	var paths []string
	for _, f := range res.Files {
		paths = append(paths, f.Path)
	}
	if !slices.Equal(paths, []string{"lanes.yaml", "process-compose.yaml"}) {
		t.Errorf("files %v", paths)
	}
	for _, w := range []string{"Unchanged:", "  " + SettingsFile, "  " + EnvrcFile, "  .gitignore", "Commit lanes.yaml, process-compose.yaml."} {
		if !strings.Contains(report, w) {
			t.Errorf("the report lacks %q:\n%s", w, report)
		}
	}
	if strings.Contains(report, "direnv allow") {
		t.Errorf("direnv allow without an .envrc change:\n%s", report)
	}
	if got := readFile(t, dir, SettingsFile); got != settings {
		t.Errorf("settings rewritten:\n%s", got)
	}
}

func TestMySQLFallbacks(t *testing.T) {
	head := "services:\n  db:\n    image: mysql:8\n"
	cases := []struct {
		name, compose, want, wantNot, report string
	}{
		{"no published port", head + "    environment: {MYSQL_ROOT_PASSWORD: pw}\n", "# lanes init: port —", "port: ", "resources[0].mysql.port"},
		{"password from the shell, list", head + "    ports: [\"3306:3306\"]\n    environment: [MYSQL_ROOT_PASSWORD]\n", "# lanes init: password —", "password:", "takes MYSQL_ROOT_PASSWORD from the shell"},
		{"password from the shell, map", head + "    ports: [\"3306:3306\"]\n    environment:\n      MYSQL_ROOT_PASSWORD:\n", "# lanes init: password —", "password:", "takes MYSQL_ROOT_PASSWORD from the shell"},
		{"empty password refused", head + "    ports: [\"3306:3306\"]\n    environment: {MYSQL_ALLOW_EMPTY_PASSWORD: \"false\"}\n", "# lanes init: password —", "password:", "resources[0].mysql.password"},
		{"no environment", head + "    ports: [\"3306:3306\"]\n", "# lanes init: password —", "password:", "sets no MYSQL_ROOT_PASSWORD"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := repo(t, map[string]string{"compose.yaml": c.compose})
			_, report := initRepo(t, dir)
			y := readFile(t, dir, "lanes.yaml")
			if !strings.Contains(y, c.want) {
				t.Errorf("lanes.yaml lacks %q:\n%s", c.want, y)
			}
			for _, line := range strings.Split(y, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), c.wantNot) {
					t.Errorf("lanes.yaml has %q:\n%s", line, y)
				}
			}
			if !strings.Contains(report, c.report) {
				t.Errorf("the report lacks %q:\n%s", c.report, report)
			}
		})
	}
}

func TestComposeDotenv(t *testing.T) {
	compose := "services:\n  db:\n    image: mysql:8\n    ports: [\"${DB_PORT:-3306}:3306\"]\n    environment:\n      MYSQL_ROOT_PASSWORD: ${DB_PASSWORD:-secret}\n"
	dir := repo(t, map[string]string{"pilote/docker-compose.yaml": compose, ".gitignore": ".env\n"})
	testutil.Write(t, filepath.Join(dir, "pilote/.env"), "# local\nDB_PORT=3307\nexport DB_PASSWORD='from env'\n")
	_, report := initRepo(t, dir)
	y := readFile(t, dir, "lanes.yaml")
	if !strings.Contains(y, "port: 3307") || !strings.Contains(y, "password: from env") {
		t.Errorf("lanes.yaml ignores the compose .env:\n%s", y)
	}
	if !strings.Contains(report, "DB_PORT, DB_PASSWORD read from the .env") || strings.Contains(report, "from env") {
		t.Errorf("report:\n%s", report)
	}

	dir = repo(t, map[string]string{"compose.yaml": compose})
	_, report = initRepo(t, dir)
	if !strings.Contains(readFile(t, dir, "lanes.yaml"), "port: 3306") || !strings.Contains(report, "DB_PORT, DB_PASSWORD not set in the .env") {
		t.Errorf("defaults not reported:\n%s", report)
	}
}

func TestSettingsShapesRefused(t *testing.T) {
	for _, s := range []string{`[]`, `{"hooks": []}`, `{"hooks": {"SessionStart": {}}}`} {
		dir := repo(t, map[string]string{SettingsFile: s})
		if _, err := Plan(context.Background(), dir); err == nil || !strings.Contains(err.Error(), SettingsFile) {
			t.Errorf("%s: %v", s, err)
		}
		if st := status(t, dir); st != "" {
			t.Errorf("%s: the tree changed:\n%s", s, st)
		}
	}
	dir := repo(t, map[string]string{SettingsFile: "  \n"})
	res, _ := initRepo(t, dir)
	if !slices.ContainsFunc(res.Files, func(f File) bool { return f.Path == SettingsFile && f.Mode == Merge }) {
		t.Errorf("a blank settings file is not merged: %+v", res.Files)
	}
	if h := hooks(t, dir); len(h) != 4 {
		t.Errorf("hooks %+v", h)
	}
}

func TestMalformedInputsSkipped(t *testing.T) {
	dir := repo(t, map[string]string{"firebase.json": "{ nope", "web/package.json": "{ nope", "compose.yaml": "services: [\n"})
	_, report := initRepo(t, dir)
	for _, w := range []string{"firebase.json: not a valid Firebase config", "web/package.json: not a valid package.json", "compose.yaml: not a valid compose file"} {
		if !strings.Contains(report, w) {
			t.Errorf("the report lacks %q:\n%s", w, report)
		}
	}
}

func TestSymlinksRefused(t *testing.T) {
	for _, rel := range []string{EnvrcFile, SettingsFile, ".gitignore"} {
		t.Run(rel, func(t *testing.T) {
			dir := repo(t, map[string]string{"shared/target": "{}\n", "firebase.json": `{"emulators": {"auth": {}}}`})
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir, "shared/target"), filepath.Join(dir, rel)); err != nil {
				t.Fatal(err)
			}
			if _, err := Plan(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "symbolic link") {
				t.Fatalf("Plan: %v", err)
			}
		})
	}
	t.Run(".claude directory", func(t *testing.T) {
		dir := repo(t, map[string]string{"shared/settings.json": "{}\n"})
		if err := os.Symlink(filepath.Join(dir, "shared"), filepath.Join(dir, ".claude")); err != nil {
			t.Fatal(err)
		}
		if _, err := Plan(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("Plan: %v", err)
		}
	})
}

func TestShellQuoting(t *testing.T) {
	dir := repo(t, map[string]string{
		"my app/pubspec.lock":   "",
		"web/package.json":      `{"scripts": {"dev server": "vite"}}`,
		"web/package-lock.json": "{}",
	})
	initRepo(t, dir)
	cfg := parse(t, dir)
	if !slices.ContainsFunc(cfg.Setup, func(s config.Step) bool { return s.Run == "cd 'my app' && flutter pub get" }) {
		t.Errorf("setup %+v", cfg.Setup)
	}
	if pcYAML := readFile(t, dir, "process-compose.yaml"); !strings.Contains(pcYAML, `npm run 'dev server' -- --port`) {
		t.Errorf("process-compose.yaml:\n%s", pcYAML)
	}
}
