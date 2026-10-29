//go:build integration && darwin

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
)

const suiteSummary = "# tests 3"

func (h *harness) eventDelivered(wt, marker string) bool {
	return h.firestoreHas(wt, "events", marker)
}

func (h *harness) firestoreHas(wt, collection, text string) bool {
	resp := httpBody("GET", firestoreURL(h.port(wt, "firestore"), collection))
	return strings.Contains(resp, text)
}

func (h *harness) firebaseTree(wt string) []procinfo.Proc {
	return procsUnder(wt, func(p procinfo.Proc) bool { return p.Under(filepath.Join(wt, ".lanes", "firebase")) || isWorker(p) })
}

func TestFirebaseStackStarts(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("fb", "feat/fb")
	h.session(wt)
	h.mustLanes(wt, "start")

	h.writeDoc(wt, "items/item-04999")
	for _, n := range []string{"firestore", "auth", "pubsub"} {
		if !listening(h.port(wt, n)) {
			t.Errorf("%s does not listen on its port %d", n, h.port(wt, n))
		}
	}
	if !h.hasDoc(wt, "demo/hello") {
		t.Fatal("the seed was not imported")
	}
	log := h.pieceLog(wt, "firebase")
	if !strings.Contains(log, "Loaded functions definitions") || !strings.Contains(log, "importing "+filepath.Join(wt, "seed")) {
		t.Fatalf("firebase log:\n%s", log)
	}
	h.publish(wt, "ac1")
	h.eventually("the function to write events", time.Minute, func() bool { return h.eventDelivered(wt, "ac1") })

	if !strings.Contains(h.pieceLog(wt, "functions-build"), "build done") {
		t.Fatal("the build the project put before firebase did not run")
	}
	built, _ := os.Stat(filepath.Join(h.tmpdir(wt), "logs", "functions-build.log"))
	if started := birth(t, filepath.Join(h.tmpdir(wt), "logs", "firebase.log")); started.Before(built.ModTime().Add(-time.Second)) {
		t.Fatalf("firebase started at %s, before the build ended at %s", started, built.ModTime())
	}

	heaps := javaHeaps(t, wt)
	if len(heaps) < 2 || slices.ContainsFunc(heaps, func(s string) bool { return s != "-Xmx512m" }) {
		t.Fatalf("JVM heaps = %v, want -Xmx512m on each", heaps)
	}

	script := `set -e
eval "$(lanes env)"
env | grep -E '^(FIRESTORE|FIREBASE|PUBSUB|CLOUD_TASKS|GCLOUD|LANES_FIREBASE)' | sort
echo ---
node tool/write-doc.mjs ac13
echo ---
eval "$(lanes env --unset)"
env | grep -E '^(FIRESTORE|FIREBASE|PUBSUB|CLOUD_TASKS|GCLOUD|LANES_FIREBASE|LANES_HOST|PORT_)' || true`
	parts := strings.Split(h.run(wt, "bash", "-c", script), "---\n")
	if len(parts) != 3 {
		t.Fatalf("env script output: %q", parts)
	}
	for _, want := range []string{
		"FIRESTORE_EMULATOR_HOST=127.0.0.1:" + strconv.Itoa(h.port(wt, "firestore")),
		"FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:" + strconv.Itoa(h.port(wt, "auth")),
		"PUBSUB_EMULATOR_HOST=127.0.0.1:" + strconv.Itoa(h.port(wt, "pubsub")),
		"CLOUD_TASKS_EMULATOR_HOST=127.0.0.1:" + strconv.Itoa(h.port(wt, "tasks")),
		"FIREBASE_EMULATOR_HUB=127.0.0.1:" + strconv.Itoa(h.port(wt, "hub")),
		"LANES_FIREBASE_CONFIG=" + filepath.Join(wt, "firebase.lanes.json"),
	} {
		if !strings.Contains(parts[0], want+"\n") {
			t.Errorf("lanes env lacks %s:\n%s", want, parts[0])
		}
	}
	if strings.Contains(parts[0], "GCLOUD_PROJECT") || strings.Contains(parts[0], "FIREBASE_STORAGE") {
		t.Errorf("lanes env exports more than the started emulators:\n%s", parts[0])
	}
	if strings.TrimSpace(parts[1]) != "written/ac13" || !h.hasDoc(wt, "written/ac13") {
		t.Fatalf("the firebase-admin script did not write into the lane: %q", parts[1])
	}
	if strings.TrimSpace(parts[2]) != "" {
		t.Errorf("lanes env --unset left:\n%s", parts[2])
	}
}

func TestFirebaseDataLifecycle(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("data", "feat/data")
	data, seed := filepath.Join(wt, ".emu-data"), filepath.Join(wt, "seed")
	h.session(wt)
	seedHash := treeHash(t, seed)
	h.mustLanes(wt, "start")
	h.writeDoc(wt, "notes/kept")
	h.mustLanes(wt, "stop")
	if !exists(filepath.Join(data, "firebase-export-metadata.json")) || exists(data+".next") {
		t.Fatal("the stop did not swap a complete export into the lane's data")
	}
	h.mustLanes(wt, "start")
	if !h.hasDoc(wt, "notes/kept") || treeHash(t, seed) != seedHash {
		t.Fatal("stop then start lost the document or touched the seed")
	}

	dataHash := treeHash(t, data)
	r := h.lanes(wt, "test-env", "--", "npm", "run", "test:emu")
	if r.code != 0 || !strings.Contains(r.stdout, suiteSummary) || !strings.Contains(r.stdout, "# fail 0") {
		t.Fatalf("the suite under test-env: %s", r)
	}
	if !h.hasDoc(wt, "demo/hello") || treeHash(t, data) != dataHash {
		t.Fatal("test-env touched the stack or the lane's data")
	}

	h.git(wt, "switch", "-q", "-c", "feat/data-next", "origin/main")
	h.mustLanes(wt, "env")
	if exists(data) {
		t.Fatal("the new lane kept the old lane's data")
	}
	h.mustLanes(wt, "start")
	if h.hasDoc(wt, "notes/kept") || !h.hasDoc(wt, "demo/hello") {
		t.Fatal("the new lane did not start from the seed")
	}
}

func TestFirebaseExportAbandoned(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("abandon", "feat/abandon")
	h.firebaseSpec(wt, func(fb map[string]any) { fb["export_timeout"] = "10s" })
	h.session(wt)
	h.mustLanes(wt, "start")
	h.writeDoc(wt, "notes/a")
	h.mustLanes(wt, "stop")
	h.mustLanes(wt, "start")
	h.writeDoc(wt, "notes/b")

	jvms := procsUnder(wt, isFirestoreJVM)
	if len(jvms) != 1 {
		t.Fatalf("Firestore JVMs under the worktree: %v", jvms)
	}
	if err := syscall.Kill(jvms[0].PID, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	h.mustLanes(wt, "stop")
	if log := h.pieceLog(wt, "firebase"); !strings.Contains(log, "export abandoned after 10s (export_timeout)") {
		t.Fatalf("firebase log:\n%s", log)
	}
	h.eventually("no emulator left", 15*time.Second, func() bool { return len(h.firebaseTree(wt)) == 0 })
	if !exists(filepath.Join(wt, ".emu-data", "firebase-export-metadata.json")) || exists(filepath.Join(wt, ".emu-data.next")) {
		t.Fatal("the lane's data lost its last complete export")
	}
	h.mustLanes(wt, "start")
	if !h.hasDoc(wt, "notes/a") || h.hasDoc(wt, "notes/b") {
		t.Fatal("the lane did not restart from its last complete export")
	}
}

func TestFirebaseCaps(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("caps", "feat/caps")
	h.firebaseSpec(wt, func(fb map[string]any) { fb["jvm_heap"] = "640MB" })
	h.session(wt)
	h.mustLanes(wt, "start")
	if heaps := javaHeaps(t, wt); len(heaps) < 2 || slices.ContainsFunc(heaps, func(s string) bool { return s != "-Xmx640m" }) {
		t.Fatalf("JVM heaps = %v, want the project's -Xmx640m", heaps)
	}
	h.mustLanes(wt, "stop")

	override := []string{"LANES_FIREBASE_JVM_HEAP=700MB"}
	if r := h.lanesIn(wt, "", override, "start"); r.code != 0 {
		t.Fatalf("start: %s", r)
	}
	if heaps := javaHeaps(t, wt); len(heaps) < 2 || slices.ContainsFunc(heaps, func(s string) bool { return s != "-Xmx700m" }) {
		t.Fatalf("JVM heaps = %v, want the override's -Xmx700m", heaps)
	}
	probe := `node_modules/.bin/firebase emulators:exec --config "$LANES_FIREBASE_CONFIG" --only firestore 'ps eww -o command= -p $(lsof -ti tcp:$PORT_FIRESTORE -sTCP:LISTEN | head -1) | grep -o "JAVA_TOOL_OPTIONS=[^ ]*"'`
	for env, want := range map[string]string{"": "-Xmx640m", "LANES_FIREBASE_JVM_HEAP=700MB": "-Xmx700m"} {
		r := h.lanesIn(wt, "", []string{env}, "test-env", "--", "sh", "-c", probe)
		if r.code != 0 || !strings.Contains(r.stdout, "JAVA_TOOL_OPTIONS="+want) {
			t.Errorf("test-env JVM with %q: want %s, got %s", env, want, r)
		}
	}
}

func TestFirebaseSlowFunctionLoad(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("slow", "feat/slow")
	// The functions discovery gets only the functions' .env files, never the
	// piece's own environment.
	if err := os.WriteFile(filepath.Join(wt, "functions", ".env.local"), []byte("DEMO_SLOW_LOAD=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.session(wt)

	r := h.lanesIn(wt, "", []string{"LANES_FIREBASE_FUNCTIONS_TIMEOUT=10"}, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "Failed to load function definition") {
		t.Fatalf("a 35 s load under a 10 s timeout: %s", r)
	}
	began := time.Now()
	h.mustLanes(wt, "start")
	if took := time.Since(began); took < 35*time.Second {
		t.Fatalf("the slow function loaded in %s, under its 35 s", took)
	}
}

func TestFirebaseOrphanWorkers(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("orphans", "feat/orphans")
	h.session(wt)
	h.mustLanes(wt, "start")
	spawnWorker := func() {
		t.Helper()
		h.publish(wt, "spawn")
		h.eventually("a functions worker", time.Minute, func() bool { return len(procsUnder(wt, isWorker)) > 0 })
	}

	spawnWorker()
	if err := syscall.Kill(h.piecePID(wt, "firebase"), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	left := pidsOf(h.firebaseTree(wt))
	if len(procsUnder(wt, isWorker)) == 0 {
		t.Fatal("kill -9 of the piece took its workers down: nothing to sweep")
	}
	h.mustLanes(wt, "restart", "firebase")
	if alive := stillAlive(left); len(alive) > 0 {
		t.Fatalf("lanes restart firebase left the killed piece's processes: %v", alive)
	}

	// A crashed stack whose only survivors are the firebase piece's: the core
	// refuses a start beside any other piece's orphan, which holds its port.
	spawnWorker()
	tree := h.firebaseTree(wt)
	left = pidsOf(tree)
	for _, p := range procsUnder(wt, func(p procinfo.Proc) bool {
		return !p.NamedAs([]string{"claude"}) && !slices.ContainsFunc(tree, func(f procinfo.Proc) bool { return f.PID == p.PID })
	}) {
		_ = syscall.Kill(p.PID, syscall.SIGKILL)
	}
	for _, p := range pcOf(h.tmpdir(wt)) {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	h.eventually("process-compose to die", 10*time.Second, func() bool { return len(pcOf(h.tmpdir(wt))) == 0 })
	h.mustLanes(wt, "start")
	if alive := stillAlive(left); len(alive) > 0 {
		t.Fatalf("lanes start left the crashed stack's processes: %v", alive)
	}

	spawnWorker()
	h.mustLanes(wt, "stop")
	if workers := procsUnder(wt, isWorker); len(workers) > 0 {
		t.Fatalf("functions workers outlived lanes stop: %v", workers)
	}
}

func TestFirebaseTwoLanes(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	a, b := h.worktree("lane-a", "feat/lane-a"), h.worktree("lane-b", "feat/lane-b")
	for _, wt := range []string{a, b} {
		h.session(wt)
		h.mustLanes(wt, "start")
	}
	h.writeDoc(a, "notes/a")
	h.writeDoc(b, "notes/b")
	h.mustLanes(a, "stop")
	if !h.hasDoc(b, "notes/b") {
		t.Fatal("stopping one lane broke the other's stack")
	}
	h.mustLanes(a, "start")
	if !h.hasDoc(a, "notes/a") || h.hasDoc(a, "notes/b") {
		t.Fatal("a lane's export holds another lane's data")
	}
	for _, wt := range []string{a, b} {
		if strings.Contains(h.pieceLog(wt, "firebase"), "An unexpected error has occurred") {
			t.Errorf("%s: firebase logged an unexpected error", wt)
		}
		h.mustLanes(wt, "stop")
	}

	var wg sync.WaitGroup
	results := make([]result, 2)
	for i, wt := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.lanes(wt, "test-env", "--", "npm", "run", "test:emu")
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.code != 0 || !strings.Contains(r.stdout, suiteSummary) || !strings.Contains(r.stdout, "# fail 0") {
			t.Errorf("parallel test-env: %s\n--- firebase-debug.log ---\n%s", r, tail(read(filepath.Join([]string{a, b}[i], "firebase-debug.log")), 60))
		}
	}
}

func TestFirebaseTestEnvSeries(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("series", "feat/series")
	h.firebaseSpec(wt, func(fb map[string]any) { fb["test"] = map[string]any{"only": []any{"firestore"}} })
	for _, n := range []string{"test_auth", "test_functions", "test_pubsub", "test_eventarc", "test_tasks"} {
		h.dropPort(wt, n)
	}

	script := `node_modules/.bin/firebase emulators:exec --config "$LANES_FIREBASE_CONFIG" 'nc -z 127.0.0.1 $PORT_FIRESTORE && nc -z 127.0.0.1 $PORT_HUB && echo "series up on $PORT_FIRESTORE"'`
	r := h.lanes(wt, "test-env", "--", "sh", "-c", script)
	if r.code != 0 || !strings.Contains(r.stdout, "series up on "+strconv.Itoa(h.port(wt, "test_firestore"))) {
		t.Fatalf("test.only firestore: %s", r)
	}
	if !strings.Contains(r.stdout, "Starting emulators: firestore\n") || strings.Contains(r.stdout, "View Emulator UI") {
		t.Fatalf("the test series started more than Firestore and its implicit emulators:\n%s", r.stdout)
	}
	var cfg struct {
		Emulators map[string]any `json:"emulators"`
	}
	if err := json.Unmarshal([]byte(read(filepath.Join(wt, "firebase.lanes.test.json"))), &cfg); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range cfg.Emulators {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if strings.Join(keys, ",") != "firestore,hub,logging,singleProjectMode,ui" || cfg.Emulators["ui"].(map[string]any)["enabled"] != false {
		t.Fatalf("test config emulators = %v", cfg.Emulators)
	}

	first := h.spawn(wt, "lanes", "test-env", "--", "sleep", "30")
	h.eventually("the first test-env to hold the lock", 10*time.Second, func() bool {
		return strings.TrimSpace(read(filepath.Join(h.tmpdir(wt), "test", "lock"))) == strconv.Itoa(first.Process.Pid)
	})
	r = h.lanes(wt, "test-env", "--", "true")
	if r.code == 0 || !strings.Contains(r.stderr, "already running in this lane (PID "+strconv.Itoa(first.Process.Pid)+")") {
		t.Fatalf("a second test-env: %s", r)
	}
}

func TestFirebaseCommandAndLANHost(t *testing.T) {
	ip := defaultRouteIP(t)
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("cmd", "feat/cmd")
	mark := filepath.Join(t.TempDir(), "ran")
	stub := filepath.Join(t.TempDir(), "firebase-stub")
	body := "#!/bin/sh\ntouch " + mark + "\nexec node " + filepath.Join(wt, "node_modules", "firebase-tools", "lib", "bin", "firebase.js") + ` "$@"` + "\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	h.firebaseSpec(wt, func(fb map[string]any) { fb["command"] = stub })
	h.session(wt)
	h.mustLanes(wt, "start")
	if !exists(mark) {
		t.Fatal("the project's command did not launch the emulators")
	}

	before := map[string]int{}
	for _, p := range h.laneOf(wt).Pieces {
		before[p.Name] = p.PID
	}
	h.mustLanes(wt, "host", "lan")
	if got := envValue(h.mustLanes(wt, "env"), "LANES_HOST"); got != ip || !strings.Contains(read(filepath.Join(wt, "app.env")), "LANES_HOST="+ip+"\n") {
		t.Fatalf("LANES_HOST = %q, want %s", got, ip)
	}
	for _, p := range h.laneOf(wt).Pieces {
		if p.PID != before[p.Name] {
			t.Errorf("host lan restarted %s", p.Name)
		}
	}
	if code := httpStatus("GET", "http://"+ip+":"+strconv.Itoa(h.port(wt, "firestore"))+"/", ""); code != 200 {
		t.Fatalf("Firestore by the LAN IP = %d", code)
	}
}

func TestFirebaseLeavesGitClean(t *testing.T) {
	h := newHarness(t, withProfile(firebaseFull))
	h.startDaemon()
	wt := h.worktree("clean", "feat/clean")
	h.session(wt)
	h.mustLanes(wt, "start")
	h.mustLanes(wt, "stop")
	if r := h.lanes(wt, "test-env", "--", "npm", "run", "test:emu"); r.code != 0 {
		t.Fatalf("test-env: %s", r)
	}
	h.lanesIn(wt, "", nil, "host", "lan")
	h.mustLanes(wt, "host", "local")
	h.git(wt, "switch", "-q", "-c", "feat/clean-next", "origin/main")
	h.mustLanes(wt, "env")
	if status := h.git(wt, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("git status after a full round:\n%s", status)
	}
}

func birth(t *testing.T, path string) time.Time {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
}

func pidsOf(procs []procinfo.Proc) []int {
	out := make([]int, len(procs))
	for i, p := range procs {
		out[i] = p.PID
	}
	return out
}

func stillAlive(pids []int) []int {
	var out []int
	for _, p := range pids {
		if syscall.Kill(p, 0) == nil {
			out = append(out, p)
		}
	}
	return out
}

func pcOf(tmp string) []int {
	procs, _ := procinfo.System().Snapshot()
	var out []int
	for _, p := range procs {
		if p.NamedAs([]string{"process-compose"}) && slices.Contains(p.Argv, paths.PCSocket(tmp)) {
			out = append(out, p.PID)
		}
	}
	return out
}

func httpBody(method, url string) string {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return ""
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
