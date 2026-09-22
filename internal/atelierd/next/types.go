package next

import (
	"errors"
	"time"
)

const (
	ContractVersion  = 2
	SchemaVersion    = 1
	Pipeline         = "atelier-next"
	LeaseDuration    = 30 * time.Minute
	MaxFileBytes     = 8 << 20
	MaxOperations    = 4096
	MaxContributions = 128
)

var (
	ErrNotFound  = errors.New("next: campaign not found")
	ErrAmbiguous = errors.New("next: ambiguous campaign lookup")
	ErrConflict  = errors.New("next: conflicting operation or campaign")
	ErrLease     = errors.New("next: writer lease unavailable or stale")
	ErrCheckout  = errors.New("next: checkout does not match campaign")
	ErrInvalid   = errors.New("next: invalid input or state transition")
	ErrReconcile = errors.New("next: integration requires reconciliation")
	ErrBusy      = errors.New("next: state store busy")
)

type Ticket struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
}

type StartSpec struct {
	Root Ticket `json:"root"`
	Mode string `json:"mode"`
}

type ContributionSpec struct {
	Ticket        Ticket   `json:"ticket"`
	Contract      string   `json:"contract"`
	DependsOn     []string `json:"dependsOn"`
	RequiredTests []string `json:"requiredTests"`
}

type Plan struct {
	Reference         string             `json:"reference"`
	Revision          int                `json:"revision"`
	DecisionReference string             `json:"decisionReference,omitempty"`
	Contributions     []ContributionSpec `json:"contributions"`
	FinalChecks       *FinalChecks       `json:"finalChecks,omitempty"`
}

type TestEvidence struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	Status    string `json:"status"`
	Reference string `json:"reference"`
}

type ReviewEvidence struct {
	Independent bool   `json:"independent"`
	Reference   string `json:"reference"`
}

type Evidence struct {
	Tree   string         `json:"tree"`
	Tests  []TestEvidence `json:"tests"`
	Review ReviewEvidence `json:"review"`
}

type Checkout struct {
	Repository string `json:"repository"`
	Worktree   string `json:"worktree"`
	Branch     string `json:"branch"`
}

type Lease struct {
	Session   string    `json:"session"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Integration struct {
	ID       string   `json:"id"`
	Parent   string   `json:"parent"`
	Tree     string   `json:"tree"`
	Trailer  string   `json:"trailer"`
	Evidence Evidence `json:"evidence"`
	Commit   string   `json:"commit,omitempty"`
}

type LinearSync struct {
	Pending        bool       `json:"pending"`
	StateID        string     `json:"stateId,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt,omitempty"`
}

type Contribution struct {
	Spec        ContributionSpec `json:"spec"`
	State       string           `json:"state"`
	Base        string           `json:"base,omitempty"`
	Integration *Integration     `json:"integration,omitempty"`
	Linear      LinearSync       `json:"linear"`
}

type Block struct {
	Class             string     `json:"class"`
	Reason            string     `json:"reason"`
	ResumeState       string     `json:"resumeState"`
	PlanRevision      int        `json:"planRevision"`
	DecisionReference string     `json:"decisionReference,omitempty"`
	ResolvedAt        *time.Time `json:"resolvedAt,omitempty"`
}

type Event struct {
	SchemaVersion int            `json:"schemaVersion"`
	ID            string         `json:"eventId"`
	Type          string         `json:"type"`
	OperationID   string         `json:"operationId"`
	OccurredAt    time.Time      `json:"occurredAt"`
	Pipeline      string         `json:"pipeline"`
	CampaignID    string         `json:"campaignId"`
	RootTicketID  string         `json:"rootTicketId"`
	TicketID      string         `json:"ticketId"`
	SkillName     string         `json:"skillName"`
	Mode          string         `json:"mode"`
	SessionID     string         `json:"sessionId"`
	Revision      int            `json:"revision"`
	Data          map[string]any `json:"data"`
}

type Receipt struct {
	CampaignID    string `json:"campaignId"`
	OperationID   string `json:"operationId"`
	Revision      int    `json:"revision"`
	Token         string `json:"token,omitempty"`
	IntegrationID string `json:"integrationId,omitempty"`
	Trailer       string `json:"trailer,omitempty"`
	Commit        string `json:"commit,omitempty"`
}

type Operation struct {
	Digest  string  `json:"digest"`
	Receipt Receipt `json:"receipt"`
}

type Campaign struct {
	SchemaVersion  int                  `json:"schemaVersion"`
	Pipeline       string               `json:"pipeline"`
	ID             string               `json:"campaignId"`
	Root           Ticket               `json:"root"`
	Mode           string               `json:"mode"`
	State          string               `json:"state"`
	Revision       int                  `json:"revision"`
	Checkout       Checkout             `json:"checkout"`
	Base           string               `json:"base"`
	ExpectedHead   string               `json:"expectedHead"`
	Lease          *Lease               `json:"lease,omitempty"`
	Plan           *Plan                `json:"plan,omitempty"`
	Contributions  []Contribution       `json:"contributions"`
	Blocks         []Block              `json:"blocks"`
	Operations     map[string]Operation `json:"operations,omitempty"`
	Events         []Event              `json:"events,omitempty"`
	Repairs        []Repair             `json:"repairs,omitempty"`
	Verification   *Verification        `json:"verification,omitempty"`
	Delivery       *Delivery            `json:"delivery,omitempty"`
	SuiteReference string               `json:"suiteReference,omitempty"`
}

// Request is an in-process command, not the staging-file or persisted schema.
type Request struct {
	Action              string
	CampaignID          string
	Session             string
	Token               string
	OperationID         string
	CWD                 string
	Start               *StartSpec
	Plan                *Plan
	Evidence            *Evidence
	Ticket              string
	IntegrationID       string
	Commit              string
	StateID             string
	StateType           string
	Class               string
	Reason              string
	DecisionReference   string
	PreviousToken       string
	ConfirmOwnerStopped bool
	Verification        *Verification
	Repair              *RepairRequest
	PR                  *PullRequest
	BaseBranch          string
	Reference           string
}
