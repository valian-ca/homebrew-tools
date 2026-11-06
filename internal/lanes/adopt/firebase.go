package adopt

import (
	"path"
	"slices"
	"strings"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
)

type firebaseFound struct {
	config    string
	emulators []string
	stack     []string
	test      []string
}

var firebaseConfigNames = []string{"firebase.emulator.json", "firebase.json"}

func (p *project) detectFirebase() {
	var candidates []string
	for _, f := range p.files {
		if !slices.Contains(firebaseConfigNames, path.Base(f)) {
			continue
		}
		data, err := p.read(f)
		if err != nil {
			continue
		}
		if _, err := firebase.ParseBase(data); err != nil {
			p.report.check("%s: not a valid Firebase config (%v) — skipped", f, err)
			continue
		}
		if !hasEmulators(data) {
			continue
		}
		candidates = append(candidates, f)
	}
	if len(candidates) == 0 {
		return
	}
	slices.SortStableFunc(candidates, func(a, b string) int {
		if d := depth(a) - depth(b); d != 0 {
			return d
		}
		if da, db := path.Dir(a), path.Dir(b); da != db {
			return strings.Compare(da, db)
		}
		return slices.Index(firebaseConfigNames, path.Base(a)) - slices.Index(firebaseConfigNames, path.Base(b))
	})
	chosen := candidates[0]
	for _, other := range candidates[1:] {
		p.report.check("%s: another Firebase config with emulators — lanes.yaml uses %s, and a project has at most one firebase resource", other, chosen)
	}
	data, _ := p.read(chosen)
	base, _ := firebase.ParseBase(data)
	fb := &config.Firebase{Config: chosen}
	stack := firebase.NewPlan(base, fb, firebase.Stack)
	p.firebase = &firebaseFound{
		config:    chosen,
		emulators: stack.Only(),
		stack:     stack.RequiredPorts(),
		test:      firebase.NewPlan(base, fb, firebase.Test).RequiredPorts(),
	}
	if !slices.ContainsFunc(p.packages, func(k pkg) bool { return k.dependsOn("firebase-tools") }) {
		p.report.check("firebase-tools is in no package.json: add it to the project's dependencies, or set resources[%d].firebase.command in lanes.yaml", p.firebaseIndex())
	}
}

func hasEmulators(data []byte) bool {
	v, err := decodeJSON(data)
	if err != nil {
		return false
	}
	o, ok := v.(*object)
	if !ok {
		return false
	}
	em, ok := o.get("emulators")
	if !ok {
		return false
	}
	_, ok = em.(*object)
	return ok
}

func depth(f string) int { return strings.Count(f, "/") }
