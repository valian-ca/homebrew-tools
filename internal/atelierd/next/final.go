package next

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

type FinalChecks struct {
	Criteria       []string `json:"criteria"`
	Tests          []string `json:"tests"`
	Surfaces       []string `json:"surfaces"`
	Visual         bool     `json:"visual"`
	NoVisualReason string   `json:"noVisualReason,omitempty"`
}

type UserTrial struct {
	Head                string `json:"head"`
	Response            string `json:"response"`
	Reference           string `json:"reference"`
	Readiness           string `json:"readiness,omitempty"`
	NotApplicableReason string `json:"notApplicableReason,omitempty"`
}

type PostDeployment struct {
	Reason    string `json:"reason"`
	Trigger   string `json:"trigger"`
	FindingID string `json:"findingId"`
}

type Check struct {
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	Reference      string          `json:"reference"`
	PostDeployment *PostDeployment `json:"postDeployment,omitempty"`
}

type Finding struct {
	ID                string `json:"id"`
	Brief             string `json:"brief"`
	Impact            string `json:"impact"`
	Class             string `json:"class"`
	Status            string `json:"status"`
	Reference         string `json:"reference,omitempty"`
	DecisionReference string `json:"decisionReference,omitempty"`
	Trigger           string `json:"trigger,omitempty"`
}

type Capture struct {
	Surface   string `json:"surface"`
	Head      string `json:"head"`
	Reference string `json:"reference"`
}

type Verification struct {
	Head         string         `json:"head"`
	PlanRevision int            `json:"planRevision"`
	Reference    string         `json:"reference"`
	Criteria     []Check        `json:"criteria"`
	Tests        []Check        `json:"tests"`
	Surfaces     []Check        `json:"surfaces"`
	Review       ReviewEvidence `json:"review"`
	Captures     []Capture      `json:"captures"`
	Findings     []Finding      `json:"findings"`
}

type RepairRequest struct {
	Kind       string   `json:"kind,omitempty"`
	FindingIDs []string `json:"findingIds"`
	Evidence   Evidence `json:"evidence"`
}

type Repair struct {
	Kind        string      `json:"kind,omitempty"`
	FindingIDs  []string    `json:"findingIds"`
	Integration Integration `json:"integration"`
}

// PullRequest accepts the unmodified output of the documented gh pr view --json call.
type PullRequest struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	HeadRefOID  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	IsDraft     bool   `json:"isDraft"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	StatusCheckRollup []json.RawMessage `json:"statusCheckRollup"`
}

type Delivery struct {
	Repository string       `json:"repository"`
	BaseBranch string       `json:"baseBranch"`
	PR         *PullRequest `json:"pr,omitempty"`
}

type SuiteView struct {
	CampaignID string    `json:"campaignId"`
	State      string    `json:"state"`
	Reference  string    `json:"reference,omitempty"`
	Entries    []Finding `json:"entries"`
}

func Suite(c *Campaign) (SuiteView, error) {
	result := SuiteView{CampaignID: c.ID, State: c.State, Reference: c.SuiteReference, Entries: []Finding{}}
	if c.State != "pr-open" && c.State != "delivered" {
		return result, ErrInvalid
	}
	if err := validateVerification(c); err != nil {
		return result, err
	}
	for _, f := range c.Verification.Findings {
		if f.Status == "deferred" {
			result.Entries = append(result.Entries, f)
		}
	}
	return result, nil
}

func validateTrial(c *Campaign) error {
	t := c.Trial
	if t == nil || c.Plan == nil || c.Plan.FinalChecks == nil || len(c.Contributions) == 0 || !textOK(t.Reference) {
		return fmt.Errorf("%w: record the user trial before verification", ErrInvalid)
	}
	last := c.Contributions[len(c.Contributions)-1].Integration
	known := last != nil && t.Head == last.Commit && last.Commit != ""
	for _, repair := range c.Repairs {
		if repair.Integration.Commit != "" && t.Head == repair.Integration.Commit {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("%w: trial must attest a recorded integrated HEAD", ErrInvalid)
	}
	switch t.Response {
	case "tested", "declined":
		if !textOK(t.Readiness) || t.NotApplicableReason != "" {
			return fmt.Errorf("%w: ready stack and actual user response required", ErrInvalid)
		}
	case "not-applicable":
		if c.Plan.FinalChecks.Visual || len(c.Plan.FinalChecks.Surfaces) != 0 || !textOK(t.NotApplicableReason) || t.Readiness != "" {
			return fmt.Errorf("%w: only a static campaign may omit the app trial", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: user response must be tested or declined, not silence", ErrInvalid)
	}
	return nil
}

func recordTrial(ctx context.Context, c *Campaign, trial *UserTrial) error {
	if c.State != "awaiting-trial" || trial == nil || trial.Head != c.ExpectedHead {
		return ErrInvalid
	}
	if err := cleanCheckout(ctx, c.Checkout.Worktree); err != nil {
		return err
	}
	c.Trial = trial
	if err := validateTrial(c); err != nil {
		return err
	}
	c.State = "verifying"
	return nil
}

func finalState(state string) bool {
	switch state {
	case "awaiting-trial", "verifying", "verified", "delivering", "pr-open", "delivered":
		return true
	}
	return false
}

func namesValid(names []string, required bool) bool {
	if len(names) > 128 || (required && len(names) == 0) {
		return false
	}
	seen := map[string]bool{}
	for _, n := range names {
		if !textOK(n) || seen[n] {
			return false
		}
		seen[n] = true
	}
	return true
}

func validateFinalChecks(f *FinalChecks) error {
	if f == nil || !namesValid(f.Criteria, true) || !namesValid(f.Tests, true) || !namesValid(f.Surfaces, f.Visual) || (!f.Visual && !textOK(f.NoVisualReason)) {
		return fmt.Errorf("%w: final checks require criteria, tests, surfaces and explicit visual applicability", ErrInvalid)
	}
	return nil
}

func checkCoverage(required []string, actual []Check, findings []Finding, allowPostDeployment bool) error {
	if len(actual) != len(required) {
		return fmt.Errorf("%w: coverage must match planned checks", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, c := range actual {
		if !textOK(c.Reference) || seen[c.Name] {
			return fmt.Errorf("%w: check missing evidence or duplicated", ErrInvalid)
		}
		switch c.Status {
		case "pass":
			if c.PostDeployment != nil {
				return fmt.Errorf("%w: a post-deployment check is not a pass", ErrInvalid)
			}
		case "post-deployment":
			p := c.PostDeployment
			if !allowPostDeployment || p == nil || !textOK(p.Reason) || !textOK(p.Trigger) {
				return fmt.Errorf("%w: only deployment-only criteria with justification may remain unverified", ErrInvalid)
			}
			linked := false
			for _, f := range findings {
				if f.ID == p.FindingID && f.Class == "manual" && f.Status == "deferred" && f.Trigger == p.Trigger && f.Reference == c.Reference {
					linked = true
				}
			}
			if !linked {
				return fmt.Errorf("%w: deployment-only criterion needs a matching manual suite entry", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: check not passing or reserved for post-deployment", ErrInvalid)
		}
		seen[c.Name] = true
	}
	for _, n := range required {
		if !seen[n] {
			return fmt.Errorf("%w: missing check %s", ErrInvalid, n)
		}
	}
	return nil
}

func validateFindings(items []Finding, complete bool) error {
	if len(items) > 256 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, f := range items {
		if !keyPattern.MatchString(f.ID) || seen[f.ID] || !textOK(f.Brief) || !textOK(f.Impact) {
			return fmt.Errorf("%w: finding identity, brief and user impact required", ErrInvalid)
		}
		seen[f.ID] = true
		switch f.Class {
		case "necessary", "improvement", "scope", "future", "manual":
		default:
			return ErrInvalid
		}
		switch f.Status {
		case "open", "accepted", "resolved", "deferred":
		default:
			return ErrInvalid
		}
		if f.Status == "resolved" && !textOK(f.Reference) {
			return fmt.Errorf("%w: resolution evidence required", ErrInvalid)
		}
		if f.Status == "deferred" && (f.Class == "necessary" || !textOK(f.Trigger)) {
			return fmt.Errorf("%w: necessary correction cannot be deferred; suite entries need a trigger", ErrInvalid)
		}
		if (f.Class == "scope" || (f.Class == "improvement" && (f.Status == "accepted" || f.Status == "deferred" || f.Status == "resolved"))) && !textOK(f.DecisionReference) {
			return fmt.Errorf("%w: recorded human decision required", ErrInvalid)
		}
		if complete {
			if f.Status == "open" || f.Status == "accepted" {
				return fmt.Errorf("%w: unresolved finding %s", ErrInvalid, f.ID)
			}
			if (f.Class == "scope" && f.Status != "resolved") || ((f.Class == "future" || f.Class == "manual") && f.Status != "deferred") {
				return fmt.Errorf("%w: invalid final finding disposition", ErrInvalid)
			}
		}
	}
	return nil
}

func validateVerification(c *Campaign) error {
	v := c.Verification
	if c.Plan == nil {
		return ErrInvalid
	}
	if err := validateFinalChecks(c.Plan.FinalChecks); err != nil {
		return err
	}
	if err := validateTrial(c); err != nil {
		return err
	}
	if v == nil || v.Head != c.ExpectedHead || v.PlanRevision != c.Plan.Revision || !textOK(v.Reference) || !v.Review.Independent || !textOK(v.Review.Reference) {
		return fmt.Errorf("%w: current HEAD/plan and independent final review required", ErrInvalid)
	}
	for _, child := range c.Contributions {
		if child.State != "integrated" || child.Linear.Pending {
			return fmt.Errorf("%w: all contributions and Linear synchronizations must be complete", ErrInvalid)
		}
	}
	for _, r := range c.Repairs {
		if r.Integration.Commit == "" {
			return ErrReconcile
		}
	}
	f := c.Plan.FinalChecks
	for index, pair := range []struct {
		required []string
		actual   []Check
	}{{f.Criteria, v.Criteria}, {f.Tests, v.Tests}, {f.Surfaces, v.Surfaces}} {
		if err := checkCoverage(pair.required, pair.actual, v.Findings, index == 0); err != nil {
			return err
		}
	}
	if err := validateFindings(v.Findings, true); err != nil {
		return err
	}
	if len(v.Captures) > 128 {
		return ErrInvalid
	}
	captured := map[string]bool{}
	for _, capture := range v.Captures {
		if capture.Head != v.Head || !httpsReference(capture.Reference) {
			return fmt.Errorf("%w: current published captures required", ErrInvalid)
		}
		captured[capture.Surface] = true
	}
	if f.Visual {
		for _, surface := range f.Surfaces {
			if !captured[surface] {
				return fmt.Errorf("%w: missing final capture for %s", ErrInvalid, surface)
			}
		}
	}
	return nil
}

func httpsReference(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func saveVerification(c *Campaign, v *Verification) error {
	if c.State != "verifying" || v == nil || v.Head != c.ExpectedHead || c.Plan == nil || v.PlanRevision != c.Plan.Revision || !textOK(v.Reference) {
		return ErrInvalid
	}
	if err := validateFindings(v.Findings, false); err != nil {
		return err
	}
	if c.Verification != nil {
		if v.Reference != c.Verification.Reference {
			return fmt.Errorf("%w: extend the campaign's verification document; do not replace it", ErrInvalid)
		}
		next := map[string]Finding{}
		for _, f := range v.Findings {
			next[f.ID] = f
		}
		for _, old := range c.Verification.Findings {
			f, ok := next[old.ID]
			if !ok {
				return fmt.Errorf("%w: findings cannot silently disappear", ErrInvalid)
			}
			if old.Class != f.Class && !textOK(f.DecisionReference) {
				return fmt.Errorf("%w: reclassification requires a human decision", ErrInvalid)
			}
		}
	}
	c.Verification = v
	c.SuiteReference = ""
	return nil
}

func repairPrepare(ctx context.Context, c *Campaign, r *RepairRequest, out *Receipt) error {
	if c.State != "verifying" || r == nil || !namesValid(r.FindingIDs, true) || c.Verification == nil {
		return ErrInvalid
	}
	ciRounds := 0
	for _, prior := range c.Repairs {
		if prior.Integration.Commit == "" {
			return ErrReconcile
		}
		if prior.Kind == "ci" {
			ciRounds++
		}
	}
	if r.Kind != "" && r.Kind != "ci" {
		return fmt.Errorf("%w: repair kind must be empty (QA) or ci", ErrInvalid)
	}
	if r.Kind == "ci" && (ciRounds >= 3 || c.Delivery == nil || c.Delivery.PR == nil || c.Delivery.PR.State != "OPEN") {
		return fmt.Errorf("%w: CI plumbing repairs require an open campaign PR and at most three rounds", ErrInvalid)
	}
	for _, id := range r.FindingIDs {
		found := false
		for _, f := range c.Verification.Findings {
			if f.ID == id && ((f.Class == "necessary" && (f.Status == "open" || f.Status == "accepted")) || (f.Class == "improvement" && f.Status == "accepted")) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%w: repair must name open necessary or accepted improvement findings", ErrInvalid)
		}
	}
	names := []string{}
	for _, test := range r.Evidence.Tests {
		names = append(names, test.Name)
	}
	if !namesValid(names, true) {
		return ErrInvalid
	}
	if err := validateEvidence(&r.Evidence, ContributionSpec{RequiredTests: names}); err != nil {
		return err
	}
	tree, err := stagedTree(ctx, c.Checkout.Worktree)
	if err != nil {
		return err
	}
	if tree != r.Evidence.Tree {
		return ErrInvalid
	}
	id := ulid.New()
	i := Integration{ID: id, Parent: c.ExpectedHead, Tree: tree, Evidence: r.Evidence}
	c.Repairs = append(c.Repairs, Repair{Kind: r.Kind, FindingIDs: r.FindingIDs, Integration: i})
	out.IntegrationID = id
	return nil
}

func repairFinish(ctx context.Context, c *Campaign, id, head string, out *Receipt) error {
	if c.State != "verifying" {
		return ErrInvalid
	}
	for n := range c.Repairs {
		i := &c.Repairs[n].Integration
		if i.ID != id {
			continue
		}
		if i.Commit != "" {
			if head != c.ExpectedHead {
				return ErrReconcile
			}
			out.Commit, out.IntegrationID = i.Commit, id
			return nil
		}
		if err := proveIntegration(ctx, c.Checkout.Worktree, head, i); err != nil {
			return err
		}
		i.Commit, c.ExpectedHead = head, head
		out.Commit, out.IntegrationID = head, id
		c.SuiteReference = ""
		return nil
	}
	return ErrInvalid
}

func githubRepository(raw string) (string, error) {
	if strings.HasPrefix(raw, "git@github.com:") {
		raw = "https://github.com/" + strings.TrimPrefix(raw, "git@github.com:")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: GitHub origin required", ErrInvalid)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !keyPattern.MatchString(parts[0]) || !keyPattern.MatchString(parts[1]) {
		return "", ErrInvalid
	}
	return "https://github.com/" + path, nil
}

func deliveryStart(ctx context.Context, c *Campaign, base string) error {
	if c.State != "verified" {
		return fmt.Errorf("%w: branch is not verified", ErrInvalid)
	}
	if err := validateVerification(c); err != nil {
		return err
	}
	if err := cleanCheckout(ctx, c.Checkout.Worktree); err != nil {
		return err
	}
	if _, err := git(ctx, c.Checkout.Worktree, "check-ref-format", "--branch", base); err != nil {
		return ErrInvalid
	}
	if "refs/heads/"+base == c.Checkout.Branch {
		return ErrInvalid
	}
	remote, err := git(ctx, c.Checkout.Worktree, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	repository, err := githubRepository(remote)
	if err != nil {
		return err
	}
	if c.Delivery != nil && (c.Delivery.Repository != repository || c.Delivery.BaseBranch != base) {
		return ErrConflict
	}
	if c.Delivery == nil {
		c.Delivery = &Delivery{Repository: repository, BaseBranch: base}
	}
	c.State = "delivering"
	return nil
}

func observePR(c *Campaign, p *PullRequest) error {
	if (c.State != "delivering" && c.State != "pr-open") || c.Delivery == nil || p == nil {
		return ErrInvalid
	}
	if err := validateVerification(c); err != nil {
		return err
	}
	d := c.Delivery
	if p.Number < 1 || p.URL != d.Repository+"/pull/"+strconv.Itoa(p.Number) || p.HeadRefName != strings.TrimPrefix(c.Checkout.Branch, "refs/heads/") || p.HeadRefOID != c.ExpectedHead || p.BaseRefName != d.BaseBranch || p.IsDraft || (p.State != "OPEN" && p.State != "MERGED") {
		return fmt.Errorf("%w: PR must match campaign repository, branch, base and verified SHA", ErrInvalid)
	}
	if d.PR != nil && (d.PR.Number != p.Number || (d.PR.State == "MERGED" && p.State != "MERGED")) {
		return fmt.Errorf("%w: a campaign has only one PR and cannot undo an observed merge", ErrConflict)
	}
	if p.StatusCheckRollup == nil {
		return fmt.Errorf("%w: CI rollup must be supplied, even when empty", ErrInvalid)
	}
	d.PR = p
	c.State = "pr-open"
	return nil
}

func ciGreen(p *PullRequest) bool {
	if p == nil || p.StatusCheckRollup == nil {
		return false
	}
	for _, raw := range p.StatusCheckRollup {
		var c struct {
			Type       string `json:"__typename"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		}
		if json.Unmarshal(raw, &c) != nil {
			return false
		}
		switch c.Type {
		case "CheckRun":
			if c.Status != "COMPLETED" || (c.Conclusion != "SUCCESS" && c.Conclusion != "NEUTRAL" && c.Conclusion != "SKIPPED") {
				return false
			}
		case "StatusContext":
			if c.State != "SUCCESS" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func finalMutation(ctx context.Context, c *Campaign, r Request, head string, out *Receipt) (string, string, error) {
	event, skill := "atelier-next:campaign-updated", "verification"
	switch r.Action {
	case "trial-record":
		return event, "realisation", recordTrial(ctx, c, r.Trial)
	case "verification-save":
		return event, skill, saveVerification(c, r.Verification)
	case "verification-complete":
		if c.State != "verifying" {
			return event, skill, ErrInvalid
		}
		if err := cleanCheckout(ctx, c.Checkout.Worktree); err != nil {
			return event, skill, err
		}
		if err := validateVerification(c); err != nil {
			return event, skill, err
		}
		c.State = "verified"
		event = "atelier-next:verification-completed"
	case "verification-reopen":
		if c.Delivery != nil && c.Delivery.PR != nil && c.Delivery.PR.State == "MERGED" {
			return event, skill, fmt.Errorf("%w: an observed merge cannot return to repair", ErrConflict)
		}
		if c.State != "verified" && c.State != "delivering" && c.State != "pr-open" {
			return event, skill, ErrInvalid
		}
		if !textOK(r.Reason) {
			return event, skill, ErrInvalid
		}
		c.State = "verifying"
		c.SuiteReference = ""
	case "repair-prepare":
		return event, skill, repairPrepare(ctx, c, r.Repair, out)
	case "repair-finish":
		return event, skill, repairFinish(ctx, c, r.IntegrationID, head, out)
	case "repair-cancel":
		if c.State != "verifying" || len(c.Repairs) == 0 {
			return event, skill, ErrInvalid
		}
		last := c.Repairs[len(c.Repairs)-1]
		if last.Integration.ID != r.IntegrationID || last.Integration.Commit != "" {
			return event, skill, ErrInvalid
		}
		c.Repairs = c.Repairs[:len(c.Repairs)-1]
	case "delivery-start":
		return event, "livraison", deliveryStart(ctx, c, r.BaseBranch)
	case "delivery-observe":
		return "atelier-next:pr-linked", "livraison", observePR(c, r.PR)
	case "delivery-check":
		skill = "livraison"
		if c.State != "pr-open" || c.Delivery == nil || !ciGreen(c.Delivery.PR) {
			return event, skill, ErrInvalid
		}
		if err := cleanCheckout(ctx, c.Checkout.Worktree); err != nil {
			return event, skill, err
		}
		return event, skill, validateVerification(c)
	case "delivery-complete":
		skill = "livraison"
		if err := cleanCheckout(ctx, c.Checkout.Worktree); err != nil {
			return event, skill, err
		}
		if c.State != "pr-open" || c.Delivery == nil || !ciGreen(c.Delivery.PR) {
			return event, skill, ErrInvalid
		}
		p := c.Delivery.PR
		if p.State != "MERGED" || p.MergeCommit == nil || !hashPattern.MatchString(p.MergeCommit.OID) {
			return event, skill, fmt.Errorf("%w: confirmed merge required", ErrInvalid)
		}
		if err := validateVerification(c); err != nil {
			return event, skill, err
		}
		c.State = "delivered"
		event = "atelier-next:delivery-completed"
	case "suite-publish":
		skill = "suite"
		if (c.State != "pr-open" && c.State != "delivered") || !textOK(r.Reference) {
			return event, skill, ErrInvalid
		}
		if err := validateVerification(c); err != nil {
			return event, skill, err
		}
		c.SuiteReference = r.Reference
		event = "atelier-next:suite-published"
	default:
		return event, skill, fmt.Errorf("%w: unknown action %q", ErrInvalid, r.Action)
	}
	return event, skill, nil
}
