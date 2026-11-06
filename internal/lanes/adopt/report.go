package adopt

import (
	"fmt"
	"io"
	"strings"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
)

type Item struct {
	File, Key, What string
}

func (i Item) String() string { return i.File + " " + i.Key + " — " + i.What }

type Entry struct {
	File    string
	Details []string
}

type Report struct {
	Written         []Entry
	Merged          []Entry
	Unchanged       []Entry
	Fill            []Item
	Check           []string
	NotWritten      []string
	Harness         []string
	NothingDetected bool
	Direnv          bool

	wired       []string
	added       []string
	envrcWired  bool
	ignoreAdded []string
}

func (r *Report) fill(i Item) { r.Fill = append(r.Fill, i) }

func (r *Report) check(format string, a ...any) { r.Check = append(r.Check, fmt.Sprintf(format, a...)) }

func (p *project) describeFiles(files []File) {
	r := p.report
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	for _, f := range files {
		e := Entry{File: f.Path, Details: p.details(f.Path)}
		if f.Mode == Write {
			r.Written = append(r.Written, e)
		} else {
			r.Merged = append(r.Merged, e)
		}
	}
	if _, ok := byPath[SettingsFile]; !ok {
		r.Unchanged = append(r.Unchanged, Entry{File: SettingsFile, Details: []string{"already holds the four lanes hooks"}})
	}
	if _, ok := byPath[EnvrcFile]; !ok && r.envrcWired {
		r.Unchanged = append(r.Unchanged, Entry{File: EnvrcFile, Details: []string{"already runs lanes env"}})
	}
	if _, ok := byPath[GitignoreFile]; !ok && p.firebase != nil {
		r.Unchanged = append(r.Unchanged, Entry{File: GitignoreFile, Details: []string{"already ignores " + strings.Join(p.gitignoreEntries(), ", ")}})
	}
}

func (p *project) details(path string) []string {
	r := p.report
	switch path {
	case config.FileName:
		d := []string{fmt.Sprintf("schema 2, %d named ports in a block of %d", len(p.ports), blockSize(len(p.ports)))}
		if fb := p.firebase; fb != nil {
			d = append(d, fmt.Sprintf("firebase resource on %s (emulators: %s): ports %s for the stack and %s for the test series",
				fb.config, strings.Join(fb.emulators, ", "), strings.Join(fb.stack, ", "), strings.Join(fb.test, ", ")))
		}
		if m := p.mysql; m != nil {
			d = append(d, "mysql resource from "+m.service.String())
		}
		for _, s := range p.steps {
			d = append(d, fmt.Sprintf("setup step %s: %s, when %s changes", s.name, s.run, s.lockfile))
		}
		return d
	case pc.ProjectFile:
		if len(p.devs) == 0 {
			return []string{"no dev server detected"}
		}
		var d []string
		for _, s := range p.devs {
			d = append(d, fmt.Sprintf("dev server %s (%s), disabled, on port %s: lanes up %s", s.name, s.source(), s.port, s.name))
		}
		return d
	case SettingsFile:
		events := r.added
		if len(events) == 0 {
			events = []string{"SessionStart", "CwdChanged", "PostToolUse", "SessionEnd"}
		}
		d := []string{"lanes hooks added: " + strings.Join(events, ", ")}
		if len(p.steps) > 0 {
			d = append(d, fmt.Sprintf("timeout %d on the SessionStart, CwdChanged and PostToolUse lanes hooks: lanes.yaml has setup steps", PrepTimeout))
		}
		return d
	case EnvrcFile:
		return []string{"the lanes line: " + EnvrcLine}
	case GitignoreFile:
		return []string{"ignores " + strings.Join(r.ignoreAdded, ", ") + ": the firebase resource's generated configs and data"}
	}
	return nil
}

func (r *Result) WriteDryRun(w io.Writer) {
	for _, f := range r.Files {
		fmt.Fprintf(w, "== %s (%s) ==\n", f.Path, f.Mode)
		shown := f.Content
		if f.Added != nil {
			shown = f.Added
		}
		_, _ = w.Write(shown)
		fmt.Fprintln(w)
	}
}

func (r *Result) WriteReport(w io.Writer, dryRun bool) {
	rep := r.Report
	if dryRun {
		fmt.Fprintf(w, "lanes init --dry-run in %s — nothing written\n", r.Root)
	} else {
		fmt.Fprintf(w, "lanes init in %s\n", r.Root)
	}
	if rep.NothingDetected {
		fmt.Fprintln(w, "\nNothing detected: no Firebase config with emulators, no compose database, no dev server, no lockfile.")
	}
	entries(w, "Written", rep.Written)
	entries(w, "Merged", rep.Merged)
	entries(w, "Unchanged", rep.Unchanged)
	if len(rep.Fill) > 0 {
		fmt.Fprintln(w, "\nTo fill in — lanes init does not infer these:")
		for _, i := range rep.Fill {
			fmt.Fprintf(w, "  - %s\n", i)
		}
	}
	lines(w, "To check", rep.Check)
	lines(w, "Not written", rep.NotWritten)
	lines(w, "Former worktree harness — not removed, removing it is the migration's work", rep.Harness)
	fmt.Fprintln(w, "\nNext steps:")
	n := 1
	step := func(s string) { fmt.Fprintf(w, "  %d. %s\n", n, s); n++ }
	if len(rep.Fill) > 0 {
		step("Fill in each item above, at its \"" + strings.TrimSpace(fillMark) + "\" comment.")
	}
	if rep.Direnv {
		step("direnv allow")
	}
	var paths []string
	for _, f := range r.Files {
		paths = append(paths, f.Path)
	}
	step("Commit " + strings.Join(paths, ", ") + ".")
	step("In a worktree: lanes start. See lanes help contract.")
}

func entries(w io.Writer, title string, es []Entry) {
	if len(es) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s:\n", title)
	for _, e := range es {
		fmt.Fprintf(w, "  %s\n", e.File)
		for _, d := range e.Details {
			fmt.Fprintf(w, "    - %s\n", d)
		}
	}
}

func lines(w io.Writer, title string, ls []string) {
	if len(ls) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s:\n", title)
	for _, l := range ls {
		fmt.Fprintf(w, "  - %s\n", l)
	}
}
