package lane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
)

const slugMax = 24

type Record struct {
	ID                   string    `json:"id"`
	Branch               string    `json:"branch"`
	BornAt               time.Time `json:"bornAt"`
	LastTip              string    `json:"lastTip,omitempty"`
	LastTipHadOwnCommits bool      `json:"lastTipHadOwnCommits"`
	Created              []string  `json:"created,omitempty"`
}

func (r Record) HasCreated(adapter string) bool { return slices.Contains(r.Created, adapter) }

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(branch string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(branch), "_"), "_")
	if len(s) > slugMax {
		s = strings.TrimRight(s[:slugMax], "_")
	}
	if s == "" {
		return "lane"
	}
	return s
}

func NewID(branch string) string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic("lanes: no randomness: " + err.Error())
	}
	return Slug(branch) + "_" + hex.EncodeToString(b)
}

func Load(adminDir string) (*Record, error) {
	var r Record
	ok, err := fsx.ReadJSON(paths.LaneRecord(adminDir), &r)
	if err != nil || !ok {
		return nil, err
	}
	return &r, nil
}

func Save(adminDir string, r Record) error {
	return fsx.WriteJSON(paths.LaneRecord(adminDir), r)
}

type Retire func(ctx context.Context, old Record) error

// Check holds the lane lock so two hooks firing together cannot both give
// birth to a lane.
func Check(ctx context.Context, wt gitx.Worktree, retire Retire) (Record, bool, error) {
	var (
		rec  Record
		born bool
	)
	err := fsx.WithLock(paths.LaneLock(wt.AdminDir), func() error {
		cur, err := Load(wt.AdminDir)
		if err != nil {
			return err
		}
		state, err := observe(ctx, wt.Root)
		if err != nil {
			return err
		}
		if cur != nil && sameLane(*cur, state) {
			rec = refresh(*cur, state)
		} else {
			if cur != nil {
				if err := retire(ctx, *cur); err != nil {
					return err
				}
			}
			born = true
			rec = refresh(Record{ID: NewID(birthName(state)), Branch: state.branch, BornAt: time.Now().UTC()}, state)
		}
		return Save(wt.AdminDir, rec)
	})
	return rec, born, err
}

func MarkCreated(wt gitx.Worktree, laneID, adapter string) error {
	return fsx.WithLock(paths.LaneLock(wt.AdminDir), func() error {
		cur, err := Load(wt.AdminDir)
		if err != nil || cur == nil || cur.ID != laneID || cur.HasCreated(adapter) {
			return err
		}
		cur.Created = append(cur.Created, adapter)
		return Save(wt.AdminDir, *cur)
	})
}

type observed struct {
	branch      string
	tip         string
	ownCommits  bool
	ownKnown    bool
	renamedFrom func(string) bool
}

func observe(ctx context.Context, root string) (observed, error) {
	branch, err := gitx.Branch(ctx, root)
	if err != nil {
		return observed{}, err
	}
	tip, err := gitx.Head(ctx, root)
	if err != nil {
		return observed{}, err
	}
	o := observed{branch: branch, tip: tip}
	if branch == "" {
		return o, nil
	}
	if base := gitx.DefaultBranch(ctx, root); base != "" && tip != "" {
		if n, err := gitx.CommitsOutside(ctx, root, tip, base); err == nil {
			o.ownCommits, o.ownKnown = n > 0, true
		}
	}
	renames := gitx.Renames(ctx, root, branch)
	o.renamedFrom = func(from string) bool { return gitx.RenamedInto(renames, from, branch) }
	return o, nil
}

func birthName(o observed) string {
	if o.branch == "" {
		return "detached"
	}
	return o.branch
}

func sameLane(rec Record, o observed) bool {
	switch {
	case o.branch == "":
		return true
	case rec.Branch == "":
		return true
	case o.branch == rec.Branch:
		return !resetToDefault(rec, o)
	default:
		return o.renamedFrom(rec.Branch)
	}
}

// resetToDefault spots a branch moved onto the default branch after its work
// merged: the tip moved, the branch no longer holds a commit of its own, and
// it did before. Ancestry cannot tell it: a merge commit keeps the old tip in
// the default branch's history.
func resetToDefault(rec Record, o observed) bool {
	return rec.LastTip != "" && o.tip != "" && o.tip != rec.LastTip &&
		rec.LastTipHadOwnCommits && o.ownKnown && !o.ownCommits
}

func refresh(rec Record, o observed) Record {
	if o.branch == "" {
		return rec
	}
	rec.Branch = o.branch
	if o.tip != "" && o.tip != rec.LastTip {
		rec.LastTip, rec.LastTipHadOwnCommits = o.tip, false
	}
	// Sticky for an unchanged tip: once its merge is fetched, the same tip
	// counts no commit of its own, and the reset that follows must still show.
	if o.tip != "" && o.ownKnown && o.ownCommits {
		rec.LastTipHadOwnCommits = true
	}
	return rec
}
