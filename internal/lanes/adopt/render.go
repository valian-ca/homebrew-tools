package adopt

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const fillMark = "# lanes init: "

// devServerReadySeconds outlasts a dev server's first start: process-compose
// stops a process whose probe fails failure_threshold times in a row, 3 by
// default, and Vite's first dependency scan takes longer than 3 s.
const devServerReadySeconds = 120

func (p *project) firebaseIndex() int { return 0 }

func (p *project) mysqlIndex() int {
	if p.firebase != nil {
		return 1
	}
	return 0
}

func (p *project) renderLanesYAML() []byte {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("schema: 2")
	w("ports:")
	w("  block: %d", blockSize(len(p.ports)))
	b.WriteString(flowList("  names: ", p.ports))

	scripts := "a scripts resource for each lane-scoped resource the project makes itself (a bucket, a queue, a directory outside the worktree), with its create, erase, list and owner commands"
	noMySQL := "a mysql resource, if the project's MySQL server runs without docker-compose (Homebrew, a script): its port, start and listeners"
	p.report.fill(Item{File: "lanes.yaml", Key: "resources", What: scripts})
	if p.firebase == nil && p.mysql == nil {
		w("%s%s", fillMark, scripts)
		w("%s%s", fillMark, noMySQL)
		p.report.fill(Item{File: "lanes.yaml", Key: "resources", What: noMySQL})
	} else {
		w("resources:")
		w("  %s%s", fillMark, scripts)
	}
	if fb := p.firebase; fb != nil {
		key := fmt.Sprintf("resources[%d].firebase.seed", p.firebaseIndex())
		seed := "seed — the directory of an emulator export a lane imports at its first start, if the project keeps one"
		w("  - firebase:")
		w("      config: %s", scalar(fb.config))
		w("      %s%s", fillMark, seed)
		p.report.fill(Item{File: "lanes.yaml", Key: key, What: "the directory of an emulator export a lane imports at its first start, if the project keeps one"})
	}
	if m := p.mysql; m != nil {
		i := p.mysqlIndex()
		w("  - mysql:")
		if m.portIssue != "" {
			w("      %sport — %s", fillMark, oneLine(m.portIssue))
			p.report.fill(Item{File: "lanes.yaml", Key: fmt.Sprintf("resources[%d].mysql.port", i), What: "the host port of " + m.service.String() + ": " + m.portIssue})
		} else {
			w("      port: %d", m.port)
		}
		w("      user: root")
		if m.passwordIssue != "" {
			w("      %spassword — %s", fillMark, oneLine(m.passwordIssue))
			p.report.fill(Item{File: "lanes.yaml", Key: fmt.Sprintf("resources[%d].mysql.password", i), What: "the root password: " + m.passwordIssue})
		} else {
			w("      password: %s", scalar(m.password))
		}
		w("      start: %s", scalar("docker compose -f "+shellWord(m.service.file)+" up -d --wait "+shellWord(m.service.name)))
		w("      listeners: [com.docker.backend]")
		what := "one entry per database a lane gets: a key and a name template with {lane} after a fixed prefix (dev: app_{lane}); lanes start refuses until it is filled"
		w("      %sdatabases — %s", fillMark, what)
		w("      databases: {}")
		p.report.fill(Item{File: "lanes.yaml", Key: fmt.Sprintf("resources[%d].mysql.databases", i), What: what})
	} else if p.firebase != nil {
		w("  %s%s", fillMark, noMySQL)
		p.report.fill(Item{File: "lanes.yaml", Key: "resources", What: noMySQL})
	}
	generate := "a template (<file>.tmpl) for each project file that carries ports today (an .env, a config file), rendered with the lane's variables"
	w("%sgenerate — %s", fillMark, generate)
	w("# generate:")
	w("#   - template: config/app.env.tmpl")
	p.report.fill(Item{File: "lanes.yaml", Key: "generate", What: generate})
	if len(p.steps) > 0 {
		w("setup:")
		for _, s := range p.steps {
			w("  - name: %s", scalar(s.name))
			w("    run: %s", scalar(s.run))
			w("    when_changed: [%s]", scalar(s.lockfile))
		}
	}
	return []byte(b.String())
}

func (p *project) renderProcessCompose() []byte {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	own := "the project's own processes besides its dev servers (a backend, a worker, a build the stack needs)"
	w(`version: "0.5"`)
	w("%s%s", fillMark, own)
	p.report.fill(Item{File: "process-compose.yaml", Key: "processes", What: own})
	if p.mysql != nil {
		seed := `a seed task that fills the lane's MySQL databases and keeps a marker table (see "The seed task" in lanes help contract)`
		w("%s%s", fillMark, seed)
		p.report.fill(Item{File: "process-compose.yaml", Key: "processes", What: seed})
	}
	if len(p.devs) == 0 {
		w("processes: {}")
		return []byte(b.String())
	}
	w("processes:")
	for _, d := range p.devs {
		w("  %s:", d.name)
		if d.fillNote != "" {
			w("    %s%s", fillMark, oneLine(d.fillNote))
		}
		w("    command: %s", scalar(d.command))
		if d.pkgDir != "." {
			w("    working_dir: %s", scalar(d.pkgDir))
		}
		w("    disabled: true")
		w("    readiness_probe:")
		w("      exec:")
		w("        command: %s", scalar(`nc -z localhost "$`+d.portVar()+`"`))
		w("      period_seconds: 1")
		w("      failure_threshold: %d", devServerReadySeconds)
	}
	return []byte(b.String())
}

// oneLine keeps project text that lands in a YAML comment on that comment's
// line: a newline in a service or script name would start a YAML key.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// scalar always fits on its key's line: yaml.Marshal turns a multi-line value
// into a block scalar, whose indentation would not follow the key's.
func scalar(s string) string {
	out, err := yaml.Marshal(s)
	if err != nil || strings.ContainsFunc(s, unicode.IsControl) {
		return strconv.Quote(s)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func flowList(prefix string, items []string) string {
	if len(items) == 0 {
		return prefix + "[]\n"
	}
	indent := strings.Repeat(" ", len(prefix)+1)
	var b strings.Builder
	line := prefix + "["
	for i, it := range items {
		piece := it
		if i < len(items)-1 {
			piece += ","
		} else {
			piece += "]"
		}
		if i > 0 && len(line)+1+len(piece) > 80 {
			b.WriteString(line + "\n")
			line = indent + piece
			continue
		}
		if i > 0 {
			line += " "
		}
		line += piece
	}
	b.WriteString(line + "\n")
	return b.String()
}
