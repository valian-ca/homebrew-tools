package adopt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
)

const (
	EnvrcLine         = `if has lanes; then eval "$(lanes env)"; fi`
	PrepTimeout       = 600
	SessionEndTimeout = 5
)

type hookSpec struct {
	event, matcher, name string
	prep                 bool
}

var lanesHooks = []hookSpec{
	{event: "SessionStart", name: "session-start", prep: true},
	{event: "CwdChanged", name: "cwd-changed", prep: true},
	{event: "PostToolUse", matcher: "EnterWorktree|ExitWorktree", name: "cwd-changed", prep: true},
	{event: "SessionEnd", name: "session-end"},
}

// harnessEvents are the events a former worktree harness hooked to set up
// and tear down its stack.
var harnessEvents = []string{"SessionStart", "CwdChanged", "SessionEnd"}

func HookCommand(name string) string {
	return "command -v lanes >/dev/null 2>&1 && lanes hook " + name + " || true"
}

func (h hookSpec) group(withSetup bool) *object {
	hook := newObject()
	hook.set("type", "command")
	hook.set("command", HookCommand(h.name))
	switch {
	case !h.prep:
		hook.set("timeout", json.Number(fmt.Sprint(SessionEndTimeout)))
	case withSetup:
		hook.set("timeout", json.Number(fmt.Sprint(PrepTimeout)))
	}
	g := newObject()
	if h.matcher != "" {
		g.set("matcher", h.matcher)
	}
	g.set("hooks", []any{hook})
	return g
}

func (p *project) settings() (*File, error) {
	data, perm, found, err := p.existing(SettingsFile)
	if err != nil {
		return nil, err
	}
	withSetup := len(p.steps) > 0
	if !found || len(bytes.TrimSpace(data)) == 0 {
		var b strings.Builder
		b.WriteString("{\n  \"hooks\": {\n")
		for i, h := range lanesHooks {
			fmt.Fprintf(&b, "    %s: %s", scalarJSON(h.event), encodeCompact([]any{h.group(withSetup)}))
			if i < len(lanesHooks)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString("  }\n}\n")
		mode := Write
		if found {
			mode = Merge
		}
		return &File{Path: SettingsFile, Mode: mode, Content: []byte(b.String()), perm: perm}, nil
	}
	v, err := decodeJSON(data)
	if err != nil {
		return nil, fmt.Errorf("lanes init: %s is not valid JSON (%v) — fix it, then run lanes init again; nothing was written", SettingsFile, err)
	}
	root, ok := v.(*object)
	if !ok {
		return nil, fmt.Errorf("lanes init: %s is not a JSON object — fix it, then run lanes init again; nothing was written", SettingsFile)
	}
	hooks := newObject()
	if hv, ok := root.get("hooks"); ok {
		if hooks, ok = hv.(*object); !ok {
			return nil, fmt.Errorf("lanes init: %s: hooks is not a JSON object — fix it, then run lanes init again; nothing was written", SettingsFile)
		}
	}
	p.scanSettingsHarness(hooks)
	changed := false
	for _, h := range lanesHooks {
		var groups []any
		if gv, ok := hooks.get(h.event); ok {
			if groups, ok = gv.([]any); !ok {
				return nil, fmt.Errorf("lanes init: %s: hooks.%s is not a JSON array — fix it, then run lanes init again; nothing was written", SettingsFile, h.event)
			}
		}
		present, timeout := lanesHookIn(groups, h.name)
		if present {
			p.report.wired = append(p.report.wired, h.event)
			if withSetup && h.prep && !timeout {
				p.report.check("%s hooks.%s: its lanes hook has no timeout, and lanes.yaml has setup steps — a first prep may need \"timeout\": %d", SettingsFile, h.event, PrepTimeout)
			}
			continue
		}
		hooks.set(h.event, append(groups, h.group(withSetup)))
		p.report.added = append(p.report.added, h.event)
		changed = true
	}
	if !changed {
		return nil, nil
	}
	root.set("hooks", hooks)
	return &File{Path: SettingsFile, Mode: Merge, Content: encodePretty(root), perm: perm}, nil
}

func lanesHookIn(groups []any, name string) (present, timeout bool) {
	for _, g := range groups {
		for _, h := range hookObjects(g) {
			if cmd, _ := h.vals["command"].(string); strings.Contains(cmd, "lanes hook "+name) {
				_, t := h.get("timeout")
				return true, t
			}
		}
	}
	return false, false
}

func hookObjects(group any) []*object {
	g, ok := group.(*object)
	if !ok {
		return nil
	}
	list, _ := g.vals["hooks"].([]any)
	var out []*object
	for _, h := range list {
		if o, ok := h.(*object); ok {
			out = append(out, o)
		}
	}
	return out
}

func (p *project) scanSettingsHarness(hooks *object) {
	for _, event := range harnessEvents {
		groups, _ := hooks.vals[event].([]any)
		for i, g := range groups {
			for j, h := range hookObjects(g) {
				cmd, _ := h.vals["command"].(string)
				if !strings.Contains(cmd, "lanes hook") {
					p.report.Harness = append(p.report.Harness, fmt.Sprintf("%s hooks.%s[%d].hooks[%d]: %s", SettingsFile, event, i, j, cmd))
				}
			}
		}
	}
}

var envAssign = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=`)

func harnessVar(name string) bool {
	n := strings.ToUpper(name)
	if slices.Contains(strings.Split(n, "_"), "PORT") {
		return true
	}
	return slices.Contains([]string{"DATABASE_URL", "COMPOSE_PROJECT_NAME", "FIREBASE_EMULATOR_HUB"}, n) || strings.HasSuffix(n, "_EMULATOR_HOST")
}

func (p *project) envrc() (*File, error) {
	data, perm, found, err := p.existing(EnvrcFile)
	if err != nil {
		return nil, err
	}
	if !found {
		p.report.Direnv = true
		line := []byte(EnvrcLine + "\n")
		return &File{Path: EnvrcFile, Mode: Write, Content: line, Added: line, perm: perm}, nil
	}
	present := false
	for i, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "lanes env") {
			present = true
			continue
		}
		if m := envAssign.FindStringSubmatch(line); m != nil && harnessVar(m[1]) {
			p.report.Harness = append(p.report.Harness, fmt.Sprintf("%s:%d: %s", EnvrcFile, i+1, strings.TrimSpace(line)))
		}
	}
	if present {
		p.report.envrcWired = true
		return nil, nil
	}
	p.report.Direnv = true
	return appendLines(EnvrcFile, data, perm, []string{EnvrcLine}), nil
}

func appendLines(rel string, data []byte, perm fs.FileMode, lines []string) *File {
	added := strings.Join(lines, "\n") + "\n"
	prefix := ""
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		prefix = "\n"
	}
	content := append(bytes.Clone(data), []byte(prefix+added)...)
	return &File{Path: rel, Mode: Append, Content: content, Added: []byte(added), perm: perm}
}

func (p *project) gitignoreEntries() []string {
	if p.firebase == nil {
		return nil
	}
	return []string{firebase.StackConfigName, firebase.TestConfigName, ".emu-data", ".emu-data.next", ".emu-data.old"}
}

func (p *project) gitignore() (*File, error) {
	entries := p.gitignoreEntries()
	if len(entries) == 0 {
		return nil, nil
	}
	data, perm, found, err := p.existing(GitignoreFile)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, e := range entries {
		if !ignoresLine(data, e) {
			missing = append(missing, e)
		}
	}
	p.report.ignoreAdded = missing
	if len(missing) == 0 {
		return nil, nil
	}
	lines := append([]string{"# lanes"}, missing...)
	if !found || len(bytes.TrimSpace(data)) == 0 {
		content := []byte(strings.Join(lines, "\n") + "\n")
		return &File{Path: GitignoreFile, Mode: Write, Content: content, Added: content, perm: perm}, nil
	}
	if !bytes.HasSuffix(data, []byte("\n\n")) {
		lines = append([]string{""}, lines...)
	}
	return appendLines(GitignoreFile, data, perm, lines), nil
}

// ignoresLine is a textual check of the root .gitignore, so a teammate's
// global excludes never hide a missing entry.
func ignoresLine(data []byte, entry string) bool {
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(line)
		if strings.Trim(l, "/") == entry {
			return true
		}
	}
	return false
}
