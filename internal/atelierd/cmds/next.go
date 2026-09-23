package cmds

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/next"
)

func NewNextCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "next",
		Short: "Manage isolated Atelier Next campaigns (experimental contract 3)",
		Long: `Persist Atelier Next campaigns under ~/.atelier-next (HOME selects the home directory).
This is independent of atelierd forge. No Linear API call, git commit, branch creation,
PR or cloud event shipment is performed. Git is required for checkout checks.

Mutations run from the bound worktree with --session, --token and a unique
--operation. Reuse exactly the same operation and arguments after a lost response.
Receipts and status are JSON; contract and campaign find print one value.
Leases last 30 minutes; renew explicitly. An expired lease is NOT proof the previous
writer stopped: takeover requires its token and --confirm-owner-stopped.

Exit codes: 30 missing, 31 ambiguous, 32 conflict, 33 lease, 34 checkout,
35 invalid input/transition, 36 reconciliation required, 37 store busy.
Verification and delivery record caller evidence; they never invent tests, screenshots,
CI results or merge authorization. Campaign state cannot be set arbitrarily.`,
		Example: `  atelierd next contract
  atelierd next campaign start --from /tmp/start.json --session session-1 --operation start-1
  atelierd next campaign status --campaign <id>
  atelierd next campaign find --ticket EXAMPLE-1
  atelierd next plan set --campaign <id> --session session-1 --token <token> --operation plan-1 --from /tmp/plan.json
  atelierd next contribution open --campaign <id> --session session-1 --token <token> --operation child-1 --ticket EXAMPLE-2`,
	}
	command.AddCommand(&cobra.Command{
		Use: "contract", Short: "Print the Next CLI contract version", Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), next.ContractVersion) },
	})
	campaign := &cobra.Command{Use: "campaign", Short: "Create, inspect, block and resume a campaign"}
	campaign.AddCommand(nextMutation("start", "start", "Bind a clean feature checkout and acquire its writer lease"), nextRead("status"), nextFind(),
		nextMutation("block", "block", "Pause for scope-local, scope-transversal or environment"),
		nextMutation("stop", "stop", "Record an explicit user-requested stop"),
		nextMutation("resume", "resume", "Resolve the latest block; scope decisions require a reference"))
	lease := &cobra.Command{Use: "lease", Short: "Acquire, renew or release exclusive writer ownership"}
	lease.AddCommand(nextMutation("acquire", "acquire", "Acquire a free lease or explicitly take over an expired writer"),
		nextMutation("renew", "renew", "Renew the live lease for 30 minutes"),
		nextMutation("release", "release", "Release ownership after the writer has stopped editing"))
	plan := &cobra.Command{Use: "plan", Short: "Record the approved orchestration plan"}
	plan.AddCommand(nextMutation("set", "plan", "Set the initial plan or a scope-approved revision"))
	contribution := &cobra.Command{Use: "contribution", Short: "Realize contributions sequentially in plan order"}
	contribution.AddCommand(nextMutation("open", "open", "Open the next contribution on the clean recorded HEAD"))
	integration := &cobra.Command{Use: "integration", Short: "Prepare and reconcile exactly one commit per contribution"}
	integration.AddCommand(nextMutation("prepare", "prepare", "Journal parent, staged tree, tests and independent review; return an integration ID"),
		nextMutation("finish", "finish", "Prove the committed parent and tree; record integration and pending Linear sync"),
		nextMutation("cancel", "cancel", "Cancel a prepared intent only while HEAD is still its parent"))
	linear := &cobra.Command{Use: "linear", Short: "Acknowledge observed child completion (never calls Linear)"}
	linear.AddCommand(nextMutation("ack", "ack", "Acknowledge a completed Linear state after saving and reading the child"))
	trial := &cobra.Command{Use: "trial", Short: "Record the user trial after contributions, before verification"}
	trial.AddCommand(nextMutation("record", "trial-record", "Record stack readiness and the user's tested/declined response (static: not-applicable)"))
	verification := &cobra.Command{Use: "verification", Short: "Record final QA and seal the verified HEAD"}
	verification.AddCommand(nextMutation("save", "verification-save", "Save QA evidence and the complete findings registry"), nextMutation("complete", "verification-complete", "Require QA complete; keep evidenced deployment-only criteria explicitly unverified"), nextMutation("reopen", "verification-reopen", "Return to QA before changing an attested branch"))
	repair := &cobra.Command{Use: "repair", Short: "Integrate targeted root corrections before the PR"}
	repair.AddCommand(nextMutation("prepare", "repair-prepare", "Prepare a tested repair for named findings"), nextMutation("finish", "repair-finish", "Prove and integrate the prepared repair commit"), nextMutation("cancel", "repair-cancel", "Cancel an uncommitted repair intent"))
	delivery := &cobra.Command{Use: "delivery", Short: "Fence delivery on the verified SHA and one PR"}
	delivery.AddCommand(nextMutation("start", "delivery-start", "Require the delivery gate before pushing"), nextMutation("observe", "delivery-observe", "Record unmodified gh pr view JSON"), nextMutation("check", "delivery-check", "Require current PR identity and green CI before merge"), nextMutation("complete", "delivery-complete", "Record a confirmed merge with green CI"))
	suite := &cobra.Command{Use: "suite", Short: "Read and publish only explicitly deferred or manual actions"}
	suite.AddCommand(nextRead("show"), nextMutation("publish", "suite-publish", "Record the published suite reference"))
	command.AddCommand(campaign, lease, plan, contribution, integration, linear, trial, verification, repair, delivery, suite, nextRead("events"))
	for _, group := range command.Commands() {
		if group.HasSubCommands() {
			group.Args = cobra.NoArgs
			group.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
		}
	}
	return command
}

func nextMutation(use, action, short string) *cobra.Command {
	r := next.Request{Action: action}
	var from string
	command := &cobra.Command{
		Use: use, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			r.CWD, err = os.Getwd()
			if err != nil {
				return err
			}
			switch action {
			case "start":
				r.Start = &next.StartSpec{}
				err = next.DecodeFile(from, r.Start)
			case "plan":
				r.Plan = &next.Plan{}
				err = next.DecodeFile(from, r.Plan)
			case "prepare":
				r.Evidence = &next.Evidence{}
				err = next.DecodeFile(from, r.Evidence)
			case "trial-record":
				r.Trial = &next.UserTrial{}
				err = next.DecodeFile(from, r.Trial)
			case "verification-save":
				r.Verification = &next.Verification{}
				err = next.DecodeFile(from, r.Verification)
			case "repair-prepare":
				r.Repair = &next.RepairRequest{}
				err = next.DecodeFile(from, r.Repair)
			case "delivery-observe":
				r.PR = &next.PullRequest{}
				err = next.DecodeFile(from, r.PR)
			}
			if err != nil {
				return err
			}
			store, err := next.Open()
			if err != nil {
				return err
			}
			receipt, err := store.Apply(cmd.Context(), r)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
		},
	}
	flag := func(target *string, name, description string, required bool) {
		command.Flags().StringVar(target, name, "", description)
		if required {
			_ = command.MarkFlagRequired(name)
		}
	}
	flag(&r.Session, "session", "writer session ID (required)", true)
	flag(&r.OperationID, "operation", "stable operation ID; reuse identical arguments only (required)", true)
	if action != "start" {
		flag(&r.CampaignID, "campaign", "campaign ULID (required)", true)
	}
	if action != "start" && action != "acquire" {
		flag(&r.Token, "token", "current lease token (required)", true)
	}
	switch action {
	case "start", "plan", "prepare", "trial-record", "verification-save", "repair-prepare", "delivery-observe":
		flag(&from, "from", "strict staging JSON file (required; see docs/atelier-next.md)", true)
	case "open":
		flag(&r.Ticket, "ticket", "exact contribution identifier (required)", true)
	case "finish", "cancel", "repair-finish", "repair-cancel":
		flag(&r.IntegrationID, "integration", "prepared integration ULID (required)", true)
	case "ack":
		flag(&r.Ticket, "ticket", "integrated child identifier (required)", true)
		flag(&r.Commit, "commit", "integrated commit SHA (required)", true)
		flag(&r.StateID, "state-id", "observed Linear workflow state UUID (required)", true)
		flag(&r.StateType, "state-type", "observed state type, must be completed (required)", true)
	case "block":
		flag(&r.Class, "class", "scope-local | scope-transversal | environment (required)", true)
		flag(&r.Reason, "reason", "blocking fact/question, never a secret (required)", true)
	case "stop", "verification-reopen":
		flag(&r.Reason, "reason", "reason for stopping or reopening (required)", true)
	case "delivery-start":
		flag(&r.BaseBranch, "base", "agreed target branch (required)", true)
	case "suite-publish":
		flag(&r.Reference, "reference", "published suite reference (required)", true)
	case "resume":
		flag(&r.Reason, "reason", "how the block was resolved (required)", true)
		flag(&r.DecisionReference, "decision-ref", "dated Linear scope-decision reference; required for scope", false)
	case "acquire":
		flag(&r.PreviousToken, "previous-token", "expired owner's token, required for takeover", false)
		command.Flags().BoolVar(&r.ConfirmOwnerStopped, "confirm-owner-stopped", false, "explicitly confirm the old editor and its processes have stopped")
	}
	return command
}

func nextRead(action string) *cobra.Command {
	var id string
	command := &cobra.Command{
		Use: action, Short: "Print campaign " + action + " as JSON (read-only)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := next.Open()
			if err != nil {
				return err
			}
			c, err := store.Status(cmd.Context(), id)
			if err != nil {
				return err
			}
			if action == "show" {
				items, err := next.Suite(c)
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
			}
			if action == "events" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(c.Events)
			}
			c.Operations, c.Events = nil, nil
			return json.NewEncoder(cmd.OutOrStdout()).Encode(c)
		},
	}
	command.Flags().StringVar(&id, "campaign", "", "campaign ULID (required)")
	_ = command.MarkFlagRequired("campaign")
	return command
}

func nextFind() *cobra.Command {
	var ticket, repo string
	command := &cobra.Command{
		Use: "find", Short: "Find exactly one campaign by root ticket; ambiguity never picks the latest", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := next.Open()
			if err != nil {
				return err
			}
			id, err := store.Find(cmd.Context(), ticket, repo)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), id)
			return nil
		},
	}
	command.Flags().StringVar(&ticket, "ticket", "", "exact root ticket identifier (required)")
	command.Flags().StringVar(&repo, "repository", "", "canonical common git directory, as shown by campaign status")
	_ = command.MarkFlagRequired("ticket")
	return command
}
