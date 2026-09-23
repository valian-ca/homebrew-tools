package next

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTrialGateAndRecovery(t *testing.T) {
	f := setup(t, "standalone")
	r := f.request("plan")
	r.Plan = makePlan(rootTicket)
	r.Plan.FinalChecks = &FinalChecks{Criteria: []string{"AC1"}, Tests: []string{"root-test"}, Surfaces: []string{"web"}, Visual: true}
	f.apply(r)
	f.open(rootTicket)
	p := f.prepare()
	f.commit(p)
	f.finish(p)
	r = f.request("verification-save")
	r.Verification = finalReport(f)
	f.reject(r, ErrInvalid)
	for _, trial := range []UserTrial{
		{Head: f.status().ExpectedHead, Response: "tested", Reference: "reply"},
		{Head: f.status().ExpectedHead, Response: "waiting", Reference: "reply", Readiness: "logs"},
		{Head: f.status().Base, Response: "declined", Reference: "reply", Readiness: "logs"},
		{Head: f.status().ExpectedHead, Response: "not-applicable", Reference: "plan", NotApplicableReason: "skip UI"},
	} {
		r = f.request("trial-record")
		r.Trial = &trial
		f.reject(r, ErrInvalid)
	}
	r = f.request("stop")
	r.Reason = "user needs time"
	f.apply(r)
	r = f.request("resume")
	r.Reason = "user returned"
	f.apply(r)
	if f.status().State != "awaiting-trial" {
		t.Fatal("resume skipped the trial")
	}
	r = f.request("trial-record")
	r.Trial = &UserTrial{Head: f.status().ExpectedHead, Response: "tested", Reference: "actual-reply", Readiness: "ready-log"}
	receipt := f.apply(r)
	fresh, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	f.s = fresh
	if f.apply(r) != receipt || f.status().State != "verifying" {
		t.Fatal("trial response replay failed")
	}
	saveReport(f, finalReport(f))
	f.apply(f.request("verification-complete"))
}

func deploymentReport(f *fixture) *Verification {
	v := finalReport(f)
	v.Criteria[0].Status = "post-deployment"
	v.Criteria[0].PostDeployment = &PostDeployment{Reason: "Requires the real deployed domain and production callback", Trigger: "after deployment on the real domain", FindingID: "production"}
	v.Findings = []Finding{{ID: "production", Brief: "Exercise the deployed callback", Impact: "The real domain binding remains unverified", Class: "manual", Status: "deferred", Trigger: v.Criteria[0].PostDeployment.Trigger, Reference: v.Criteria[0].Reference}}
	return v
}

func TestDeploymentOnlyCriterionIsNotAPassButCanShip(t *testing.T) {
	f := finalFixture(t, true, false)
	saveReport(f, deploymentReport(f))
	startDelivery(f)
	r := f.request("delivery-observe")
	r.PR = prObservation(f)
	f.apply(r)
	f.apply(f.request("delivery-check"))
	suite, err := Suite(f.status())
	if err != nil || len(suite.Entries) != 1 || f.status().Verification.Criteria[0].Status != "post-deployment" {
		t.Fatal(suite, err)
	}
}

func TestDeploymentExceptionCannotHideMissingTestsOrBugs(t *testing.T) {
	for _, scenario := range []string{"reason", "trigger", "link", "reference", "class", "pass", "test", "surface", "fail", "known-bug", "capture"} {
		t.Run(scenario, func(t *testing.T) {
			f := finalFixture(t, true, false)
			v := deploymentReport(f)
			switch scenario {
			case "reason":
				v.Criteria[0].PostDeployment.Reason = ""
			case "trigger":
				v.Criteria[0].PostDeployment.Trigger = ""
			case "link":
				v.Criteria[0].PostDeployment.FindingID = "absent"
			case "reference":
				v.Findings[0].Reference = "unrelated"
			case "class":
				v.Findings[0].Class = "future"
			case "pass":
				v.Criteria[0].Status = "pass"
			case "test":
				v.Tests[0] = v.Criteria[0]
				v.Tests[0].Name = "root-test"
			case "surface":
				v.Surfaces[0] = v.Criteria[0]
				v.Surfaces[0].Name = "web"
			case "fail":
				v.Criteria[0].Status = "fail"
			case "known-bug":
				v.Findings = append(v.Findings, Finding{ID: "bug", Brief: "Known defect", Impact: "User cannot continue", Class: "necessary", Status: "open"})
			case "capture":
				v.Captures = nil
			}
			saveReport(f, v)
			f.reject(f.request("verification-complete"), ErrInvalid)
		})
	}
}

func TestVerificationDocumentIsUnique(t *testing.T) {
	f := finalFixture(t, false, false)
	saveReport(f, finalReport(f))
	v := finalReport(f)
	v.Reference = "replacement-document"
	r := f.request("verification-save")
	r.Verification = v
	f.reject(r, ErrInvalid)
}

func TestLegacySchemaIsNotSilentlyMigrated(t *testing.T) {
	f := setup(t, "standalone")
	c := f.status()
	c.SchemaVersion = 1
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.s.path(f.id), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Status(f.ctx, f.id); err == nil {
		t.Fatal("accepted old schema")
	}
}

func TestCIRepairCapSurvivesReentry(t *testing.T) {
	f := finalFixture(t, false, false)
	saveReport(f, finalReport(f))
	startDelivery(f)
	r := f.request("delivery-observe")
	r.PR = prObservation(f)
	f.apply(r)
	for round := 0; round < 3; round++ {
		r = f.request("verification-reopen")
		r.Reason = "CI plumbing"
		f.apply(r)
		v := finalReport(f)
		v.Findings = []Finding{{ID: "ci", Brief: "CI configuration", Impact: "Checks cannot run", Class: "necessary", Status: "open"}}
		saveReport(f, v)
		r = f.request("repair-prepare")
		r.Repair = &RepairRequest{Kind: "ci", FindingIDs: []string{"ci"}, Evidence: *f.evidence()}
		p := f.apply(r)
		f.commit(p)
		r = f.request("repair-finish")
		r.IntegrationID = p.IntegrationID
		f.apply(r)
		v = finalReport(f)
		v.Findings = []Finding{{ID: "ci", Brief: "CI configuration", Impact: "Checks cannot run", Class: "necessary", Status: "resolved", Reference: "retest"}}
		saveReport(f, v)
		f.apply(f.request("verification-complete"))
		r = f.request("delivery-start")
		r.BaseBranch = "main"
		f.apply(r)
		r = f.request("delivery-observe")
		r.PR = prObservation(f)
		f.apply(r)
	}
	fresh, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	f.s = fresh
	r = f.request("verification-reopen")
	r.Reason = "fourth CI round"
	f.apply(r)
	v := finalReport(f)
	v.Findings = []Finding{{ID: "ci", Brief: "CI configuration", Impact: "Checks cannot run", Class: "necessary", Status: "open"}}
	saveReport(f, v)
	r = f.request("repair-prepare")
	r.Repair = &RepairRequest{Kind: "ci", FindingIDs: []string{"ci"}, Evidence: *f.evidence()}
	f.reject(r, ErrInvalid)
}

func TestFinalChecksReplanRequiresNewTrial(t *testing.T) {
	f := finalFixture(t, false, false)
	r := f.request("block")
	r.Class, r.Reason = "scope-transversal", "new root promise"
	f.apply(r)
	r = f.request("plan")
	r.Plan = f.status().Plan
	r.Plan.Revision++
	r.Plan.DecisionReference = "decision"
	r.Plan.FinalChecks.Criteria = append(r.Plan.FinalChecks.Criteria, "AC2")
	f.apply(r)
	r = f.request("resume")
	r.Reason, r.DecisionReference = "approved", "decision"
	f.apply(r)
	if f.status().State != "awaiting-trial" || f.status().Trial != nil {
		t.Fatal("replan bypassed new trial")
	}
	f.reject(f.request("verification-complete"), ErrInvalid)
}

func TestRepairRecoveryWithoutMessageMetadata(t *testing.T) {
	f := finalFixture(t, true, false)
	v := finalReport(f)
	v.Findings = []Finding{{ID: "bug", Brief: "Wrong value", Impact: "Incorrect amount", Class: "necessary", Status: "open"}}
	saveReport(f, v)
	r := f.request("repair-prepare")
	r.Repair = &RepairRequest{FindingIDs: []string{"bug"}, Evidence: *f.evidence()}
	p := f.apply(r)
	sha := f.commit(p)
	fresh, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	f.s = fresh
	r = f.request("repair-finish")
	r.IntegrationID = p.IntegrationID
	receipt := f.apply(r)
	if receipt.Commit != sha || f.apply(r) != receipt {
		t.Fatal("repair recovery failed")
	}
	f.reject(f.request("verification-complete"), ErrInvalid)
	r = f.request("block")
	r.Class, r.Reason = "scope-transversal", "new final check after repair"
	f.apply(r)
	r = f.request("plan")
	r.Plan = f.status().Plan
	r.Plan.Revision++
	r.Plan.DecisionReference = "decision"
	r.Plan.FinalChecks.Criteria = append(r.Plan.FinalChecks.Criteria, "AC2")
	f.apply(r)
	r = f.request("resume")
	r.Reason, r.DecisionReference = "approved", "decision"
	f.apply(r)
	r = f.request("trial-record")
	r.Trial = &UserTrial{Head: sha, Response: "tested", Reference: "new-trial-response", Readiness: "ready-log"}
	f.apply(r)
	if f.status().Trial.Head != sha || f.status().State != "verifying" {
		t.Fatal("cannot repeat trial at repaired HEAD after replan")
	}
}
