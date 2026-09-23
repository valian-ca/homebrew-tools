package next

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

func (s *Store) start(ctx context.Context, r Request, checkout Checkout, head, digest string) (*Campaign, error) {
	if r.Start == nil || !ticketOK(r.Start.Root) || (r.Start.Mode != "parent" && r.Start.Mode != "standalone") {
		return nil, fmt.Errorf("%w: root UUID/identifier and parent|standalone mode required", ErrInvalid)
	}
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if op, ok := c.Operations[r.OperationID]; ok && op.Digest == digest {
			return c, nil
		}
		if c.Checkout.Worktree == checkout.Worktree || (c.Checkout.Repository == checkout.Repository && c.Checkout.Branch == checkout.Branch) {
			return nil, fmt.Errorf("%w: checkout already belongs to campaign %s; resume it", ErrConflict, c.ID)
		}
	}
	switch checkout.Branch {
	case "refs/heads/main", "refs/heads/master", "refs/heads/develop", "refs/heads/staging":
		return nil, fmt.Errorf("%w: create a feature branch before starting a campaign", ErrCheckout)
	}
	if err := cleanCheckout(ctx, checkout.Worktree); err != nil {
		return nil, err
	}
	return &Campaign{
		SchemaVersion: SchemaVersion, Pipeline: Pipeline, ID: ulid.New(), Root: r.Start.Root,
		Mode: r.Start.Mode, State: "planning", Checkout: checkout, Base: head, ExpectedHead: head,
		Contributions: []Contribution{}, Blocks: []Block{}, Operations: map[string]Operation{},
	}, nil
}

func owns(c *Campaign, r Request, now time.Time) error {
	if c.Lease == nil || c.Lease.Session != r.Session || c.Lease.Token != r.Token || !now.Before(c.Lease.ExpiresAt) {
		return ErrLease
	}
	return nil
}

func active(c *Campaign) *Contribution {
	for n := range c.Contributions {
		if c.Contributions[n].State == "active" {
			return &c.Contributions[n]
		}
	}
	return nil
}

func (s *Store) mutate(ctx context.Context, c *Campaign, r Request, head string, now time.Time, receipt *Receipt) error {
	if r.Action != "start" && r.Action != "acquire" {
		if err := owns(c, r, now); err != nil {
			return err
		}
	}
	switch r.Action {
	case "start":
		c.Lease = &Lease{Session: r.Session, Token: ulid.New(), ExpiresAt: now.Add(LeaseDuration)}
		receipt.Token = c.Lease.Token
	case "acquire":
		if c.Lease != nil {
			if now.Before(c.Lease.ExpiresAt) {
				return fmt.Errorf("%w: live owner %s; renew or release with its token", ErrLease, c.Lease.Session)
			}
			if !r.ConfirmOwnerStopped || r.PreviousToken != c.Lease.Token {
				return fmt.Errorf("%w: expired owner is not proof it stopped; explicit confirmation and previous token required", ErrLease)
			}
		}
		c.Lease = &Lease{Session: r.Session, Token: ulid.New(), ExpiresAt: now.Add(LeaseDuration)}
		receipt.Token = c.Lease.Token
	case "renew":
		c.Lease.ExpiresAt = now.Add(LeaseDuration)
		receipt.Token = c.Lease.Token
	case "release":
		c.Lease = nil
	case "block", "stop":
		if c.State == "blocked" || c.State == "stopped" || c.State == "delivered" {
			return fmt.Errorf("%w: already blocked; resolve before another block", ErrInvalid)
		}
		for _, repair := range c.Repairs {
			if repair.Integration.Commit == "" {
				return ErrReconcile
			}
		}
		if child := active(c); child != nil && child.Integration != nil {
			return fmt.Errorf("%w: finish or cancel prepared integration before blocking", ErrReconcile)
		}
		if r.Action == "block" && r.Class == "user-stop" {
			return fmt.Errorf("%w: use campaign stop for a user-requested stop", ErrInvalid)
		}
		if r.Action == "stop" {
			r.Class = "user-stop"
		}
		if !textOK(r.Reason) || (r.Class != "scope-local" && r.Class != "scope-transversal" && r.Class != "environment" && r.Class != "user-stop") {
			return fmt.Errorf("%w: block class and reason required", ErrInvalid)
		}
		if r.Class == "scope-local" && active(c) == nil {
			return fmt.Errorf("%w: local scope requires an active contribution", ErrInvalid)
		}
		b := Block{Class: r.Class, Reason: r.Reason, ResumeState: c.State}
		if c.Plan != nil {
			b.PlanRevision = c.Plan.Revision
		}
		c.Blocks = append(c.Blocks, b)
		c.State = "blocked"
		if r.Action == "stop" {
			c.State = "stopped"
		}
	default:
		if head != c.ExpectedHead && r.Action != "finish" && r.Action != "repair-finish" {
			return fmt.Errorf("%w: HEAD moved outside recorded integration", ErrReconcile)
		}
		switch r.Action {
		case "plan":
			return setPlan(c, r.Plan)
		case "resume":
			return resume(c, r, now)
		case "open":
			return openContribution(ctx, c, r.Ticket)
		case "prepare":
			return prepare(ctx, c, r.Evidence, receipt)
		case "cancel":
			child := active(c)
			if child == nil || child.Integration == nil || child.Integration.ID != r.IntegrationID {
				return fmt.Errorf("%w: active prepared integration required", ErrInvalid)
			}
			child.Integration = nil
		case "finish":
			return finish(ctx, c, r.IntegrationID, head, receipt)
		case "ack":
			return acknowledge(c, r, now)
		default:
			return finalMutation(ctx, c, r, head, receipt)
		}
	}
	return nil
}

func setPlan(c *Campaign, p *Plan) error {
	if err := validatePlan(p, c.Root, c.Mode); err != nil {
		return err
	}
	nextRevision := 1
	if c.Plan != nil {
		nextRevision = c.Plan.Revision + 1
	}
	if p.Revision != nextRevision {
		return fmt.Errorf("%w: plan revision must be %d", ErrInvalid, nextRevision)
	}
	if c.State != "planning" {
		if c.State != "blocked" || len(c.Blocks) == 0 {
			return fmt.Errorf("%w: replan requires a scope block", ErrInvalid)
		}
		b := c.Blocks[len(c.Blocks)-1]
		if (b.Class != "scope-local" && b.Class != "scope-transversal") || !textOK(p.DecisionReference) {
			return fmt.Errorf("%w: replan requires a recorded scope decision", ErrInvalid)
		}
		if b.Class == "scope-local" {
			if len(p.Contributions) != len(c.Contributions) {
				return fmt.Errorf("%w: local scope cannot change campaign topology", ErrInvalid)
			}
			for n, child := range c.Contributions {
				if child.State != "active" && !reflect.DeepEqual(child.Spec, p.Contributions[n]) {
					return fmt.Errorf("%w: local scope cannot change another contribution; resume unchanged and open a transversal block", ErrInvalid)
				}
			}
		}
	}
	if len(c.Repairs) > 0 && !reflect.DeepEqual(c.Plan.Contributions, p.Contributions) {
		return fmt.Errorf("%w: after root repairs, preserve contribution contracts and use another root repair", ErrInvalid)
	}
	children := make([]Contribution, len(p.Contributions))
	old := map[string]Contribution{}
	for n, child := range c.Contributions {
		old[child.Spec.Ticket.ID] = child
		if child.State == "integrated" && (n >= len(p.Contributions) || !reflect.DeepEqual(child.Spec, p.Contributions[n])) {
			return fmt.Errorf("%w: integrated contracts/order are immutable; add a correction contribution instead", ErrInvalid)
		}
		if child.State == "active" {
			if child.Integration != nil {
				return ErrReconcile
			}
			if n >= len(p.Contributions) || child.Spec.Ticket != p.Contributions[n].Ticket {
				return fmt.Errorf("%w: preserve the active contribution in place", ErrInvalid)
			}
		}
	}
	for n, spec := range p.Contributions {
		children[n] = Contribution{Spec: spec, State: "pending"}
		if prior, ok := old[spec.Ticket.ID]; ok {
			if prior.Spec.Ticket != spec.Ticket {
				return fmt.Errorf("%w: ticket identity changed", ErrInvalid)
			}
			children[n] = prior
			children[n].Spec = spec
		}
	}
	if c.Plan != nil && (!reflect.DeepEqual(c.Plan.FinalChecks, p.FinalChecks) || len(c.Contributions) != len(children)) {
		c.Trial = nil
	}
	c.Plan, c.Contributions = p, children
	if c.State == "planning" {
		c.State = "ready"
	}
	return nil
}

func resume(c *Campaign, r Request, now time.Time) error {
	if (c.State != "blocked" && c.State != "stopped") || len(c.Blocks) == 0 {
		return fmt.Errorf("%w: campaign is not blocked or stopped", ErrInvalid)
	}
	b := &c.Blocks[len(c.Blocks)-1]
	if b.Class == "scope-local" || b.Class == "scope-transversal" {
		if !textOK(r.DecisionReference) {
			return fmt.Errorf("%w: scope decision reference required", ErrInvalid)
		}
		if b.Class == "scope-transversal" && (c.Plan == nil || c.Plan.Revision <= b.PlanRevision) {
			return fmt.Errorf("%w: transversal scope requires a revised plan", ErrInvalid)
		}
		if c.Plan != nil && c.Plan.Revision > b.PlanRevision && c.Plan.DecisionReference != r.DecisionReference {
			return fmt.Errorf("%w: resume decision must match the revised plan", ErrInvalid)
		}
	}
	if !textOK(r.Reason) {
		return fmt.Errorf("%w: resolution reason required", ErrInvalid)
	}
	b.ResolvedAt, b.DecisionReference, b.Resolution = &now, r.DecisionReference, r.Reason
	c.State = b.ResumeState
	if b.Class == "scope-transversal" && finalState(c.State) {
		c.State = "verifying"
		c.SuiteReference = ""
	}
	if c.State == "planning" && c.Plan != nil {
		c.State = "ready"
	}
	if c.State == "verifying" || c.State == "awaiting-trial" {
		for _, child := range c.Contributions {
			if child.State != "integrated" {
				c.State = "realizing"
				c.Trial = nil
				break
			}
		}
	}
	if c.State == "verifying" && c.Trial == nil {
		c.State = "awaiting-trial"
	}
	return nil
}

func openContribution(ctx context.Context, c *Campaign, ticket string) error {
	if (c.State != "ready" && c.State != "realizing") || active(c) != nil {
		return fmt.Errorf("%w: campaign must be ready with no active contribution", ErrInvalid)
	}
	for n := range c.Contributions {
		child := &c.Contributions[n]
		if child.State == "integrated" {
			continue
		}
		if child.Spec.Ticket.Identifier != ticket {
			return fmt.Errorf("%w: next contribution is %s", ErrInvalid, child.Spec.Ticket.Identifier)
		}
		if err := cleanCheckout(ctx, c.Checkout.Worktree); err != nil {
			return err
		}
		child.State, child.Base = "active", c.ExpectedHead
		c.State = "realizing"
		return nil
	}
	return fmt.Errorf("%w: no pending contribution", ErrInvalid)
}

func prepare(ctx context.Context, c *Campaign, e *Evidence, receipt *Receipt) error {
	child := active(c)
	if c.State != "realizing" || child == nil {
		return fmt.Errorf("%w: active contribution required", ErrInvalid)
	}
	if child.Integration != nil {
		return fmt.Errorf("%w: integration already prepared; use its ID or cancel before preparing again", ErrConflict)
	}
	if err := validateEvidence(e, child.Spec); err != nil {
		return err
	}
	tree, err := stagedTree(ctx, c.Checkout.Worktree)
	if err != nil {
		return err
	}
	if e.Tree != tree {
		return fmt.Errorf("%w: evidence must attest the staged tree", ErrInvalid)
	}
	id := ulid.New()
	child.Integration = &Integration{ID: id, Parent: c.ExpectedHead, Tree: tree, Evidence: *e}
	receipt.IntegrationID = id
	return nil
}

func finish(ctx context.Context, c *Campaign, id, head string, receipt *Receipt) error {
	for n := range c.Contributions {
		child := &c.Contributions[n]
		i := child.Integration
		if i == nil || i.ID != id {
			continue
		}
		if i.Commit != "" {
			if head != c.ExpectedHead {
				return ErrReconcile
			}
			receipt.Commit, receipt.IntegrationID = i.Commit, i.ID
			return nil
		}
		if c.State != "realizing" || child.State != "active" {
			return ErrInvalid
		}
		if err := proveIntegration(ctx, c.Checkout.Worktree, head, i); err != nil {
			return err
		}
		i.Commit, child.State, c.ExpectedHead = head, "integrated", head
		child.Linear.Pending = c.Mode == "parent"
		receipt.Commit, receipt.IntegrationID = head, i.ID
		allIntegrated := true
		for _, other := range c.Contributions {
			if other.State != "integrated" {
				allIntegrated = false
			}
		}
		if allIntegrated {
			c.State = "awaiting-trial"
			c.Trial = nil
		}
		return nil
	}
	return fmt.Errorf("%w: unknown integration ID", ErrInvalid)
}

func acknowledge(c *Campaign, r Request, now time.Time) error {
	if c.Mode != "parent" {
		return fmt.Errorf("%w: standalone root is closed at delivery, never at integration", ErrInvalid)
	}
	if !uuidPattern.MatchString(r.StateID) || r.StateType != "completed" {
		return fmt.Errorf("%w: observed completed Linear state UUID required", ErrInvalid)
	}
	for n := range c.Contributions {
		child := &c.Contributions[n]
		if child.Spec.Ticket.Identifier != r.Ticket {
			continue
		}
		if child.State != "integrated" || child.Integration == nil || child.Integration.Commit != r.Commit {
			return fmt.Errorf("%w: acknowledgment must name the integrated commit", ErrInvalid)
		}
		if !child.Linear.Pending {
			if child.Linear.StateID != r.StateID {
				return ErrConflict
			}
			return nil
		}
		child.Linear = LinearSync{StateID: r.StateID, AcknowledgedAt: &now}
		return nil
	}
	return fmt.Errorf("%w: unknown child", ErrInvalid)
}
