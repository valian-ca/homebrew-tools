package next

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func awaitingTrialFixture(t *testing.T, parent bool) *fixture {
	t.Helper()
	mode, tickets := "standalone", []Ticket{rootTicket}
	if parent {
		mode, tickets = "parent", []Ticket{childTicket, secondTicket}
	}
	f := setup(t, mode)
	r := f.request("plan")
	r.Plan = makePlan(tickets...)
	r.Plan.FinalChecks = &FinalChecks{Criteria: []string{"AC1"}, Tests: []string{"root-test"}, Surfaces: []string{"web"}, Visual: true}
	f.apply(r)
	for _, ticket := range tickets {
		f.open(ticket)
		p := f.prepare()
		sha := f.commit(p)
		f.finish(p)
		if parent {
			r = f.request("ack")
			r.Ticket, r.Commit, r.StateID, r.StateType = ticket.Identifier, sha, completedState, "completed"
			f.apply(r)
		}
	}
	return f
}

func environmentRequest(f *fixture) Request {
	f.t.Helper()
	r := f.request("repair-prepare")
	r.Repair = &RepairRequest{Kind: "environment", Reason: "Emulator socket prevents trial account preparation", Reference: "orchestration-diagnostic", Evidence: *f.evidence()}
	return r
}

func reopenStore(f *fixture) {
	f.t.Helper()
	s, err := Open()
	if err != nil {
		f.t.Fatal(err)
	}
	f.s = s
}

func TestEnvironmentRepairPreservesTrialGateAcrossRecovery(t *testing.T) {
	for _, parent := range []bool{false, true} {
		name := "standalone"
		if parent {
			name = "parent"
		}
		t.Run(name, func(t *testing.T) {
			f := awaitingTrialFixture(t, parent)
			before := f.status()
			for round := 0; round < 2; round++ {
				r := environmentRequest(f)
				prepared := f.apply(r)
				reopenStore(f)
				if f.apply(r) != prepared {
					t.Fatal("lost preparation response created another repair")
				}
				sha := f.commit(prepared)
				reopenStore(f)
				r = f.request("repair-finish")
				r.IntegrationID = prepared.IntegrationID
				finished := f.apply(r)
				reopenStore(f)
				if f.apply(r) != finished || finished.Commit != sha {
					t.Fatal("lost finish response changed integration")
				}
				r = f.request("stop")
				r.Reason = "user will try later"
				if round == 0 {
					r.Action, r.Class, r.Reason = "block", "environment", "another readiness failure"
				}
				f.apply(r)
				reopenStore(f)
				r = f.request("resume")
				r.Reason = "user returned"
				f.apply(r)
				c := f.status()
				if c.State != "awaiting-trial" || c.ExpectedHead != sha || c.Trial != nil || c.Verification != nil || len(c.Repairs) != round+1 {
					t.Fatal("repair bypassed the trial or lost the integrated HEAD")
				}
				if !reflect.DeepEqual(c.Contributions, before.Contributions) || !reflect.DeepEqual(c.Plan, before.Plan) {
					t.Fatal("repair rewrote the integrated contribution or its plan")
				}
				r = f.request("verification-save")
				r.Verification = finalReport(f)
				f.reject(r, ErrInvalid)
				f.reject(f.request("verification-complete"), ErrInvalid)
				r = f.request("delivery-start")
				r.BaseBranch = "main"
				f.reject(r, ErrInvalid)
			}
			for _, trial := range []UserTrial{
				{Head: before.ExpectedHead, Response: "tested", Reference: "reply", Readiness: "logs"},
				{Head: f.status().ExpectedHead, Response: "waiting", Reference: "reply", Readiness: "logs"},
				{Head: f.status().ExpectedHead, Response: "tested", Reference: "reply"},
				{Head: f.status().ExpectedHead, Response: "tested", Readiness: "logs"},
				{Head: f.status().ExpectedHead, Response: "not-applicable", Reference: "plan", NotApplicableReason: "broken stack"},
			} {
				r := f.request("trial-record")
				r.Trial = &trial
				f.reject(r, ErrInvalid)
			}
			r := f.request("trial-record")
			r.Trial = &UserTrial{Head: f.status().ExpectedHead, Response: "tested", Reference: "actual-user-reply", Readiness: "repaired-stack-log"}
			f.apply(r)
			reopenStore(f)
			saveReport(f, finalReport(f))
			f.apply(f.request("verification-complete"))
			if f.run(f.cwd, "rev-list", "--count", "HEAD") != map[bool]string{false: "4", true: "5"}[parent] {
				t.Fatal("recovery duplicated a commit")
			}
		})
	}
}

func TestEnvironmentRepairPreparationGuards(t *testing.T) {
	for _, scenario := range []string{"reason", "reference", "findings", "qa", "ci", "kind", "tests", "failed-test", "review", "tree", "lease", "expired", "checkout", "head", "blocked", "verifying"} {
		t.Run(scenario, func(t *testing.T) {
			f := awaitingTrialFixture(t, false)
			if scenario == "blocked" {
				r := f.request("block")
				r.Class, r.Reason = "environment", "needs repair"
				f.apply(r)
			}
			if scenario == "verifying" {
				r := f.request("trial-record")
				r.Trial = &UserTrial{Head: f.status().ExpectedHead, Response: "declined", Reference: "reply", Readiness: "logs"}
				f.apply(r)
			}
			r := environmentRequest(f)
			want := ErrInvalid
			switch scenario {
			case "reason":
				r.Repair.Reason = ""
			case "reference":
				r.Repair.Reference = ""
			case "findings":
				r.Repair.FindingIDs = []string{"pretend-qa"}
			case "qa", "ci", "kind":
				r.Repair.Kind, r.Repair.Reason, r.Repair.Reference = scenario, "", ""
				r.Repair.FindingIDs = []string{"bug"}
				if scenario == "qa" {
					r.Repair.Kind = ""
				}
			case "tests":
				r.Repair.Evidence.Tests = nil
			case "failed-test":
				r.Repair.Evidence.Tests[0].Status = "fail"
			case "review":
				r.Repair.Evidence.Review.Independent = false
			case "tree":
				r.Repair.Evidence.Tree = f.status().Base
			case "lease":
				r.Token, want = "stale", ErrLease
			case "expired":
				expires := f.status().Lease.ExpiresAt
				f.s.now = func() time.Time { return expires.Add(time.Second) }
				want = ErrLease
			case "checkout":
				r.CWD = filepath.Join(f.sandbox, "other")
				f.newRepo(r.CWD)
				want = ErrCheckout
			case "head":
				f.commit(Receipt{})
				want = ErrReconcile
			}
			f.reject(r, want)
		})
	}
}

func TestEnvironmentRepairRecoveryWithNewOwner(t *testing.T) {
	f := awaitingTrialFixture(t, false)
	p := f.apply(environmentRequest(f))
	sha := f.commit(p)
	expires := f.status().Lease.ExpiresAt
	reopenStore(f)
	f.s.now = func() time.Time { return expires.Add(time.Second) }
	r := f.request("acquire")
	r.Session, r.PreviousToken = "session-b", f.token
	f.reject(r, ErrLease)
	r.ConfirmOwnerStopped = true
	owner := f.apply(r)
	r = f.request("repair-finish")
	r.IntegrationID = p.IntegrationID
	f.reject(r, ErrLease)
	r.Session, r.Token = "session-b", owner.Token
	f.apply(r)
	reopenStore(f)
	c := f.status()
	if c.State != "awaiting-trial" || c.ExpectedHead != sha || c.Trial != nil || len(c.Repairs) != 1 || c.Repairs[0].Reference != "orchestration-diagnostic" {
		t.Fatal("new owner lost the repair or skipped the trial")
	}
}

func TestEnvironmentRepairPendingIntentAndCancel(t *testing.T) {
	f := awaitingTrialFixture(t, false)
	r := environmentRequest(f)
	p := f.apply(r)
	r = f.request("repair-prepare")
	r.Repair = environmentRequest(f).Repair
	f.reject(r, ErrReconcile)
	r = f.request("block")
	r.Class, r.Reason = "environment", "pause"
	f.reject(r, ErrReconcile)
	f.run(f.cwd, "restore", "--source=HEAD", "--staged", "--worktree", ".")
	r = f.request("trial-record")
	r.Trial = &UserTrial{Head: f.status().ExpectedHead, Response: "tested", Reference: "reply", Readiness: "logs"}
	f.reject(r, ErrReconcile)
	r = f.request("repair-cancel")
	r.IntegrationID = p.IntegrationID
	f.apply(r)
	reopenStore(f)
	if len(f.status().Repairs) != 0 || f.status().State != "awaiting-trial" {
		t.Fatal("cancel changed trial gate")
	}
	p = f.apply(environmentRequest(f))
	f.commit(p)
	r = f.request("repair-cancel")
	r.IntegrationID = p.IntegrationID
	f.reject(r, ErrReconcile)
	r = f.request("repair-finish")
	r.IntegrationID = p.IntegrationID
	f.apply(r)
}

func TestEnvironmentRepairFinishRequiresExactCommitAndOwner(t *testing.T) {
	for _, scenario := range []string{"no-commit", "tree", "extra-commit", "dirty", "lease", "checkout"} {
		t.Run(scenario, func(t *testing.T) {
			f := awaitingTrialFixture(t, false)
			p := f.apply(environmentRequest(f))
			if scenario == "tree" {
				f.n++
				f.evidence()
			}
			if scenario != "no-commit" {
				f.commit(p)
			}
			r := f.request("repair-finish")
			r.IntegrationID = p.IntegrationID
			want := ErrReconcile
			switch scenario {
			case "extra-commit":
				f.run(f.cwd, "commit", "--allow-empty", "-m", "test: unexpected commit")
			case "dirty":
				want = ErrCheckout
				if err := os.WriteFile(filepath.Join(f.cwd, "untracked"), []byte("dirty"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "lease":
				r.Token, want = "stale", ErrLease
			case "checkout":
				r.CWD = filepath.Join(f.sandbox, "other")
				f.newRepo(r.CWD)
				want = ErrCheckout
			}
			f.reject(r, want)
		})
	}
}
