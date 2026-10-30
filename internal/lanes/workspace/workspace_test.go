package workspace

import (
	"slices"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
)

func TestRenderEnvSeesTheLaneAndThePreviousEntries(t *testing.T) {
	base := env.Vars{{Name: "LANES_MYSQL_DATABASE_DEV", Value: "app_l_abc123"}, {Name: "PORT_WEB", Value: "42001"}}
	got, err := renderEnv(config.EnvVars{
		{Name: "DB_URL", Template: "jdbc:mysql://h/{{.LANES_MYSQL_DATABASE_DEV}}"},
		{Name: "BOTH", Template: "{{.DB_URL}}@{{.PORT_WEB}}"},
	}, base)
	if err != nil {
		t.Fatal(err)
	}
	if m := got.Map(); m["DB_URL"] != "jdbc:mysql://h/app_l_abc123" || m["BOTH"] != "jdbc:mysql://h/app_l_abc123@42001" || len(got) != 2 {
		t.Fatalf("rendered %+v", got)
	}
}

func TestRenderEnvNamesTheFailingEntry(t *testing.T) {
	_, err := renderEnv(config.EnvVars{{Name: "URL", Template: "{{.LANES_MYSQL_HOST}}"}}, env.Vars{})
	if err == nil || !strings.HasPrefix(err.Error(), "env.URL: ") {
		t.Fatalf("a variable the lane lacks: %v", err)
	}
}

func TestOutsideEnvironDropsTheLaneVariables(t *testing.T) {
	t.Setenv("LANES_VARS", "LANE_ID PORT_WEB PROJECT_URL")
	t.Setenv("LANE_ID", "old_abc123")
	t.Setenv("PORT_WEB", "42001")
	t.Setenv("PROJECT_URL", "http://x")
	t.Setenv("LANES_MYSQL_DATABASE_DEV", "app_old_abc123")
	t.Setenv("TMPDIR", "/var/folders/user")
	t.Setenv("KEEP_ME", "1")
	w := &Workspace{Vars: env.Vars{{Name: "LANES_WORKTREE", Value: "/w"}}}
	names := []string{}
	for _, kv := range w.OutsideEnviron() {
		k, _, _ := strings.Cut(kv, "=")
		names = append(names, k)
	}
	for _, gone := range []string{"LANES_VARS", "LANE_ID", "PORT_WEB", "PROJECT_URL", "LANES_MYSQL_DATABASE_DEV"} {
		if slices.Contains(names, gone) {
			t.Errorf("%s reached the outside environment", gone)
		}
	}
	for _, kept := range []string{"TMPDIR", "KEEP_ME", "PATH"} {
		if !slices.Contains(names, kept) {
			t.Errorf("%s was dropped", kept)
		}
	}
}
