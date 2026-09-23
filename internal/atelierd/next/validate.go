package next

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
)

func textOK(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 4096 && !strings.ContainsRune(value, 0)
}

func ticketOK(t Ticket) bool {
	return uuidPattern.MatchString(t.ID) && ticketPattern.MatchString(t.Identifier)
}

func validatePlan(p *Plan, root Ticket, mode string) error {
	if p == nil || !textOK(p.Reference) || p.Revision < 1 || len(p.Contributions) < 1 || len(p.Contributions) > MaxContributions {
		return fmt.Errorf("%w: plan requires reference, positive revision and 1–128 contributions", ErrInvalid)
	}
	if mode == "standalone" && (len(p.Contributions) != 1 || p.Contributions[0].Ticket != root) {
		return fmt.Errorf("%w: standalone contribution must be the root", ErrInvalid)
	}
	if p.FinalChecks != nil {
		if err := validateFinalChecks(p.FinalChecks); err != nil {
			return err
		}
	}
	seen, ids := map[string]bool{}, map[string]bool{}
	for _, c := range p.Contributions {
		if !ticketOK(c.Ticket) || !textOK(c.Contract) || seen[c.Ticket.Identifier] || ids[c.Ticket.ID] {
			return fmt.Errorf("%w: contributions require unique ticket UUIDs/identifiers and local contracts", ErrInvalid)
		}
		if mode == "parent" && (c.Ticket.ID == root.ID || c.Ticket.Identifier == root.Identifier) {
			return fmt.Errorf("%w: parent contributions must be children, not the root", ErrInvalid)
		}
		deps := map[string]bool{}
		for _, dep := range c.DependsOn {
			if !seen[dep] || deps[dep] {
				return fmt.Errorf("%w: dependency must occur once and precede its contribution", ErrInvalid)
			}
			deps[dep] = true
		}
		if len(c.RequiredTests) < 1 || len(c.RequiredTests) > 128 {
			return fmt.Errorf("%w: each contribution needs 1–128 required tests", ErrInvalid)
		}
		tests := map[string]bool{}
		for _, test := range c.RequiredTests {
			if !textOK(test) || tests[test] {
				return fmt.Errorf("%w: required tests must be non-empty and unique", ErrInvalid)
			}
			tests[test] = true
		}
		seen[c.Ticket.Identifier], ids[c.Ticket.ID] = true, true
	}
	return nil
}

func validateEvidence(e *Evidence, c ContributionSpec) error {
	if e == nil || !hashPattern.MatchString(e.Tree) || !e.Review.Independent || !textOK(e.Review.Reference) || len(e.Tests) > 128 {
		return fmt.Errorf("%w: tree and independent review evidence required", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, t := range e.Tests {
		if !textOK(t.Name) || !textOK(t.Command) || !textOK(t.Reference) || t.Status != "pass" || seen[t.Name] {
			return fmt.Errorf("%w: every test must have unique name, command, pass result and evidence reference", ErrInvalid)
		}
		seen[t.Name] = true
	}
	for _, test := range c.RequiredTests {
		if !seen[test] {
			return fmt.Errorf("%w: missing passing evidence for %q", ErrInvalid, test)
		}
	}
	return nil
}

func validateState(c *Campaign, id string) error {
	bad := func() error { return fmt.Errorf("%w: corrupt or unsupported campaign state", ErrInvalid) }
	if c.SchemaVersion != SchemaVersion || c.Pipeline != Pipeline || c.ID != id || !validID(id) || !ticketOK(c.Root) || (c.Mode != "parent" && c.Mode != "standalone") {
		return bad()
	}
	if !filepath.IsAbs(c.Checkout.Repository) || !filepath.IsAbs(c.Checkout.Worktree) || !strings.HasPrefix(c.Checkout.Branch, "refs/heads/") || !hashPattern.MatchString(c.Base) || !hashPattern.MatchString(c.ExpectedHead) {
		return bad()
	}
	if c.Revision < 1 || c.Revision > MaxOperations || len(c.Operations) != c.Revision || len(c.Blocks) > MaxOperations || len(c.Contributions) > MaxContributions {
		return bad()
	}
	switch c.State {
	case "planning", "ready", "realizing", "awaiting-trial", "verifying", "verified", "delivering", "pr-open", "delivered", "blocked", "stopped":
	default:
		return bad()
	}
	if c.Lease != nil && (!keyPattern.MatchString(c.Lease.Session) || !validID(c.Lease.Token) || c.Lease.ExpiresAt.IsZero()) {
		return bad()
	}
	if c.Plan == nil {
		if len(c.Contributions) != 0 || (c.State != "planning" && c.State != "blocked" && c.State != "stopped") {
			return bad()
		}
	} else {
		if err := validatePlan(c.Plan, c.Root, c.Mode); err != nil {
			return err
		}
		if len(c.Plan.Contributions) != len(c.Contributions) || c.State == "planning" {
			return bad()
		}
	}
	active, integrated := 0, 0
	head := c.Base
	for n, child := range c.Contributions {
		if !reflect.DeepEqual(c.Plan.Contributions[n], child.Spec) {
			return bad()
		}
		switch child.State {
		case "pending":
			if child.Base != "" || child.Integration != nil || child.Linear.Pending || child.Linear.StateID != "" || child.Linear.AcknowledgedAt != nil {
				return bad()
			}
		case "active":
			active++
			if n != integrated || child.Base != head || child.Linear.Pending || child.Linear.StateID != "" || child.Linear.AcknowledgedAt != nil {
				return bad()
			}
		case "integrated":
			if n != integrated || child.Base != head || child.Integration == nil || !hashPattern.MatchString(child.Integration.Commit) {
				return bad()
			}
			integrated++
			head = child.Integration.Commit
			if c.Mode == "standalone" {
				if child.Linear.Pending || child.Linear.StateID != "" || child.Linear.AcknowledgedAt != nil {
					return bad()
				}
			} else if child.Linear.Pending {
				if child.Linear.StateID != "" || child.Linear.AcknowledgedAt != nil {
					return bad()
				}
			} else if !uuidPattern.MatchString(child.Linear.StateID) || child.Linear.AcknowledgedAt == nil {
				return bad()
			}
		default:
			return bad()
		}
		if i := child.Integration; i != nil {
			if !validID(i.ID) || i.Parent != child.Base || !hashPattern.MatchString(i.Tree) || i.Evidence.Tree != i.Tree {
				return bad()
			}
			if child.State == "active" && i.Commit != "" {
				return bad()
			}
			if err := validateEvidence(&i.Evidence, child.Spec); err != nil {
				return err
			}
		}
	}
	ciRounds := 0
	for n, repair := range c.Repairs {
		if repair.Kind != "" && repair.Kind != "ci" {
			return bad()
		}
		if repair.Kind == "ci" {
			ciRounds++
			if ciRounds > 3 || c.Delivery == nil || c.Delivery.PR == nil {
				return bad()
			}
		}
		i := repair.Integration
		if integrated != len(c.Contributions) || i.Parent != head || !validID(i.ID) || !hashPattern.MatchString(i.Tree) || i.Evidence.Tree != i.Tree || !namesValid(repair.FindingIDs, true) {
			return bad()
		}
		names := []string{}
		for _, t := range i.Evidence.Tests {
			names = append(names, t.Name)
		}
		if !namesValid(names, true) {
			return bad()
		}
		if err := validateEvidence(&i.Evidence, ContributionSpec{RequiredTests: names}); err != nil {
			return err
		}
		if i.Commit == "" {
			if n != len(c.Repairs)-1 || c.State != "verifying" {
				return bad()
			}
		} else {
			if !hashPattern.MatchString(i.Commit) {
				return bad()
			}
			head = i.Commit
		}
	}
	if c.Trial != nil {
		if err := validateTrial(c); err != nil {
			return err
		}
	}
	if c.State == "verifying" && c.Trial == nil {
		return bad()
	}
	if c.Verification != nil {
		if !hashPattern.MatchString(c.Verification.Head) || c.Verification.PlanRevision < 1 || !textOK(c.Verification.Reference) {
			return bad()
		}
		if err := validateFindings(c.Verification.Findings, false); err != nil {
			return err
		}
	}
	if c.State == "verified" || c.State == "delivering" || c.State == "pr-open" || c.State == "delivered" {
		if err := validateVerification(c); err != nil {
			return err
		}
	}
	if c.State == "delivering" || c.State == "pr-open" || c.State == "delivered" {
		if c.Delivery == nil {
			return bad()
		}
	}
	if c.State == "pr-open" || c.State == "delivered" {
		if c.Delivery.PR == nil || c.Delivery.PR.HeadRefOID != c.ExpectedHead {
			return bad()
		}
	}
	if active > 1 || c.ExpectedHead != head {
		return bad()
	}
	if finalState(c.State) && (integrated != len(c.Contributions) || integrated == 0) {
		return bad()
	}
	if c.State == "ready" && (active != 0 || integrated != 0) {
		return bad()
	}
	for n, b := range c.Blocks {
		if !textOK(b.Reason) || (b.Class != "scope-local" && b.Class != "scope-transversal" && b.Class != "environment" && b.Class != "user-stop") {
			return bad()
		}
		if b.ResumeState != "planning" && b.ResumeState != "ready" && b.ResumeState != "realizing" && !finalState(b.ResumeState) {
			return bad()
		}
		if b.ResolvedAt != nil {
			if !textOK(b.Resolution) {
				return bad()
			}
		} else if n != len(c.Blocks)-1 || b.Resolution != "" {
			return bad()
		}
	}
	blocked := len(c.Blocks) > 0 && c.Blocks[len(c.Blocks)-1].ResolvedAt == nil
	if blocked != (c.State == "blocked" || c.State == "stopped") {
		return bad()
	}
	revisions := map[int]bool{}
	for operationID, op := range c.Operations {
		revision := op.Receipt.Revision
		if !keyPattern.MatchString(operationID) || len(op.Digest) != 64 || strings.IndexFunc(op.Digest, func(r rune) bool { return !unicode.Is(unicode.ASCII_Hex_Digit, r) }) >= 0 || op.Receipt.CampaignID != id || op.Receipt.OperationID != operationID || revision < 1 || revision > c.Revision || revisions[revision] {
			return bad()
		}
		revisions[revision] = true
	}
	return nil
}
