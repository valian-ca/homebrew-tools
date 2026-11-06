package adopt

import (
	"path"
	"strings"
)

func slug(s string) string {
	return strings.Trim(nonNameChar.ReplaceAllString(strings.ToLower(strings.ReplaceAll(s, "/", "-")), "-"), "-")
}

// cdPath keeps cd from reading a directory named -x as an option.
func cdPath(dir string) string {
	if strings.HasPrefix(dir, "-") {
		return "./" + dir
	}
	return dir
}

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
			s.name = in.tool + "-" + slug(dir)
			s.run = "cd " + shellWord(cdPath(dir)) + " && " + in.run
		}
		s.name = unique(s.name, "-", used)
		p.steps = append(p.steps, s)
	}
}
