package adopt

import (
	"path"
	"strings"
)

type setupStep struct {
	name, run, lockfile string
}

var installs = map[string]struct{ tool, run string }{
	"pnpm-lock.yaml":    {"pnpm", "pnpm install --frozen-lockfile"},
	"package-lock.json": {"npm", "npm ci"},
	"yarn.lock":         {"yarn", "yarn install --frozen-lockfile"},
	"pubspec.lock":      {"flutter", "flutter pub get"},
}

func (p *project) detectSetup() {
	used := map[string]bool{}
	for _, f := range p.files {
		in, ok := installs[path.Base(f)]
		if !ok {
			continue
		}
		dir := path.Dir(f)
		s := setupStep{name: in.tool, run: in.run, lockfile: f}
		if dir != "." {
			s.name = in.tool + "-" + strings.Trim(nonNameChar.ReplaceAllString(strings.ToLower(strings.ReplaceAll(dir, "/", "-")), "-"), "-")
			s.run = "cd " + shellWord(dir) + " && " + in.run
		}
		s.name = unique(s.name, "-", used)
		p.steps = append(p.steps, s)
	}
}
