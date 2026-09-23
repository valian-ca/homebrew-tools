package next

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var rootTicket = Ticket{ID: "11111111-1111-4111-8111-111111111111", Identifier: "TEST-1"}
var childTicket = Ticket{ID: "22222222-2222-4222-8222-222222222222", Identifier: "TEST-2"}
var secondTicket = Ticket{ID: "33333333-3333-4333-8333-333333333333", Identifier: "TEST-3"}
var completedState = "44444444-4444-4444-8444-444444444444"

type fixture struct {
	t       *testing.T
	s       *Store
	ctx     context.Context
	sandbox string
	cwd     string
	id      string
	token   string
	n       int
}

func setup(t *testing.T, mode string) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, s: s, ctx: context.Background(), sandbox: base, cwd: filepath.Join(base, "repo")}
	f.newRepo(f.cwd)
	r := f.request("start")
	r.Start = &StartSpec{Root: rootTicket, Mode: mode}
	receipt := f.apply(r)
	f.id, f.token = receipt.CampaignID, receipt.Token
	return f
}

func (f *fixture) run(cwd string, args ...string) string {
	f.t.Helper()
	rel, err := filepath.Rel(f.sandbox, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		f.t.Fatal("git refused outside test sandbox")
	}
	out, err := git(f.ctx, cwd, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) newRepo(path string) {
	f.t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		f.t.Fatal(err)
	}
	f.run(path, "init", "-b", "main")
	f.run(path, "config", "user.name", "Next Test")
	f.run(path, "config", "user.email", "next@example.invalid")
	f.run(path, "config", "commit.gpgsign", "false")
	f.run(path, "commit", "--allow-empty", "-m", "test: base")
	f.run(path, "switch", "-c", "feature")
}

func (f *fixture) request(action string) Request {
	f.n++
	return Request{Action: action, CampaignID: f.id, Session: "session-a", Token: f.token,
		OperationID: fmt.Sprintf("op-%d", f.n), CWD: f.cwd}
}

func (f *fixture) apply(r Request) Receipt {
	f.t.Helper()
	receipt, err := f.s.Apply(f.ctx, r)
	if err != nil {
		f.t.Fatalf("%s: %v", r.Action, err)
	}
	return receipt
}

func (f *fixture) status() *Campaign {
	f.t.Helper()
	c, err := f.s.Status(f.ctx, f.id)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) reject(r Request, want error) {
	f.t.Helper()
	before, err := os.ReadFile(f.s.path(f.id))
	if err != nil {
		f.t.Fatal(err)
	}
	receipt, err := f.s.Apply(f.ctx, r)
	if !errors.Is(err, want) {
		f.t.Fatalf("%s: got %v, want %v", r.Action, err, want)
	}
	if receipt != (Receipt{}) {
		f.t.Fatalf("failed mutation returned receipt: %+v", receipt)
	}
	after, err := os.ReadFile(f.s.path(f.id))
	if err != nil {
		f.t.Fatal(err)
	}
	if string(before) != string(after) {
		f.t.Fatal("rejected mutation changed state")
	}
}

func makePlan(tickets ...Ticket) *Plan {
	p := &Plan{Reference: "linear-document-plan-1", Revision: 1, Contributions: []ContributionSpec{}}
	for n, t := range tickets {
		spec := ContributionSpec{Ticket: t, Contract: "Implement the agreed local behavior", RequiredTests: []string{"format", "analyze", "unit"}, DependsOn: []string{}}
		if n > 0 {
			spec.DependsOn = []string{tickets[n-1].Identifier}
		}
		p.Contributions = append(p.Contributions, spec)
	}
	return p
}

func (f *fixture) plan(tickets ...Ticket) {
	f.t.Helper()
	r := f.request("plan")
	r.Plan = makePlan(tickets...)
	f.apply(r)
}

func (f *fixture) open(ticket Ticket) {
	f.t.Helper()
	r := f.request("open")
	r.Ticket = ticket.Identifier
	f.apply(r)
}

func (f *fixture) evidence() *Evidence {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.cwd, fmt.Sprintf("contribution-%d.txt", f.n)), []byte("verified content\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.run(f.cwd, "add", ".")
	e := &Evidence{Tree: f.run(f.cwd, "write-tree"), Review: ReviewEvidence{Independent: true, Reference: "/tmp/review.md"}, Tests: []TestEvidence{}}
	for _, name := range []string{"format", "analyze", "unit"} {
		e.Tests = append(e.Tests, TestEvidence{Name: name, Command: "tool " + name, Status: "pass", Reference: "/tmp/" + name + ".log"})
	}
	return e
}

func (f *fixture) prepare() Receipt {
	f.t.Helper()
	r := f.request("prepare")
	r.Evidence = f.evidence()
	return f.apply(r)
}

func (f *fixture) commit(prepared Receipt) string {
	f.t.Helper()
	f.run(f.cwd, "commit", "-m", "feat: integrate contribution")
	return f.run(f.cwd, "rev-parse", "HEAD")
}

func (f *fixture) finish(prepared Receipt) Receipt {
	f.t.Helper()
	r := f.request("finish")
	r.IntegrationID = prepared.IntegrationID
	return f.apply(r)
}

func TestStandaloneLifecycleAndLostResponses(t *testing.T) {
	f := setup(t, "standalone")
	c := f.status()
	if c.Pipeline != Pipeline || c.State != "planning" || c.Checkout.Worktree != f.cwd {
		t.Fatalf("start: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(f.sandbox, ".atelier")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy state touched")
	}
	f.plan(rootTicket)
	f.open(rootTicket)
	preparedRequest := f.request("prepare")
	preparedRequest.Evidence = f.evidence()
	prepared := f.apply(preparedRequest)
	if again := f.apply(preparedRequest); again != prepared {
		t.Fatal("prepare response not idempotent")
	}
	f.reject(func() Request { r := f.request("finish"); r.IntegrationID = prepared.IntegrationID; return r }(), ErrReconcile)
	sha := f.commit(prepared)
	// Reload through a fresh Store after the git commit but before any state write.
	fresh, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	f.s = fresh
	finish := f.request("finish")
	finish.IntegrationID = prepared.IntegrationID
	done := f.apply(finish)
	if done.Commit != sha || f.apply(finish) != done {
		t.Fatal("finish response not idempotent")
	}
	f.finish(prepared)
	c = f.status()
	if c.State != "awaiting-trial" || c.ExpectedHead != sha || c.Contributions[0].Linear.Pending {
		t.Fatalf("standalone finish: %+v", c)
	}
	integrations := 0
	for _, e := range c.Events {
		if e.Type == "atelier-next:contribution-integrated" {
			integrations++
		}
	}
	if integrations != 1 {
		t.Fatalf("duplicate integration events: %d", integrations)
	}
	r := f.request("ack")
	r.Ticket, r.Commit, r.StateID, r.StateType = rootTicket.Identifier, sha, completedState, "completed"
	f.reject(r, ErrInvalid)
	if f.run(f.cwd, "rev-list", "--count", "HEAD") != "2" {
		t.Fatal("duplicate commit")
	}
}

func TestParentOrderAndPendingLinearSync(t *testing.T) {
	f := setup(t, "parent")
	f.plan(childTicket, secondTicket)
	r := f.request("open")
	r.Ticket = secondTicket.Identifier
	f.reject(r, ErrInvalid)
	f.open(childTicket)
	r = f.request("open")
	r.Ticket = secondTicket.Identifier
	f.reject(r, ErrInvalid)
	prepared := f.prepare()
	sha := f.commit(prepared)
	f.finish(prepared)
	c := f.status()
	if !c.Contributions[0].Linear.Pending || c.State != "realizing" {
		t.Fatal("missing durable pending sync")
	}
	r = f.request("ack")
	r.Ticket, r.Commit, r.StateID, r.StateType = childTicket.Identifier, sha, completedState, "started"
	f.reject(r, ErrInvalid)
	r.StateType = "completed"
	ack := f.apply(r)
	if f.apply(r) != ack {
		t.Fatal("Linear ack replay not stable")
	}
	c = f.status()
	if c.Contributions[0].Linear.Pending || c.Contributions[0].Linear.AcknowledgedAt == nil {
		t.Fatal("Linear sync not acknowledged")
	}
	f.open(secondTicket)
	prepared = f.prepare()
	f.commit(prepared)
	f.finish(prepared)
	if f.status().State != "awaiting-trial" || !f.status().Contributions[1].Linear.Pending {
		t.Fatal("parent must keep pending sync visible at verification entry")
	}
}

func TestExclusiveLeaseAndExplicitTakeover(t *testing.T) {
	f := setup(t, "standalone")
	f.plan(rootTicket)
	r := f.request("acquire")
	r.Session = "session-b"
	f.reject(r, ErrLease)
	now := f.status().Lease.ExpiresAt.Add(time.Second)
	f.s.now = func() time.Time { return now }
	r = f.request("open")
	r.Ticket = rootTicket.Identifier
	f.reject(r, ErrLease)
	r = f.request("acquire")
	r.Session = "session-b"
	f.reject(r, ErrLease)
	r.PreviousToken, r.ConfirmOwnerStopped = f.token, true
	owner := f.apply(r)
	if owner.Token == f.token {
		t.Fatal("takeover did not fence old token")
	}
	r = f.request("open")
	r.Ticket = rootTicket.Identifier
	f.reject(r, ErrLease)
	r = f.request("open")
	r.Session, r.Token, r.Ticket = "session-b", owner.Token, rootTicket.Identifier
	f.apply(r)
}

func TestLeaseRenewReleaseAndSimultaneousAcquisition(t *testing.T) {
	f := setup(t, "standalone")
	oldExpiry := f.status().Lease.ExpiresAt
	f.s.now = func() time.Time { return oldExpiry.Add(-time.Minute) }
	renew := f.apply(f.request("renew"))
	if renew.Token != f.token || !f.status().Lease.ExpiresAt.After(oldExpiry) {
		t.Fatal("renew failed")
	}
	f.apply(f.request("release"))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n := 0; n < 2; n++ {
		r := f.request("acquire")
		r.Session = fmt.Sprintf("new-%d", n)
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.s.Apply(f.ctx, r); results <- err }()
	}
	wg.Wait()
	close(results)
	wins, losses := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrLease) {
			losses++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("lease winners=%d losers=%d", wins, losses)
	}
}

func TestCheckoutAndStartGuards(t *testing.T) {
	f := setup(t, "standalone")
	start := f.request("start")
	start.Start = &StartSpec{Root: rootTicket, Mode: "standalone"}
	f.reject(start, ErrConflict)
	other := filepath.Join(f.sandbox, "other")
	f.newRepo(other)
	r := f.request("renew")
	r.CWD = other
	f.reject(r, ErrCheckout)
	f.run(f.cwd, "switch", "-c", "wrong-branch")
	f.reject(f.request("renew"), ErrCheckout)
	f.run(f.cwd, "switch", "feature")
	f.run(f.cwd, "switch", "--detach")
	f.reject(f.request("renew"), ErrCheckout)
	f.run(f.cwd, "switch", "feature")
	worktree := filepath.Join(f.sandbox, "worktree")
	f.run(f.cwd, "worktree", "add", "-b", "other-feature", worktree)
	r = f.request("renew")
	r.CWD = worktree
	f.reject(r, ErrCheckout)
	r = f.request("start")
	r.Start = &StartSpec{Root: rootTicket, Mode: "standalone"}
	r.CWD = worktree
	f.apply(r)
	f.run(other, "switch", "main")
	r = f.request("start")
	r.Start = &StartSpec{Root: rootTicket, Mode: "standalone"}
	r.CWD = other
	f.reject(r, ErrCheckout)
}

func TestStartResponseRecoveryAndFindAmbiguity(t *testing.T) {
	f := setup(t, "standalone")
	r := Request{Action: "start", Session: "session-a", OperationID: "op-1", CWD: f.cwd, Start: &StartSpec{Root: rootTicket, Mode: "standalone"}}
	if receipt := f.apply(r); receipt.CampaignID != f.id || receipt.Token != f.token || receipt.Revision != 1 {
		t.Fatal("start response changed")
	}
	if _, err := f.s.Find(f.ctx, "TEST-9", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if id, err := f.s.Find(f.ctx, rootTicket.Identifier, ""); err != nil || id != f.id {
		t.Fatal(id, err)
	}
	other := filepath.Join(f.sandbox, "other")
	f.newRepo(other)
	r = Request{Action: "start", Session: "session-b", OperationID: "start-other", CWD: other, Start: &StartSpec{Root: rootTicket, Mode: "standalone"}}
	f.apply(r)
	if _, err := f.s.Find(f.ctx, rootTicket.Identifier, ""); !errors.Is(err, ErrAmbiguous) {
		t.Fatal(err)
	}
	if id, err := f.s.Find(f.ctx, rootTicket.Identifier, f.status().Checkout.Repository); err != nil || id != f.id {
		t.Fatal(id, err)
	}
}

func TestPlanValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Plan)
	}{
		{"duplicate", func(p *Plan) { p.Contributions = append(p.Contributions, p.Contributions[0]) }},
		{"cycle", func(p *Plan) { p.Contributions[0].DependsOn = []string{secondTicket.Identifier} }},
		{"missing-dependency", func(p *Plan) { p.Contributions[1].DependsOn = []string{"TEST-99"} }},
		{"missing-tests", func(p *Plan) { p.Contributions[0].RequiredTests = nil }},
		{"duplicate-test", func(p *Plan) { p.Contributions[0].RequiredTests = []string{"unit", "unit"} }},
		{"invalid-uuid", func(p *Plan) { p.Contributions[0].Ticket.ID = "not-a-uuid" }},
		{"root-as-child", func(p *Plan) { p.Contributions[0].Ticket = rootTicket }},
		{"revision", func(p *Plan) { p.Revision = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, "parent")
			r := f.request("plan")
			r.Plan = makePlan(childTicket, secondTicket)
			tc.edit(r.Plan)
			f.reject(r, ErrInvalid)
		})
	}
}

func TestEvidenceGatesAndCancel(t *testing.T) {
	f := setup(t, "standalone")
	f.plan(rootTicket)
	f.open(rootTicket)
	e := f.evidence()
	for _, edit := range []func(*Evidence){
		func(e *Evidence) { e.Review.Independent = false },
		func(e *Evidence) { e.Tests = e.Tests[:1] },
		func(e *Evidence) { e.Tests[0].Status = "fail" },
		func(e *Evidence) { e.Tree = strings.Repeat("a", 40) },
		func(e *Evidence) { e.Tests[0].Reference = "" },
	} {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var copy Evidence
		if err := json.Unmarshal(data, &copy); err != nil {
			t.Fatal(err)
		}
		edit(&copy)
		r := f.request("prepare")
		r.Evidence = &copy
		f.reject(r, ErrInvalid)
	}
	if err := os.WriteFile(filepath.Join(f.cwd, "untracked"), []byte("change"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.request("prepare")
	r.Evidence = e
	f.reject(r, ErrInvalid)
	if err := os.Remove(filepath.Join(f.cwd, "untracked")); err != nil {
		t.Fatal(err)
	}
	prepared := f.apply(r)
	r = f.request("block")
	r.Class, r.Reason = "environment", "missing dependency"
	f.reject(r, ErrReconcile)
	r = f.request("cancel")
	r.IntegrationID = prepared.IntegrationID
	f.apply(r)
	if f.status().Contributions[0].Integration != nil {
		t.Fatal("cancel did not clear pending intent")
	}
}

func TestIntegrationRejectsAmbiguousCommit(t *testing.T) {
	for _, tc := range []string{"wrong-tree", "multiple-commits", "merge-commit", "dirty-after-commit"} {
		t.Run(tc, func(t *testing.T) {
			f := setup(t, "standalone")
			f.plan(rootTicket)
			f.open(rootTicket)
			prepared := f.prepare()
			switch tc {
			case "wrong-tree":
				if err := os.WriteFile(filepath.Join(f.cwd, "extra"), []byte("extra"), 0o600); err != nil {
					t.Fatal(err)
				}
				f.run(f.cwd, "add", ".")
				f.commit(prepared)
			case "multiple-commits":
				f.commit(prepared)
				f.run(f.cwd, "commit", "--allow-empty", "-m", "feat: another commit")
			case "merge-commit":
				parent := f.status().ExpectedHead
				other := f.run(f.cwd, "commit-tree", f.run(f.cwd, "rev-parse", "HEAD^{tree}"), "-m", "other root")
				merge := f.run(f.cwd, "commit-tree", f.status().Contributions[0].Integration.Tree, "-p", parent, "-p", other, "-m", "merge")
				f.run(f.cwd, "reset", "--hard", merge)
			case "dirty-after-commit":
				f.commit(prepared)
				if err := os.WriteFile(filepath.Join(f.cwd, "extra"), []byte("extra"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r := f.request("finish")
			r.IntegrationID = prepared.IntegrationID
			want := ErrReconcile
			if tc == "dirty-after-commit" {
				want = ErrCheckout
			}
			f.reject(r, want)
			r = f.request("cancel")
			r.IntegrationID = prepared.IntegrationID
			f.reject(r, ErrReconcile)
		})
	}
}

func TestScopeBlocksAndReplanning(t *testing.T) {
	f := setup(t, "parent")
	f.plan(childTicket, secondTicket)
	f.open(childTicket)
	r := f.request("block")
	r.Class, r.Reason = "scope-transversal", "shared interface decision required"
	f.apply(r)
	r = f.request("prepare")
	r.Evidence = &Evidence{}
	f.reject(r, ErrInvalid)
	r = f.request("resume")
	r.Reason = "user decided"
	f.reject(r, ErrInvalid)
	r.DecisionReference = "linear-comment-decision"
	f.reject(r, ErrInvalid)
	plan := f.request("plan")
	plan.Plan = makePlan(childTicket, secondTicket)
	plan.Plan.Revision = 2
	plan.Plan.DecisionReference = r.DecisionReference
	plan.Plan.Contributions[0].Contract = "Updated interface agreed by user"
	f.apply(plan)
	f.apply(r)
	c := f.status()
	if c.State != "realizing" || c.Blocks[0].ResolvedAt == nil || c.Plan.Revision != 2 {
		t.Fatalf("resume: %+v", c)
	}
	r = f.request("block")
	r.Class, r.Reason = "scope-local", "local behavior question"
	f.apply(r)
	r = f.request("resume")
	r.Reason = "keep original scope"
	r.DecisionReference = "linear-comment-local"
	f.apply(r)
	r = f.request("stop")
	r.Reason = "user stopped the run"
	f.apply(r)
	if f.status().State != "stopped" {
		t.Fatal("stop not durable")
	}
	r = f.request("resume")
	r.Reason = "user requested resume"
	f.apply(r)
}

func TestOperationConflictsAndEventEnvelope(t *testing.T) {
	f := setup(t, "standalone")
	r := f.request("plan")
	r.Plan = makePlan(rootTicket)
	f.apply(r)
	r.Plan.Reference = "different-document"
	f.reject(r, ErrConflict)
	f.open(rootTicket)
	prepared := f.prepare()
	f.commit(prepared)
	f.finish(prepared)
	c := f.status()
	ids := map[string]bool{}
	for n, e := range c.Events {
		if e.Pipeline != Pipeline || e.Mode != "standalone" || e.CampaignID != f.id || e.RootTicketID != rootTicket.Identifier || e.TicketID != rootTicket.Identifier || e.SchemaVersion != SchemaVersion || e.Revision != n+1 || e.SessionID != "session-a" || ids[e.ID] {
			t.Fatalf("invalid event %+v", e)
		}
		ids[e.ID] = true
	}
	before := c.Events
	if !reflect.DeepEqual(before, f.status().Events) {
		t.Fatal("read generated events")
	}
}

func TestStoreRejectsSymlinksAndCorruption(t *testing.T) {
	f := setup(t, "standalone")
	path := f.s.path(f.id)
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(f.sandbox, "other-state")
	if err := os.WriteFile(outside, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Status(f.ctx, f.id); err == nil {
		t.Fatal("followed state symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var c Campaign
	if err := json.Unmarshal(bytes, &c); err != nil {
		t.Fatal(err)
	}
	c.Pipeline = "atelier"
	corrupt, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Status(f.ctx, f.id); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := f.s.Status(f.ctx, "../../escape"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestStrictStagingAndFileModes(t *testing.T) {
	f := setup(t, "standalone")
	for _, content := range []string{`{"root":{},"unknown":true}`, `{} {}`, strings.Repeat("x", MaxFileBytes+1)} {
		path := filepath.Join(f.sandbox, "staging.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := DecodeFile(path, &StartSpec{}); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	for _, path := range []string{f.s.root, filepath.Join(f.s.root, "campaigns"), f.s.path(f.id)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %o", path, info.Mode().Perm())
		}
	}
}

func TestContextCancelsBusyStore(t *testing.T) {
	f := setup(t, "standalone")
	lock, err := os.OpenFile(filepath.Join(f.s.root, ".lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := f.s.Status(ctx, f.id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
