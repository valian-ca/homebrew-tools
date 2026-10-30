//go:build integration && darwin

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMySQLStartWakesTheServerAndCreatesTheLaneDatabases(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack))
	h.startDaemon()
	wt := h.worktree("db", "feat/db")
	h.session(wt)

	h.mustLanes(wt, "start")
	l := h.laneOf(wt)
	dev, test := devDB(l.Lane), testDB(l.Lane)
	if starts(h.main) != 1 {
		t.Fatalf("the start command ran %d times", starts(h.main))
	}
	if !h.hasDB(dev) || !h.hasDB(test) {
		t.Fatalf("databases %v lack %s or %s", h.databases(), dev, test)
	}
	if got := h.registeredDBs(h.main); !slices.Equal(got, []string{dev, test}) {
		t.Fatalf("registered %v", got)
	}
	var listed []string
	for _, r := range l.Resources {
		if r.Adapter == "mysql" {
			listed = append(listed, r.Resource)
		}
	}
	slices.Sort(listed)
	if !slices.Equal(listed, []string{dev, test}) {
		t.Fatalf("lanes list --json resources = %+v", l.Resources)
	}
	if text := h.mustLanes(h.main, "list"); strings.Contains(text, dev) || strings.Contains(text, "resources") {
		t.Fatalf("the text output of lanes list changed:\n%s", text)
	}
	url := "mysql://root@127.0.0.1:" + strconv.Itoa(h.mysqlPort) + "/" + dev
	h.eventually("the seed task to run", 30*time.Second, func() bool { return len(h.seedLog(l.Lane)) > 0 })
	if got := h.seedLog(l.Lane)[0]; got != "dev=tables:0 test=tables:0 url="+url {
		t.Fatalf("the first piece found %q: the lane's databases must exist, empty", got)
	}
	h.eventually("the seed to fill the dev database", 30*time.Second, func() bool { return slices.Contains(h.tables(dev), "lanes_seed") })

	if got := read(filepath.Join(wt, "db.env")); got != "DATABASE_URL="+url+"\nTEST_DATABASE="+test+"\n" {
		t.Fatalf("generated db.env:\n%s", got)
	}
	if got := read(filepath.Join(wt, ".demo", "installs")); !strings.Contains(got, " "+url+"\n") {
		t.Fatalf("the setup step saw %q", got)
	}
	printVars := `printf '%s\n' "$DEMO_DATABASE_URL" "$LANES_MYSQL_DATABASE_DEV" "$LANES_MYSQL_DATABASE_TEST"`
	if got := h.mustLanes(wt, "test-env", "--", "/bin/sh", "-c", printVars); got != url+"\n"+dev+"\n"+test+"\n" {
		t.Fatalf("lanes test-env sees %q", got)
	}
	if out := h.lanes(h.main, "env"); out.code != 0 || out.stdout != "" {
		t.Fatalf("lanes env on the main checkout: %s", out)
	}
	h.mustLanes(wt, "stop")
	h.git(wt, "switch", "-q", "-c", "feat/db-2", "origin/main")
	h.mustLanes(wt, "env")
	if st := h.git(wt, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("git status after lanes:\n%s", st)
	}
}

func TestMySQLLaneEnvReachesTheDevDatabaseAndUnsets(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack))
	h.startDaemon()
	wt := h.worktree("env", "feat/env")
	h.mustLanes(wt, "start")
	lane := h.laneOf(wt).Lane
	h.eventually("the seed to fill the dev database", 30*time.Second, func() bool { return slices.Contains(h.tables(devDB(lane)), "lanes_seed") })

	script := `eval "$(lanes env)"
printf '%s\n' "$LANES_MYSQL_HOST" "$LANES_MYSQL_PORT" "$LANES_MYSQL_USER" "[${LANES_MYSQL_PASSWORD-unset}]" "$LANES_MYSQL_DATABASE_DEV" "$LANES_MYSQL_DATABASE_TEST" "$DEMO_DATABASE_URL" "$DEMO_WEB_URL"
"$MYSQL" --no-defaults --protocol=TCP -h "$LANES_MYSQL_HOST" -P "$LANES_MYSQL_PORT" -u "$LANES_MYSQL_USER" "$LANES_MYSQL_DATABASE_DEV" -e 'CREATE TABLE from_shell (i INT)'
eval "$(lanes env --unset)"
env`
	c := exec.Command("/bin/bash", "-c", script)
	c.Dir, c.Env = wt, append(append([]string(nil), h.env...), "MYSQL="+filepath.Join(filepath.Dir(mysqldPath()), "mysql"))
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lines := strings.Split(string(out), "\n")
	port := strconv.Itoa(h.mysqlPort)
	want := []string{"127.0.0.1", port, "root", "[]", devDB(lane), testDB(lane), "mysql://root@127.0.0.1:" + port + "/" + devDB(lane), "http://127.0.0.1:" + strconv.Itoa(h.port(wt, "web"))}
	if !slices.Equal(lines[:len(want)], want) {
		t.Fatalf("lanes env exported %q, want %q", lines[:len(want)], want)
	}
	if !slices.Contains(h.tables(devDB(lane)), "from_shell") {
		t.Fatal("a client with the lane's variables did not write in the lane's dev database")
	}
	for _, l := range lines[len(want):] {
		if strings.HasPrefix(l, "LANES_MYSQL_") || strings.HasPrefix(l, "DEMO_") {
			t.Errorf("%s survived lanes env --unset", l)
		}
	}
}

func TestMySQLConcurrentStartsRunTheStartCommandOnce(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack))
	h.startDaemon()
	a := h.worktree("a", "feat/a")
	b := h.worktree("b", "feat/b")
	var wg sync.WaitGroup
	results := make([]result, 2)
	for i, wt := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.lanes(wt, "start")
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.code != 0 {
			t.Fatalf("a concurrent start failed: %s", r)
		}
	}
	if n := starts(h.main); n != 1 {
		t.Fatalf("the start command ran %d times", n)
	}
	runs := strings.Fields(strings.ReplaceAll(read(filepath.Join(h.main, ".demo", "mysql-start.log")), "\n", " "))
	for i := 0; i+2 < len(runs); i += 4 {
		if runs[i] != "begin" || runs[i+2] != "end" {
			t.Fatalf("two start commands overlapped: %v", runs)
		}
	}
	for _, wt := range []string{a, b} {
		lane := h.laneOf(wt).Lane
		if !h.hasDB(devDB(lane)) || !h.hasDB(testDB(lane)) || h.laneOf(wt).State != "running" {
			t.Fatalf("%s: databases %v, state %s", wt, h.databases(), h.laneOf(wt).State)
		}
	}
}

func TestMySQLStartRefusals(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack), withEnv("LANES_MYSQL_WAKE_TIMEOUT=2s"))
	h.startDaemon()
	wt := h.worktree("refuse", "feat/refuse")
	addr := "127.0.0.1:" + strconv.Itoa(h.mysqlPort)
	nothingRuns := func(what string) {
		t.Helper()
		if l := h.laneOf(wt); l.State != "stopped" || len(h.registeredDBs(h.main)) != 0 {
			t.Fatalf("%s: state %s, registered %v", what, l.State, h.registeredDBs(h.main))
		}
	}

	h.mysqlSpec(wt, func(m map[string]any) { m["start"] = "echo first line; echo boom >&2; exit 3" })
	if r := h.lanes(wt, "start"); r.code == 0 || !strings.Contains(r.stderr, "boom") {
		t.Fatalf("a failing start command: %s", r)
	}
	nothingRuns("a failing start command")

	h.mysqlSpec(wt, func(m map[string]any) { m["start"] = "echo started nothing" })
	if r := h.lanes(wt, "start"); r.code == 0 || !strings.Contains(r.stderr, "did not accept connections within 2s") || !strings.Contains(r.stderr, "started nothing") {
		t.Fatalf("a start that brings no server: %s", r)
	}
	nothingRuns("a start that brings no server")

	h.mysqlSpec(wt, func(m map[string]any) { delete(m, "start") })
	if r := h.lanes(wt, "start"); r.code == 0 || !strings.Contains(r.stderr, addr) || !strings.Contains(r.stderr, "no start command") {
		t.Fatalf("a sleeping server without start: %s", r)
	}
	nothingRuns("a sleeping server without start")
}

func TestMySQLPortGuard(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack))
	h.startDaemon()
	wt := h.worktree("guard", "feat/guard")
	nc := h.spawn(t.TempDir(), "/usr/bin/nc", "-lk", "127.0.0.1", strconv.Itoa(h.mysqlPort))
	h.eventually("nc to listen", 10*time.Second, func() bool { return listening(h.mysqlPort) })

	r := h.lanes(wt, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "held by nc (PID "+strconv.Itoa(nc.Process.Pid)+")") {
		t.Fatalf("a foreign listener on the port: %s", r)
	}
	if starts(h.main) != 0 || len(h.registeredDBs(h.main)) != 0 || h.laneOf(wt).State != "stopped" {
		t.Fatalf("the guard let something through: %d starts, registered %v", starts(h.main), h.registeredDBs(h.main))
	}
	_ = nc.Process.Kill()
	h.eventually("nc to free the port", 10*time.Second, func() bool { return !listening(h.mysqlPort) })
	h.mustLanes(wt, "start")
	if lane := h.laneOf(wt).Lane; !h.hasDB(devDB(lane)) {
		t.Fatal("mysqld on the port, listeners undeclared: the start did not pass")
	}
}

func TestMySQLDataSurvivesAStopAndADroppedBaseComesBackEmpty(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack))
	h.startDaemon()
	wt := h.worktree("keep", "feat/keep")
	h.session(wt)
	h.mustLanes(wt, "start")
	lane := h.laneOf(wt).Lane
	dev, test := devDB(lane), testDB(lane)
	h.eventually("the seed to fill the dev database", 30*time.Second, func() bool { return slices.Contains(h.tables(dev), "lanes_seed") })
	pid := h.mysqldPID()

	h.exec(dev, "CREATE TABLE agent_work (i INT)")
	h.mustLanes(wt, "stop")
	h.mustLanes(wt, "start")
	if !slices.Contains(h.tables(dev), "agent_work") {
		t.Fatal("a table of the dev database did not survive lanes stop and start")
	}
	if h.mysqldPID() != pid || starts(h.main) != 1 {
		t.Fatalf("the server did not run throughout: pid %d → %d, %d starts", pid, h.mysqldPID(), starts(h.main))
	}

	h.exec(test, "CREATE TABLE marker (i INT)")
	h.exec("", "DROP DATABASE `"+dev+"`")
	h.mustLanes(wt, "start")
	if !h.hasDB(dev) || len(h.tables(dev)) != 0 {
		t.Fatalf("the dropped dev database: exists %v, tables %v", h.hasDB(dev), h.tables(dev))
	}
	if got := h.tables(test); !slices.Equal(got, []string{"marker"}) {
		t.Fatalf("the test database changed: %v", got)
	}
}

func TestMySQLNewLaneErasesTheOldDatabases(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack))
	h.startDaemon()
	wt := h.worktree("switch", "feat/first")
	h.mustLanes(wt, "start")
	old := h.laneOf(wt).Lane
	for _, n := range []string{"lanes_demo", "lanes_demo_test", "lanes_demo_scratch"} {
		h.exec("", "CREATE DATABASE "+n)
	}

	h.git(wt, "switch", "-q", "-c", "feat/second", "origin/main")
	h.mustLanes(wt, "env")
	if h.hasDB(devDB(old)) || h.hasDB(testDB(old)) || len(h.registeredDBs(h.main)) != 0 {
		t.Fatalf("after the lane change: databases %v, registered %v", h.databases(), h.registeredDBs(h.main))
	}
	h.mustLanes(wt, "start")
	lane := h.laneOf(wt).Lane
	if lane == old || !h.hasDB(devDB(lane)) || !h.hasDB(testDB(lane)) {
		t.Fatalf("the new lane %s: databases %v", lane, h.databases())
	}
	h.eventually("the seed task of the new lane", 30*time.Second, func() bool { return len(h.seedLog(lane)) > 0 })
	if got := h.seedLog(lane)[0]; !strings.HasPrefix(got, "dev=tables:0 test=tables:0 ") {
		t.Fatalf("the new lane's databases at its first piece: %q", got)
	}
	h.mustLanes(wt, "stop")
	for _, n := range []string{"lanes_demo", "lanes_demo_test", "lanes_demo_scratch"} {
		if !h.hasDB(n) {
			t.Errorf("%s, not a lane's database, was erased", n)
		}
	}
}

func TestMySQLNewLaneWhileTheServerSleeps(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack), grace(time.Hour), withEnv("LANES_RECONCILE_RETRY=2s"))
	h.startDaemon()
	wt := h.worktree("asleep", "feat/asleep")
	h.mustLanes(wt, "start")
	old := h.laneOf(wt).Lane
	h.mustLanes(wt, "stop")
	h.stopMySQL()

	h.git(wt, "switch", "-q", "-c", "feat/asleep-2", "origin/main")
	envFile := filepath.Join(t.TempDir(), "claude-env")
	r := h.lanesIn(h.main, payload(wt), []string{"CLAUDE_ENV_FILE=" + envFile}, "hook", "session-start")
	if r.code != 0 || !strings.Contains(r.stderr, "erase deferred") {
		t.Fatalf("the hook with the server asleep: %s", r)
	}
	lane := h.laneOf(wt).Lane
	if lane == old || !strings.Contains(read(envFile), "LANES_MYSQL_DATABASE_DEV='"+devDB(lane)+"'") {
		t.Fatalf("the session's env file:\n%s", read(envFile))
	}
	if out := h.mustLanes(wt, "env"); !strings.Contains(out, "LANES_MYSQL_DATABASE_TEST='"+testDB(lane)+"'") {
		t.Fatalf("lanes env with the server asleep:\n%s", out)
	}
	h.stopDaemon()
	h.startDaemon()
	time.Sleep(5 * time.Second)
	if starts(h.main) != 1 {
		t.Fatalf("something woke the server: %d starts", starts(h.main))
	}
	if got := h.registeredDBs(h.main); !slices.Equal(got, []string{devDB(old), testDB(old)}) {
		t.Fatalf("the old lane's databases while the server sleeps: registered %v", got)
	}

	h.startMySQL()
	h.eventually("the reconcile to erase the old lane's databases", time.Minute, func() bool {
		return !h.hasDB(devDB(old)) && !h.hasDB(testDB(old)) && len(h.registeredDBs(h.main)) == 0
	})
}

func TestMySQLNewLaneAtAHookBehindAForeignListener(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack), grace(time.Hour))
	h.startDaemon()
	wt := h.worktree("proxy", "feat/proxy")
	h.mustLanes(wt, "start")
	old := h.laneOf(wt).Lane
	h.mustLanes(wt, "stop")
	h.stopMySQL()
	h.spawn(t.TempDir(), "/usr/bin/nc", "-lk", "127.0.0.1", strconv.Itoa(h.mysqlPort))
	h.eventually("nc to listen", 10*time.Second, func() bool { return listening(h.mysqlPort) })

	h.git(wt, "switch", "-q", "-c", "feat/proxy-2", "origin/main")
	envFile := filepath.Join(t.TempDir(), "claude-env")
	r := h.lanesIn(h.main, payload(wt), []string{"CLAUDE_ENV_FILE=" + envFile}, "hook", "session-start")
	if r.code != 0 || !strings.Contains(r.stderr, "held by nc") {
		t.Fatalf("the hook behind a foreign listener: %s", r)
	}
	if lane := h.laneOf(wt).Lane; lane == old || !strings.Contains(read(envFile), "LANE_ID='"+lane+"'") {
		t.Fatalf("the session did not move to the new lane:\n%s", read(envFile))
	}
	if got := h.registeredDBs(h.main); !slices.Equal(got, []string{devDB(old), testDB(old)}) {
		t.Fatalf("registered %v", got)
	}
}

func TestMySQLWorktreeRemovedErasesOnlyItsLaneDatabases(t *testing.T) {
	h := newHarness(t, withProfile(mysqlStack), grace(time.Hour))
	h.startDaemon()
	other := h.repo("other")
	gone := h.worktree("gone", "feat/gone")
	kept := h.worktreeIn(other, "kept", "feat/kept")
	h.mustLanes(gone, "start")
	h.mustLanes(kept, "start")
	goneLane, keptLane := h.laneOf(gone).Lane, h.laneOf(kept).Lane
	orphan := devDB("feat_crashed_0a0b0c")
	mains := []string{"lanes_demo", "lanes_demo_test", "lanes_demo_scratch"}
	for _, n := range append([]string{orphan}, mains...) {
		h.exec("", "CREATE DATABASE "+n)
	}

	removed := time.Now()
	h.git(h.main, "worktree", "remove", "--force", gone)
	h.eventually("the gone worktree's databases and the orphan to be erased", 2*time.Minute, func() bool {
		return !h.hasDB(devDB(goneLane)) && !h.hasDB(testDB(goneLane)) && !h.hasDB(orphan)
	})
	t.Logf("erased %s after removal", time.Since(removed).Round(time.Second))
	// The kept lane's test database is also the dev database of the dead lane
	// test_<kept lane>, and is registered only in the other repository.
	for _, n := range append([]string{devDB(keptLane), testDB(keptLane)}, mains...) {
		if !h.hasDB(n) {
			t.Errorf("%s was erased", n)
		}
	}
}

func TestEnvWithoutMySQL(t *testing.T) {
	h := newHarness(t)
	wt := h.worktree("plain", "feat/plain")
	h.editYAML(filepath.Join(wt, "lanes.yaml"), func(doc map[string]any) {
		doc["schema"] = 2
		doc["env"] = map[string]any{"DEMO_WEB_URL": "http://{{.LANES_HOST}}:{{.PORT_WEB}}"}
	})
	out := h.mustLanes(wt, "env")
	if !strings.Contains(out, "export DEMO_WEB_URL='http://127.0.0.1:"+strconv.Itoa(h.port(wt, "web"))+"'\n") || strings.Contains(out, "LANES_MYSQL_") {
		t.Fatalf("lanes env:\n%s", out)
	}
}

func TestLanesYAMLRefusals(t *testing.T) {
	h := newHarness(t)
	wt := h.worktree("bad", "feat/bad")
	path := filepath.Join(wt, "lanes.yaml")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mysql := map[string]any{"mysql": map[string]any{"databases": map[string]any{"dev": "app_{lane}"}}}
	cases := map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"mysql in schema 1": {func(d map[string]any) { d["resources"] = append(d["resources"].([]any), mysql) }, "resources[1].mysql: requires schema: 2"},
		"env in schema 1":   {func(d map[string]any) { d["env"] = map[string]any{"A": "b"} }, "env: requires schema: 2"},
		"two mysql":         {func(d map[string]any) { d["schema"] = 2; d["resources"] = append(d["resources"].([]any), mysql, mysql) }, "resources[2].mysql: at most one mysql resource"},
		"env redefines":     {func(d map[string]any) { d["schema"] = 2; d["env"] = map[string]any{"PORT_WEB": "1"} }, "env.PORT_WEB: redefines a variable lanes sets"},
	}
	for name, c := range cases {
		if err := os.WriteFile(path, good, 0o644); err != nil {
			t.Fatal(err)
		}
		h.editYAML(path, c.edit)
		if r := h.lanes(wt, "env"); r.code == 0 || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: %s", name, r)
		}
	}
}

func TestEnvEntryThatDoesNotRender(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("render", "feat/render")
	h.editYAML(filepath.Join(wt, "lanes.yaml"), func(doc map[string]any) {
		doc["schema"] = 2
		doc["env"] = map[string]any{"GOOD": "x", "BROKEN": "{{.NOPE}}"}
	})

	if r := h.lanes(wt, "env"); r.code == 0 || !strings.Contains(r.stderr, "env.BROKEN") {
		t.Fatalf("lanes env: %s", r)
	}
	if r := h.lanes(wt, "start"); r.code == 0 || !strings.Contains(r.stderr, "env.BROKEN") {
		t.Fatalf("lanes start: %s", r)
	}
	if l := h.laneOf(wt); l.State != "stopped" {
		t.Fatalf("a piece started: %+v", l)
	}
	envFile := filepath.Join(t.TempDir(), "claude-env")
	r := h.lanesIn(h.main, payload(wt), []string{"CLAUDE_ENV_FILE=" + envFile}, "hook", "session-start")
	if r.code != 0 || !strings.Contains(r.stderr, "env.BROKEN") || !strings.Contains(r.stderr, "the env: variables are left out") {
		t.Fatalf("the hook: %s", r)
	}
	if got := read(envFile); !strings.Contains(got, "export LANE_ID=") || strings.Contains(got, "GOOD=") || strings.Contains(got, "BROKEN=") {
		t.Fatalf("the session's env file:\n%s", got)
	}
	if r := h.lanes(wt, "test-env", "--", "true"); r.code != 0 || !strings.Contains(r.stderr, "the env: variables are left out") {
		t.Fatalf("lanes test-env: %s", r)
	}
}
