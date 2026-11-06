package adopt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
)

type Mode string

const (
	Write  Mode = "write"
	Merge  Mode = "merge"
	Append Mode = "append"
)

const (
	SettingsFile  = ".claude/settings.json"
	EnvrcFile     = ".envrc"
	GitignoreFile = ".gitignore"
	versionedPerm = fs.FileMode(0o644)
)

type File struct {
	Path    string
	Mode    Mode
	Content []byte
	Added   []byte
	perm    fs.FileMode
}

type Result struct {
	Root   string
	Files  []File
	Report Report
}

type project struct {
	root     string
	files    []string
	firebase *firebaseFound
	mysql    *mysqlFound
	postgres []composeService
	devs     []devServer
	steps    []setupStep
	ports    []string
	report   *Report
	packages []pkg
}

func Plan(ctx context.Context, dir string) (*Result, error) {
	wt, err := gitx.Detect(ctx, dir)
	if errors.Is(err, gitx.ErrNotRepo) {
		return nil, errors.New("lanes init: not inside a git repository — run it in the project's checkout")
	}
	if err != nil {
		return nil, err
	}
	root := wt.Root
	for _, name := range []string{config.FileName, pc.ProjectFile} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return nil, fmt.Errorf("lanes init: %s already exists at %s — lanes init writes a project's lanes files once and never overwrites them; edit %s instead", name, filepath.Join(root, name), name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	files, err := gitx.ProjectFiles(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("lanes init: list the project's files: %w", err)
	}
	p := &project{root: root, files: regular(root, files), report: &Report{}}
	p.detect()

	res := &Result{Root: root}
	res.Files = append(res.Files,
		File{Path: config.FileName, Mode: Write, Content: p.renderLanesYAML(), perm: versionedPerm},
		File{Path: pc.ProjectFile, Mode: Write, Content: p.renderProcessCompose(), perm: versionedPerm},
	)
	for _, build := range []func() (*File, error){p.settings, p.envrc, p.gitignore} {
		f, err := build()
		if err != nil {
			return nil, err
		}
		if f != nil {
			res.Files = append(res.Files, *f)
		}
	}
	p.describeFiles(res.Files)
	res.Report = *p.report
	return res, nil
}

func regular(root string, files []string) []string {
	var out []string
	for _, f := range files {
		if info, err := os.Lstat(filepath.Join(root, f)); err == nil && info.Mode().IsRegular() {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

func (p *project) detect() {
	p.scanPackages()
	p.detectFirebase()
	p.detectCompose()
	p.detectDevServers()
	p.detectSetup()
	for _, d := range p.devs {
		p.ports = append(p.ports, d.port)
	}
	if p.firebase != nil {
		p.ports = append(p.ports, p.firebase.stack...)
		p.ports = append(p.ports, p.firebase.test...)
	}
	p.report.NothingDetected = p.firebase == nil && p.mysql == nil && len(p.postgres) == 0 && len(p.devs) == 0 && len(p.steps) == 0
}

func blockSize(n int) int {
	b := (n + 9) / 10 * 10
	return max(b, 10)
}

func (p *project) read(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(p.root, rel))
}

// existing refuses a symbolic link on the way to a merged file: the atomic
// rename would replace the link with a regular copy of its target, and the
// report would then ask to commit that copy.
func (p *project) existing(rel string) ([]byte, fs.FileMode, bool, error) {
	path := filepath.Join(p.root, rel)
	for at := filepath.Dir(rel); at != "."; at = filepath.Dir(at) {
		if info, err := os.Lstat(filepath.Join(p.root, at)); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return nil, 0, false, fmt.Errorf("lanes init: %s is a symbolic link — lanes init does not write through it; nothing was written", filepath.Join(p.root, at))
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, versionedPerm, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		return nil, 0, false, fmt.Errorf("lanes init: %s is a symbolic link to %s — lanes init does not replace a link; add the lanes lines to its target by hand, or replace the link with a file; nothing was written", path, target)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("lanes init: %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	return data, info.Mode().Perm(), true, err
}

// Apply writes the merged files first and lanes.yaml last: lanes.yaml is what
// makes a rerun refuse, and the merges are idempotent, so a write that fails
// halfway leaves a checkout that a rerun completes.
func (r *Result) Apply() error {
	order := slices.Clone(r.Files)
	slices.SortStableFunc(order, func(a, b File) int { return lastWritten(a) - lastWritten(b) })
	var created []string
	for _, f := range order {
		path := filepath.Join(r.Root, f.Path)
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err == nil {
			err = fsx.WriteAtomic(path, f.Content, f.perm)
		}
		if err != nil {
			for _, c := range created {
				_ = os.Remove(c)
			}
			return fmt.Errorf("lanes init: write %s: %w — run lanes init again once fixed", f.Path, err)
		}
		if lastWritten(f) > 0 {
			created = append(created, path)
		}
	}
	return nil
}

func lastWritten(f File) int {
	switch f.Path {
	case pc.ProjectFile:
		return 1
	case config.FileName:
		return 2
	}
	return 0
}
