package adopt

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

type pkg struct {
	dir     string
	name    string
	scripts [][2]string
	deps    []string
}

func (k pkg) dependsOn(name string) bool { return slices.Contains(k.deps, name) }

func (p *project) scanPackages() {
	for _, f := range p.files {
		if path.Base(f) != "package.json" {
			continue
		}
		data, err := p.read(f)
		if err != nil {
			continue
		}
		v, err := decodeJSON(data)
		o, ok := v.(*orderedObject)
		if err != nil || !ok {
			p.report.check("%s: not a valid package.json — skipped", f)
			continue
		}
		k := pkg{dir: path.Dir(f)}
		if n, ok := o.get("name"); ok {
			k.name, _ = n.(string)
		}
		if s, ok := o.get("scripts"); ok {
			if so, ok := s.(*orderedObject); ok {
				for _, name := range so.keys {
					if text, ok := so.vals[name].(string); ok {
						k.scripts = append(k.scripts, [2]string{name, text})
					}
				}
			}
		}
		for _, key := range []string{"dependencies", "devDependencies"} {
			if d, ok := o.get(key); ok {
				if do, ok := d.(*orderedObject); ok {
					k.deps = append(k.deps, do.keys...)
				}
			}
		}
		p.packages = append(p.packages, k)
	}
}

type devServer struct {
	pkgDir, script, name, port string
	command                    string
	fillNote                   string
	portFlagRewritten          bool
}

func (d devServer) portVar() string { return "PORT_" + strings.ToUpper(d.port) }

func (d devServer) source() string { return path.Join(d.pkgDir, "package.json") + " " + d.script }

var (
	commandSep  = regexp.MustCompile(`&&|\|\||[;|&]`)
	assignment  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	portFlag    = regexp.MustCompile(`(^|\s)(?:-p|--port)(?:=|\s+)[^\s;&|]+`)
	safeWord    = regexp.MustCompile(`^[A-Za-z0-9:._/@+-]+$`)
	nonNameChar = regexp.MustCompile(`[^a-z0-9_.-]+`)
	nonPortChar = regexp.MustCompile(`[^a-z0-9_]+`)
)

type scriptShape struct {
	tool      string
	composite bool
	last      bool
	flag      string
}

func devCommand(text string) (scriptShape, bool) {
	cmds := commandSep.Split(text, -1)
	for i, c := range cmds {
		words := strings.Fields(c)
		for j, w := range words {
			if assignment.MatchString(w) {
				continue
			}
			next := ""
			if j+1 < len(words) {
				next = words[j+1]
			}
			tool := ""
			switch {
			case isTool(w, "vite") && !slices.Contains([]string{"build", "preview", "optimize"}, next):
				tool = "vite"
			case isTool(w, "storybook") && next == "dev":
				tool = "storybook"
			case isTool(w, "start-storybook"):
				tool = "start-storybook"
			}
			if tool == "" {
				continue
			}
			shape := scriptShape{tool: tool, composite: len(cmds) > 1, last: i == len(cmds)-1}
			shape.flag = strings.TrimSpace(portFlag.FindString(c[strings.Index(c, w):]))
			return shape, true
		}
	}
	return scriptShape{}, false
}

func isTool(word, tool string) bool { return word == tool || strings.HasSuffix(word, "/"+tool) }

func (p *project) detectDevServers() {
	type found struct {
		k     pkg
		name  string
		text  string
		shape scriptShape
	}
	var all []found
	for _, k := range p.packages {
		for _, s := range k.scripts {
			if shape, ok := devCommand(s[1]); ok {
				all = append(all, found{k, s[0], s[1], shape})
			}
		}
	}
	labels := map[string]string{}
	count := map[string]map[string]bool{}
	for _, f := range all {
		l := packageLabel(f.k)
		labels[f.k.dir] = l
		if count[l] == nil {
			count[l] = map[string]bool{}
		}
		count[l][f.k.dir] = true
	}
	for dir, l := range labels {
		if len(count[l]) > 1 && dir != "." {
			labels[dir] = strings.ReplaceAll(dir, "/", "-")
		}
	}
	usedNames, usedPorts := map[string]bool{}, map[string]bool{}
	for _, f := range all {
		d := devServer{pkgDir: f.k.dir, script: f.name}
		d.name = unique(strings.Trim(nonNameChar.ReplaceAllString(strings.ToLower(labels[f.k.dir]+"-"+f.name), "-"), "-"), "-", usedNames)
		d.port = unique(portName(d.name), "_", usedPorts)
		d.command, d.fillNote, d.portFlagRewritten = p.devServerCommand(f.k.dir, f.name, f.text, f.shape, d.portVar())
		if d.portFlagRewritten {
			p.report.check("process-compose.yaml %s: rewritten from %s (%s) to take its port from %s — check the command", d.name, d.source(), f.shape.flag, d.portVar())
		}
		if d.fillNote != "" {
			p.report.fill(Item{File: "process-compose.yaml", Key: "processes." + d.name + ".command", What: d.fillNote})
		}
		p.devs = append(p.devs, d)
	}
}

func packageLabel(k pkg) string {
	if k.dir != "." {
		return path.Base(k.dir)
	}
	name := k.name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "app"
	}
	return name
}

func portName(processName string) string {
	n := nonPortChar.ReplaceAllString(strings.ReplaceAll(processName, "-", "_"), "")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "dev_" + n
	}
	return n
}

func unique(name, sep string, used map[string]bool) string {
	n := name
	for i := 2; used[n]; i++ {
		n = fmt.Sprintf("%s%s%d", name, sep, i)
	}
	used[n] = true
	return n
}

func (p *project) devServerCommand(dir, script, text string, shape scriptShape, portVar string) (command, fill string, rewritten bool) {
	pm := p.packageManager(dir)
	run := pm + " run " + shellWord(script)
	port := `--port "$` + portVar + `"`
	switch {
	case shape.flag != "" && !shape.composite:
		lead, rest := splitAssignments(strings.TrimSpace(text))
		replaced := strings.Replace(rest, shape.flag, port, 1)
		return lead + pm + " exec -- " + replaced, "", true
	case shape.flag != "":
		return run, fmt.Sprintf("%s sets its own port (%s) inside a composite script — make it take --port \"$%s\"", script, shape.flag, portVar), false
	case shape.composite && !shape.last:
		return run, fmt.Sprintf("%s runs %s before other commands — pass it --port \"$%s\"", script, shape.tool, portVar), false
	}
	if pm == "npm" {
		return run + " -- " + port, "", false
	}
	return run + " " + port, "", false
}

func splitAssignments(text string) (lead, rest string) {
	words := strings.Fields(text)
	i := 0
	for i < len(words) && assignment.MatchString(words[i]) {
		i++
	}
	if i == 0 {
		return "", text
	}
	rest = strings.TrimSpace(text[strings.Index(text, words[i-1])+len(words[i-1]):])
	return strings.Join(words[:i], " ") + " ", rest
}

func (p *project) packageManager(dir string) string {
	for d := dir; ; d = path.Dir(d) {
		for _, lm := range []struct{ file, pm string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"package-lock.json", "npm"}} {
			if slices.Contains(p.files, path.Join(d, lm.file)) {
				return lm.pm
			}
		}
		if d == "." || d == "/" {
			return "npm"
		}
	}
}

func shellWord(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
