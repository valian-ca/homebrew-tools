//go:build integration && darwin

package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
)

// firebaseSlots bounds the Firebase stacks running at once: each holds two
// JVMs and a Node process, about a gigabyte and a half.
var firebaseSlots = make(chan struct{}, firebaseParallel())

func firebaseParallel() int {
	if n, err := strconv.Atoi(os.Getenv("LANES_FIREBASE_PARALLEL")); err == nil && n > 0 {
		return n
	}
	return 2
}

func requireFirebase(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"java", "node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("LANES_REQUIRE_FIREBASE") == "1" {
				t.Fatalf("%s is required for the Firebase cases (LANES_REQUIRE_FIREBASE=1)", tool)
			}
			t.Skipf("%s not found: the Firebase cases need Java and Node", tool)
		}
	}
}

// shape trims the demo to the harness's profile and commits it as main.
func (h *harness) shape(dir string) {
	h.t.Helper()
	noFirebase := h.profile == core || h.profile == mysqlStack
	h.editYAML(filepath.Join(dir, "lanes.yaml"), func(doc map[string]any) {
		if h.profile != firebaseFull {
			doc["setup"] = slices.DeleteFunc(doc["setup"].([]any), func(s any) bool { return s.(map[string]any)["name"] == "node" })
		}
		if noFirebase {
			doc["resources"] = slices.DeleteFunc(doc["resources"].([]any), func(r any) bool { return r.(map[string]any)["firebase"] != nil })
		}
		if h.profile == mysqlStack {
			h.pointMySQL(doc)
		} else {
			trimMySQL(doc)
		}
	})
	h.editYAML(filepath.Join(dir, "process-compose.yaml"), func(doc map[string]any) {
		procs := doc["processes"].(map[string]any)
		if noFirebase {
			delete(procs, "firebase")
			delete(procs, "functions-build")
		}
		if h.profile != mysqlStack {
			delete(procs, "mysql-seed")
		}
	})
	h.commitAll(dir, "shape the demo for the test")
	h.git(dir, "push", "-q", "origin", "main")
}

func (h *harness) editYAML(path string, edit func(map[string]any)) {
	h.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		h.t.Fatal(err)
	}
	edit(doc)
	out, err := yaml.Marshal(doc)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) firebaseSpec(wt string, edit func(map[string]any)) {
	h.t.Helper()
	h.editYAML(filepath.Join(wt, "lanes.yaml"), func(doc map[string]any) {
		for _, r := range doc["resources"].([]any) {
			if fb, ok := r.(map[string]any)["firebase"].(map[string]any); ok {
				edit(fb)
			}
		}
	})
}

func (h *harness) commitAll(dir, msg string) {
	h.t.Helper()
	h.git(dir, "add", "-A")
	h.git(dir, "-c", "user.name=lanes-test", "-c", "user.email=lanes-test@example.com", "commit", "-q", "-m", msg)
}

// killUnder ends whatever a failed test left running from a worktree: an
// emulator outlives its test otherwise, and holds its ports and memory.
func killUnder(dir string) {
	procs, err := procinfo.System().Snapshot()
	if err != nil {
		return
	}
	for _, p := range procs {
		if p.PID != os.Getpid() && p.Under(dir) {
			_ = syscall.Kill(p.PID, syscall.SIGKILL)
		}
	}
}

func firestoreURL(port int, doc string) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/v1/projects/demo-lanes/databases/(default)/documents/" + doc
}

func httpStatus(method, url, body string) int {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("content-type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (h *harness) writeDoc(wt, doc string) {
	h.t.Helper()
	if code := httpStatus("PATCH", firestoreURL(h.port(wt, "firestore"), doc), `{"fields":{"by":{"stringValue":"test"}}}`); code != 200 {
		h.t.Fatalf("PATCH %s = %d", doc, code)
	}
}

func (h *harness) hasDoc(wt, doc string) bool {
	return httpStatus("GET", firestoreURL(h.port(wt, "firestore"), doc), "") == 200
}

func (h *harness) publish(wt, marker string) {
	h.t.Helper()
	body := `{"messages":[{"data":"` + base64.StdEncoding.EncodeToString([]byte(`{"marker":"`+marker+`"}`)) + `"}]}`
	url := "http://127.0.0.1:" + strconv.Itoa(h.port(wt, "pubsub")) + "/v1/projects/demo-lanes/topics/demo-topic:publish"
	if code := httpStatus("POST", url, body); code != 200 {
		h.t.Fatalf("publish on demo-topic = %d", code)
	}
}

func treeHash(t *testing.T, dir string) string {
	t.Helper()
	sum := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		sum.Write([]byte(rel + "\x00"))
		if d.Type().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func procsUnder(dir string, match func(procinfo.Proc) bool) []procinfo.Proc {
	procs, err := procinfo.System().Snapshot()
	if err != nil {
		return nil
	}
	var out []procinfo.Proc
	for _, p := range procs {
		if p.Under(dir) && match(p) {
			out = append(out, p)
		}
	}
	return out
}

func isJava(p procinfo.Proc) bool { return p.NamedAs([]string{"java"}) }

func isWorker(p procinfo.Proc) bool {
	return slices.ContainsFunc(p.Argv, func(a string) bool { return strings.Contains(a, "functionsEmulatorRuntime") })
}

func isFirestoreJVM(p procinfo.Proc) bool {
	return isJava(p) && slices.ContainsFunc(p.Argv, func(a string) bool { return strings.Contains(a, "cloud-firestore-emulator") })
}

var toolOptions = regexp.MustCompile(`JAVA_TOOL_OPTIONS=(\S+)`)

func javaHeaps(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, p := range procsUnder(dir, isJava) {
		raw, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(p.PID)).Output()
		if err != nil {
			continue
		}
		m := toolOptions.FindSubmatch(raw)
		if m == nil {
			out = append(out, "none")
			continue
		}
		out = append(out, string(bytes.TrimSpace(m[1])))
	}
	return out
}

func (h *harness) pieceLog(wt, piece string) string {
	return read(filepath.Join(h.tmpdir(wt), "logs", piece+".log"))
}
