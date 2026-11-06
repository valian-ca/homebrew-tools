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
	// Added is what an append adds, shown alone by a dry run.
	Added []byte
	perm  fs.FileMode
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

func (p *project) existing(rel string) ([]byte, fs.FileMode, bool, error) {
	path := filepath.Join(p.root, rel)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, versionedPerm, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("lanes init: %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	return data, info.Mode().Perm(), true, err
}

// Apply writes only once every file is computed, so a refusal leaves the
// checkout untouched.
func (r *Result) Apply() error {
	for _, f := range r.Files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(r.Root, f.Path)), 0o755); err != nil {
			return fmt.Errorf("lanes init: write %s: %w", f.Path, err)
		}
		if err := fsx.WriteAtomic(filepath.Join(r.Root, f.Path), f.Content, f.perm); err != nil {
			return fmt.Errorf("lanes init: write %s: %w", f.Path, err)
		}
	}
	return nil
}
