package next

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLocalReplanCannotWidenToAnotherChild(t *testing.T) {
	f := setup(t, "parent")
	f.plan(childTicket, secondTicket)
	f.open(childTicket)
	r := f.request("block")
	r.Class, r.Reason = "scope-local", "local product question"
	f.apply(r)
	r = f.request("plan")
	r.Plan = makePlan(childTicket, secondTicket)
	r.Plan.Revision, r.Plan.DecisionReference = 2, "linear-decision-local"
	r.Plan.Contributions[1].Contract = "Silently changed another child"
	f.reject(r, ErrInvalid)
	r.Plan = makePlan(childTicket, secondTicket)
	r.Plan.Revision, r.Plan.DecisionReference = 2, "linear-decision-local"
	r.Plan.Contributions[0].Contract = "Agreed local change"
	f.apply(r)
}

func TestReplanPreservesIntegratedEvidence(t *testing.T) {
	f := setup(t, "parent")
	f.plan(childTicket, secondTicket)
	f.open(childTicket)
	prepared := f.prepare()
	sha := f.commit(prepared)
	f.finish(prepared)
	r := f.request("block")
	r.Class, r.Reason = "scope-transversal", "new cross-cutting requirement"
	f.apply(r)
	r = f.request("plan")
	r.Plan = makePlan(childTicket, secondTicket)
	r.Plan.Revision, r.Plan.DecisionReference = 2, "linear-decision"
	r.Plan.Contributions[0].RequiredTests = []string{"less-testing"}
	f.reject(r, ErrInvalid)
	r.Plan = makePlan(childTicket, secondTicket)
	r.Plan.Revision, r.Plan.DecisionReference = 2, "linear-decision"
	r.Plan.Contributions[1].Contract = "Updated pending work"
	f.apply(r)
	r = f.request("resume")
	r.DecisionReference, r.Reason = "linear-decision", "approved revised plan"
	f.apply(r)
	child := f.status().Contributions[0]
	if child.Integration.Commit != sha || !child.Linear.Pending {
		t.Fatal("replan discarded integrated evidence or pending sync")
	}
}

func TestFreshSessionReconcilesCommittedIntent(t *testing.T) {
	f := setup(t, "parent")
	f.plan(childTicket)
	f.open(childTicket)
	prepared := f.prepare()
	sha := f.commit(prepared)
	expired := f.status().Lease.ExpiresAt.Add(time.Second)
	fresh, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	fresh.now = func() time.Time { return expired }
	f.s = fresh
	r := f.request("acquire")
	r.Session, r.PreviousToken, r.ConfirmOwnerStopped = "fresh-session", f.token, true
	owner := f.apply(r)
	r = f.request("finish")
	r.Session, r.Token, r.IntegrationID = "fresh-session", owner.Token, prepared.IntegrationID
	receipt := f.apply(r)
	if receipt.Commit != sha || !f.status().Contributions[0].Linear.Pending {
		t.Fatal("fresh owner did not reconcile the existing commit")
	}
	if f.run(f.cwd, "rev-list", "--count", "HEAD") != "2" {
		t.Fatal("recovery created another commit")
	}
}

func TestParallelStartCannotDoubleBindCheckout(t *testing.T) {
	f := setup(t, "standalone")
	other := filepath.Join(f.sandbox, "another-repo")
	f.newRepo(other)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"start-a", "start-b"} {
		r := f.request("start")
		r.CWD, r.OperationID = other, name
		r.Start = &StartSpec{Root: rootTicket, Mode: "standalone"}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.s.Apply(f.ctx, r)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestGitEnvironmentCannotRedirectCampaignCheckout(t *testing.T) {
	f := setup(t, "standalone")
	other := filepath.Join(f.sandbox, "other")
	f.newRepo(other)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git/index"))
	f.apply(f.request("renew"))
	c := f.status()
	if c.Checkout.Worktree != f.cwd {
		t.Fatal("environment redirected git")
	}
	r := f.request("renew")
	r.CWD = other
	f.reject(r, ErrCheckout)
}

func TestNamespaceDirectorySymlinkIsRefused(t *testing.T) {
	f := setup(t, "standalone")
	original := f.s.root
	moved := filepath.Join(f.sandbox, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, original); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Status(f.ctx, f.id); err == nil {
		t.Fatal("namespace symlink followed")
	}
}
