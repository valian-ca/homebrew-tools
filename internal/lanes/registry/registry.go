package registry

import (
	"slices"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
)

const KindTmpdir = "tmpdir"

type Entry struct {
	Kind      string    `json:"kind"`
	Adapter   string    `json:"adapter"`
	Resource  string    `json:"resource"`
	Lane      string    `json:"lane,omitempty"`
	AdminDir  string    `json:"adminDir"`
	Worktree  string    `json:"worktree"`
	CreatedAt time.Time `json:"createdAt"`
	// Scripts and MySQL snapshot the adapter's spec: the entry must stay
	// erasable after its worktree, and the lanes.yaml in it, are gone.
	Scripts *config.Scripts `json:"scripts,omitempty"`
	MySQL   *config.MySQL   `json:"mysql,omitempty"`
}

type file struct {
	Entries []Entry `json:"entries"`
}

func Load(commonDir string) ([]Entry, error) {
	var f file
	_, err := fsx.ReadJSON(paths.Registry(commonDir), &f)
	return f.Entries, err
}

func Add(commonDir string, entries ...Entry) error {
	return update(commonDir, func(cur []Entry) []Entry {
		for _, e := range entries {
			if !slices.ContainsFunc(cur, func(c Entry) bool { return c.Kind == e.Kind && c.Adapter == e.Adapter && c.Resource == e.Resource }) {
				cur = append(cur, e)
			}
		}
		return cur
	})
}

// Refresh replaces the entry the same lane of the same worktree registered,
// so its snapshot follows lanes.yaml; another lane's claim on the resource is
// kept as is.
func Refresh(commonDir string, e Entry) error {
	return update(commonDir, func(cur []Entry) []Entry {
		for i, c := range cur {
			if c.Kind != e.Kind || c.Adapter != e.Adapter || c.Resource != e.Resource {
				continue
			}
			if c.Lane == e.Lane && c.AdminDir == e.AdminDir {
				e.CreatedAt = c.CreatedAt
				cur[i] = e
			}
			return cur
		}
		return append(cur, e)
	})
}

func Remove(commonDir string, e Entry) error {
	return update(commonDir, func(cur []Entry) []Entry {
		return slices.DeleteFunc(cur, func(c Entry) bool {
			return c.Kind == e.Kind && c.Adapter == e.Adapter && c.Resource == e.Resource
		})
	})
}

func update(commonDir string, fn func([]Entry) []Entry) error {
	return fsx.WithLock(paths.RegistryLock(commonDir), func() error {
		cur, err := Load(commonDir)
		if err != nil {
			return err
		}
		return fsx.WriteJSON(paths.Registry(commonDir), file{Entries: fn(cur)})
	})
}
