package next

import (
	"encoding/json"
	"testing"
)

func finalFixture(t *testing.T, visual, parent bool) *fixture {
	mode := "standalone"
	tickets := []Ticket{rootTicket}
	if parent {
		mode = "parent"
		tickets = []Ticket{childTicket, secondTicket}
	}
	f := setup(t, mode)
	r := f.request("plan")
	r.Plan = makePlan(tickets...)
	r.Plan.FinalChecks = &FinalChecks{Criteria: []string{"AC1"}, Tests: []string{"root-test"}, Surfaces: []string{}, Visual: visual, NoVisualReason: "static change"}
	if visual {
		r.Plan.FinalChecks.Surfaces = []string{"web"}
		r.Plan.FinalChecks.NoVisualReason = ""
	}
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

func finalReport(f *fixture) *Verification {
	c := f.status()
	v := &Verification{Head: c.ExpectedHead, PlanRevision: c.Plan.Revision, Reference: "linear-report", Review: ReviewEvidence{Independent: true, Reference: "root-review"}, Findings: []Finding{}, Captures: []Capture{}}
	for _, n := range c.Plan.FinalChecks.Criteria {
		v.Criteria = append(v.Criteria, Check{Name: n, Status: "pass", Reference: "root-evidence"})
	}
	for _, n := range c.Plan.FinalChecks.Tests {
		v.Tests = append(v.Tests, Check{Name: n, Status: "pass", Reference: "root-evidence"})
	}
	for _, n := range c.Plan.FinalChecks.Surfaces {
		v.Surfaces = append(v.Surfaces, Check{Name: n, Status: "pass", Reference: "root-evidence"})
		v.Captures = append(v.Captures, Capture{Surface: n, Head: c.ExpectedHead, Reference: "https://linear.app/example/document/captures"})
	}
	return v
}

func saveReport(f *fixture, v *Verification) {
	f.t.Helper()
	r := f.request("verification-save")
	r.Verification = v
	f.apply(r)
}

func startDelivery(f *fixture) {
	f.t.Helper()
	f.apply(f.request("verification-complete"))
	f.run(f.cwd, "remote", "add", "origin", "git@github.com:example/project.git")
	r := f.request("delivery-start")
	r.BaseBranch = "main"
	f.apply(r)
}

func prObservation(f *fixture) *PullRequest {
	return &PullRequest{Number: 7, URL: "https://github.com/example/project/pull/7", State: "OPEN", HeadRefName: "feature", HeadRefOID: f.status().ExpectedHead, BaseRefName: "main", StatusCheckRollup: []json.RawMessage{}}
}

func TestFullCampaignScenarios(t *testing.T) {
	for _, tc := range []struct {
		name           string
		visual, parent bool
	}{{"standalone", false, false}, {"two-children", false, true}, {"visual-parent", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := finalFixture(t, tc.visual, tc.parent)
			v := finalReport(f)
			v.Findings = []Finding{{ID: "manual", Brief: "Check production data", Impact: "Real data cannot be simulated", Class: "manual", Status: "deferred", Trigger: "after deployment"}}
			saveReport(f, v)
			startDelivery(f)
			p := prObservation(f)
			r := f.request("delivery-observe")
			r.PR = p
			f.apply(r)
			f.apply(f.request("delivery-check"))
			f.reject(f.request("delivery-complete"), ErrInvalid)
			suite, err := Suite(f.status())
			if err != nil || len(suite.Entries) != 1 {
				t.Fatal(suite, err)
			}
			p.State = "MERGED"
			p.MergeCommit = &struct {
				OID string `json:"oid"`
			}{OID: f.status().ExpectedHead}
			r = f.request("delivery-observe")
			r.PR = p
			f.apply(r)
			f.apply(f.request("delivery-complete"))
			r = f.request("suite-publish")
			r.Reference = "linear-suite-document"
			f.apply(r)
			c := f.status()
			if c.State != "delivered" || c.SuiteReference == "" {
				t.Fatal("missing delivery/suite")
			}
			linked := false
			for _, e := range c.Events {
				if e.Type == "atelier-next:pr-linked" {
					linked = true
					if e.Data["number"] != float64(7) || e.Data["url"] != p.URL || e.Data["head"] != p.HeadRefOID {
						t.Fatal("PR event cannot project the link", e)
					}
				}
			}
			if !linked {
				t.Fatal("missing PR event")
			}
		})
	}
}

func TestFinalGateRejectsIncompleteEvidence(t *testing.T) {
	for _, tc := range []string{"criterion", "test", "surface", "capture", "review", "finding", "stale-capture", "stale-head"} {
		t.Run(tc, func(t *testing.T) {
			f := finalFixture(t, true, false)
			v := finalReport(f)
			switch tc {
			case "criterion":
				v.Criteria = nil
			case "test":
				v.Tests[0].Status = "fail"
			case "surface":
				v.Surfaces = nil
			case "capture":
				v.Captures = nil
			case "review":
				v.Review.Independent = false
			case "finding":
				v.Findings = []Finding{{ID: "fix", Brief: "Bug", Impact: "User cannot proceed", Class: "necessary", Status: "open"}}
			case "stale-capture":
				v.Captures[0].Head = f.status().Base
			case "stale-head":
				v.Head = f.status().Base
			}
			r := f.request("verification-save")
			r.Verification = v
			if tc == "stale-head" {
				f.reject(r, ErrInvalid)
				return
			}
			f.apply(r)
			f.reject(f.request("verification-complete"), ErrInvalid)
			r = f.request("delivery-start")
			r.BaseBranch = "main"
			f.reject(r, ErrInvalid)
		})
	}
}

func TestRootRepairInvalidatesFinalCaptureAndCannotDropFinding(t *testing.T) {
	f := finalFixture(t, true, false)
	v := finalReport(f)
	v.Findings = []Finding{{ID: "bug", Brief: "Incorrect UI", Impact: "User sees wrong value", Class: "necessary", Status: "open"}}
	saveReport(f, v)
	dropped := finalReport(f)
	r := f.request("verification-save")
	r.Verification = dropped
	f.reject(r, ErrInvalid)
	r = f.request("repair-prepare")
	r.Repair = &RepairRequest{FindingIDs: []string{"bug"}, Evidence: *f.evidence()}
	p := f.apply(r)
	f.commit(p)
	r = f.request("repair-finish")
	r.IntegrationID = p.IntegrationID
	f.apply(r)
	f.reject(f.request("verification-complete"), ErrInvalid)
	oldCaptures := v.Captures
	v = finalReport(f)
	v.Findings = []Finding{{ID: "bug", Brief: "Incorrect UI", Impact: "User sees wrong value", Class: "necessary", Status: "resolved", Reference: "targeted-test"}}
	v.Captures = oldCaptures
	saveReport(f, v)
	f.reject(f.request("verification-complete"), ErrInvalid)
	v.Captures = finalReport(f).Captures
	saveReport(f, v)
	f.apply(f.request("verification-complete"))
	r = f.request("verification-reopen")
	r.Reason = "new discovery before PR"
	f.apply(r)
	if f.status().State != "verifying" {
		t.Fatal("reopen failed")
	}
}

func TestPRIdentityAndCI(t *testing.T) {
	f := finalFixture(t, false, false)
	saveReport(f, finalReport(f))
	startDelivery(f)
	p := prObservation(f)
	r := f.request("delivery-observe")
	r.PR = p
	p.HeadRefOID = f.status().Base
	f.reject(r, ErrInvalid)
	p.HeadRefOID = f.status().ExpectedHead
	p.URL = "https://github.com/other/project/pull/7"
	f.reject(r, ErrInvalid)
	p.URL = "https://github.com/example/project/pull/7"
	p.StatusCheckRollup = nil
	f.reject(r, ErrInvalid)
	p.StatusCheckRollup = []json.RawMessage{json.RawMessage(`{"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE"}`)}
	f.apply(r)
	f.reject(f.request("delivery-check"), ErrInvalid)
	p.Number = 8
	p.URL = "https://github.com/example/project/pull/8"
	r = f.request("delivery-observe")
	r.PR = p
	f.reject(r, ErrConflict)
	for _, raw := range []string{`{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":"SUCCESS"}`, `{"__typename":"Unknown","status":"COMPLETED","conclusion":"SUCCESS"}`, `{"__typename":"StatusContext","state":"PENDING"}`} {
		if ciGreen(&PullRequest{StatusCheckRollup: []json.RawMessage{json.RawMessage(raw)}}) {
			t.Fatal("ambiguous CI passed")
		}
	}
	if !ciGreen(&PullRequest{StatusCheckRollup: []json.RawMessage{json.RawMessage(`{"__typename":"StatusContext","state":"SUCCESS"}`)}}) {
		t.Fatal("green status failed")
	}
}

func TestLegacyPlanCannotBypassFinalGate(t *testing.T) {
	f := setup(t, "standalone")
	f.plan(rootTicket)
	f.open(rootTicket)
	p := f.prepare()
	f.commit(p)
	f.finish(p)
	f.reject(f.request("verification-complete"), ErrInvalid)
}
