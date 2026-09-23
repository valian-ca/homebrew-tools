# `atelierd next` — campaign contract 3 (schema 2)

Atelier Next's independent campaign book-keeper, shipped in **atelierd 0.17.0**.
`next contract` prints `3`; the existing `forge contract` still prints `2`, with no
changes to its commands, state, exit codes or event taxonomy.

The local prototype also supports pre-trial environment repairs under contract 3 /
schema 2. This path requires the updated source binary; the published 0.17.0 binary
does not implement it. Existing schema-2 campaigns remain readable without migration.

```sh
go build -o /tmp/atelierd-next-dev ./cmd/atelierd
/tmp/atelierd-next-dev next contract
/tmp/atelierd-next-dev next --help
```

The commands above build an isolated development binary. The published version is
available through `brew install|upgrade valian-ca/tools/atelierd`. This CLI
**does not run tests, call Linear, take screenshots, commit, push or merge**. It
validates and persists the caller's attestations and external observations. The
six skills in `claude-plugins/plugins/atelier-next` perform those real operations.
No successful command is proof that an LLM genuinely ran a test or asked a human.

## Identity, state and ownership

State lives in `~/.atelier-next/campaigns/<ULID>.json` (HOME chooses the home directory),
not the Forge namespace. A namespace flock serializes creation and mutation, with
a two-second bounded wait. Snapshots use atomic replacement and file/directory fsync.
Directories are 0700, files 0600; state/lock symlinks and non-regular input files are
refused. No automatic pruning, checkout cleanup or daemon restart occurs.

A campaign binds one root UUID/identifier, mode parent|standalone, canonical Git
common directory, worktree, attached feature branch and starting HEAD. Create the
checkout first. Start refuses dirty trees and main/master/develop/staging; contribution
open also requires a clean tree/index, including untracked files. Mutations verify
checkout identity, and relevant operations verify the expected HEAD. No reset,
rebase, branch creation or arbitrary adoption of unknown history is performed.

States: `planning → ready → realizing → awaiting-trial → verifying → verified → delivering → pr-open
→ delivered`. Blocks/stops preserve history and resume state. There is no generic
state setter. One active contribution, strict plan order, dependencies earlier in
the plan. A standalone contribution is the root; parent contributions are children.
The caller validates actual Linear parentage; the offline CLI cannot discover it.

Each mutation requires `--session <id> --operation <id>`, plus `--campaign <id>
--token <token>` except start (neither) and acquire (no token). Operation/session IDs
use 1–128 letters/digits/dot/underscore/hyphen, first character alphanumeric. Save the
operation before calling. Identical replay returns the historical receipt without a
new transition; changed arguments or staging contents under that ID fail with 32.
A historical token receipt does not renew or recreate a lease. A new owner uses new
operation IDs while reconciling the same prepared integration IDs.

Lease duration is 30 minutes, **explicitly renewed**. Ordinary mutations do not extend
it. A live lease cannot be stolen. After expiry, acquisition requires the previous
token and `--confirm-owner-stopped`: first actually stop the old editor/processes.
The new token fences CLI mutations, not arbitrary Git/editor writes. This is a
cooperative protocol, not an OS sandbox; consumers must stop writing when ownership
is lost. Release only after all external writers have stopped.

```sh
atelierd next lease renew --campaign <id> --session <session> --token <token> --operation <op>
atelierd next lease release --campaign <id> --session <session> --token <token> --operation <op>
atelierd next lease acquire --campaign <id> --session <session> --operation <op>
atelierd next lease acquire --campaign <id> --session <new-session> --operation <op> --previous-token <expired-token> --confirm-owner-stopped
atelierd next campaign status --campaign <id>
atelierd next campaign find --ticket TEST-1
```

Status excludes the idempotency receipt map but retains all working evidence,
block resolutions and pending sync. Find returns exactly one match;
`--repository <canonical-common-dir>` can disambiguate. Reads do not renew leases
or mutate the campaign snapshot. They may initialize the namespace lock directory.

Exit codes: **30** missing campaign, **31** ambiguous, **32** conflict/idempotence,
**33** lease, **34** checkout, **35** invalid input/proof/transition, **36** Git
reconciliation required, **37** store busy; other failures use 1. Failed mutations
return no success receipt. Input decoding rejects unknown fields and trailing JSON.

## Staging schemas and commands

The remaining examples omit the common mutation flags above. Pass them on every
actual call. Staging files should be outside the working tree and contain no secrets.
UUIDs are lowercase canonical strings, ticket identifiers exact uppercase values.

### Start and plan

`campaign start --from <file>`:

```json
{"root":{"id":"11111111-1111-4111-8111-111111111111","identifier":"TEST-1"},"mode":"standalone"}
```

Returns campaignId, token, operationId and revision. A lost start response is
recovered with the same request from the same checkout; another start cannot
bind a second campaign to that checkout/branch.

`plan set --from <file>`:

```json
{
  "reference":"linear-plan-document", "revision":1,
  "contributions":[{
    "ticket":{"id":"11111111-1111-4111-8111-111111111111","identifier":"TEST-1"},
    "contract":"The complete local behavior and interface",
    "dependsOn":[], "requiredTests":["format","analyze","unit"]
  }],
  "finalChecks":{
    "criteria":["AC1"], "tests":["root-test"], "surfaces":[],
    "visual":false, "noVisualReason":"Static change without a visual surface"
  }
}
```

A visual plan names its surfaces, sets visual true and requires a final published
capture for each. Final criteria/tests must be non-empty, unique and stable. A plan
without finalChecks cannot record the trial or seal/pass delivery until amended.
Schema-1 campaign files are refused, not silently migrated or deleted. Resolve old
experimental state explicitly with its compatible tooling before reusing the namespace. Plans contain 1–128 contributions and 1–128 required
tests per contribution. Unknown/cyclic/duplicate dependencies and identities fail.

### Contribution and repair integration

`contribution open --ticket TEST-1` opens only the next contribution. Execute its
tests/review, stage all changes and obtain the exact `git write-tree` hash.
`integration prepare --from <file>` takes:

```json
{
  "tree":"<full-staged-tree-hash>",
  "tests":[{"name":"unit","command":"actual test command","status":"pass","reference":"durable-evidence"}],
  "review":{"independent":true,"reference":"independent-review-evidence"}
}
```

The actual tests array must cover every required test. Missing, duplicate, failed
or stale-tree evidence is refused. The service records attestations and references;
it does not inspect logs or manufacture independent review.

Preparation journals exact parent, tree, evidence and integrationId **before** Git
moves the branch. The caller must invoke **valian:commit**, preserving hooks/signing;
no campaign metadata belongs in the message. `integration finish --integration <id>`
proves HEAD is one non-merge commit matching the prepared parent/tree, with clean bound
checkout/index and live owner. The message is not read. Subject, ticket footer and
commit authorship are not recovery evidence; content-addressed Git objects are.
A different message with identical parent/tree is the same attested content; only the
observed HEAD is recorded. The cooperative lease cannot identify an arbitrary external
editor's intent. Contract 3 still permits one commit per contribution, not a chain or
arbitrary adoption. `integration cancel --integration <id>` is permitted only before
HEAD moves. Wrong tree (including a mutating hook), merge, extra commit, dirty checkout
or unknown/rebased history blocks for explicit reconciliation, never reset/recommit.

Crash after preparation: commit the prepared tree or cancel. Crash after commit:
fresh owner reads state and finishes the **same ID**, without another commit. Crash
after finish: replay or inspect the integrated state. Re-finishing an integrated ID
returns its commit without another integration event.

Parent children then have `linear.pending=true`. The caller writes Done/completed
in Linear, reads it back, and calls `linear ack --ticket <child> --commit <integratedSHA>
--state-id <observedUUID> --state-type completed`. Failed sync does not recode the
child. Standalone roots **cannot** be acknowledged at integration; they close only
on delivery. Keep cross-ticket references out of commits/PRs; correlate in Linear.

### User trial before final verification

The last integration enters **awaiting-trial**, even if Linear sync is still pending.
The caller starts the stack from its documented recipe, checks readiness/logs and
attempts recovery before asking about an infrastructure failure. Offer the user a
trial with the stack running, then **wait for the response**. No full attended track.
`trial record --from <file>`:

```json
{"head":"<current-expectedHead-including-environment-repairs>","response":"tested","reference":"actual-user-response","readiness":"ready-stack-evidence"}
```

Response is tested or declined, never silence. A static plan with no surfaces may
instead use response not-applicable, reference to the plan and notApplicableReason,
without a fictitious readiness value. The gate transitions to verifying. Lost responses
replay idempotently; stop/resume preserves awaiting-trial. Trial HEAD is the integrated
HEAD when recorded, including any pre-trial environment repairs. Later QA/CI repairs preserve this
historical trial; new contributions/changed finalChecks invalidate it and require a
new trial at the then-current integrated HEAD. The CLI validates the attestation, not that the human truly answered.
Device-bank policy belongs to the skill: mandatory lease when available, wait/retry
when exhausted, stop/ask when unavailable; no automatic off-bank fallback or explicit
mandatory device renewal. The campaign lease renewal remains required independently.

### Environment repairs before the trial

If readiness requires a Git change, `repair prepare --from <file>` accepts the
following only in **awaiting-trial**, in both standalone and parent campaigns:

```json
{
  "kind":"environment",
  "reason":"The emulator socket prevents preparation of the trial account",
  "reference":"orchestration-diagnostic",
  "evidence":{
    "tree":"<full-staged-tree-hash>",
    "tests":[{"name":"environment-regression","command":"actual command","status":"pass","reference":"test-log"}],
    "review":{"independent":true,"reference":"targeted-independent-review"}
  }
}
```

No findingIds (or an empty array), no QA report and no manufactured trial are needed.
Reason and diagnostic reference are mandatory and persisted with the repair. Run
targeted tests and project-required checks, valian:verify and independent local review.
This lane restores trial infrastructure, accounts or data; product changes still follow
scope decisions. The CLI validates attestations, not the semantic scope of the diff.

Use the same live lease, bound checkout, staged-tree evidence and exact-parent protocol
as QA repairs: prepare, valian:commit, `repair finish --integration <id>`. Finish advances
expectedHead and keeps **awaiting-trial**, without changing contributions, their Linear
sync, or the plan. Multiple sequential environment repairs are allowed; only one intent
may be prepared. An unfinished repair blocks `trial record` even if the tree is clean.
QA save/complete and delivery remain forbidden until an actual trial response.

Recovery uses the same ID: before commit, finish the prepared work or `repair cancel`;
after commit, finish without recommitting; after finish, keep the recorded repaired HEAD.
Finish/cancel before blocking or stopping. For an environment block originating at this
gate, resume with the diagnosis and recovery path, then repair in awaiting-trial, never
while blocked/stopped. Rebuild/restart and check readiness at the repaired HEAD before
offering the trial and waiting for the user's tested/declined response. Stop/resume and
fresh-store recovery preserve both the repair and the pending trial.

### Final verification

No verification save/complete before the trial. All child syncs must finish before sealing.
An independent fresh-context reader audits every original promise and executes each UI
AC in the running app; parent-only corrections. Run reports are evidence, not promises.
The CLI does not execute that audit. Keep one verification document per campaign, extend
it by patch: changing the report reference is rejected.
`verification save --from <file>` saves partial/final evidence and a complete findings
registry without attesting readiness:

```json
{
  "head":"<HEAD>", "planRevision":1, "reference":"linear-report-document",
  "criteria":[{"name":"AC1","status":"pass","reference":"criterion-evidence"}],
  "tests":[{"name":"root-test","status":"pass","reference":"test-evidence"}],
  "surfaces":[], "review":{"independent":true,"reference":"root-review"},
  "captures":[], "findings":[{
    "id":"f1", "brief":"Self-contained problem description", "impact":"Consequence for a user",
    "class":"necessary", "status":"open"
  }]
}
```

Classes: necessary, improvement, scope, future, manual. Statuses: open, accepted,
resolved, deferred. Resolution needs reference evidence; scope and improvements
accepted/resolved/deferred need a human decisionReference. Deferral needs a trigger;
necessary corrections cannot be deferred. Reclassification needs a human decision.
Existing findings may not silently disappear in later reports.

For visual surfaces, captures contain `{surface, head, reference}` with an HTTPS
Linear publication URL and the current HEAD. Upload/read the real screenshot before
attesting it. Metadata validation cannot determine whether the image is real.

`repair prepare --from <file>` takes `{findingIds:["f1"], evidence:<same evidence shape>}`.
Only open necessary or explicitly accepted improvement findings may be repaired.
Tests/review are targeted. Commit via valian:commit without campaign metadata, `repair finish
--integration <id>`, then update the full verification report and all final captures
for the new HEAD. `repair cancel` works only before committing, like contribution cancel.

`verification complete` requires all contributions and Linear sync complete, all
planned criteria passed or strictly reserved for post-deployment, all tests/surfaces
passed, independent review, no open/accepted findings,
all final visual captures published for the current HEAD, and resolved decisions.
It seals the branch as verified. `verification reopen --reason <fact>` returns a
verified/delivering/pr-open branch to QA before a later repair; old PR identity remains.
Once a merge has been observed, neither QA reopening nor a stale OPEN observation
can undo it, even before `delivery complete`.

### Deployment-only criteria, explicitly unverified

Only criteria may use the following alternative to pass:

```json
{"name":"AC1","status":"post-deployment","reference":"evidence-of-external-constraint",
 "postDeployment":{"reason":"Only the deployed production callback can establish this promise","trigger":"after deployment to the real domain","findingId":"production-check"}}
```

Link to a finding with that ID, class manual, status deferred, **same reference and
trigger**, self-contained brief and user impact. The independent auditor examines
why no local check can establish this promise. It is not a pass, not an omitted test,
not an infrastructure failure and never a known defect. Tests and surfaces cannot use
this status; visual captures remain mandatory. Pass plus postDeployment metadata is
invalid. AC n/N counts it in N, not n, and reports the deployment-only count separately.
Delivery and suite preserve the unverified criterion and its actionable follow-up.
The CLI checks structure/links, not the truth of an external limitation; fabricated
justifications remain invalid behavior even when a syntactically valid payload passes.

### Delivery and suite

`delivery start --base <target>` requires the gate and clean checkout before push.
GitHub origin, target branch and subsequent PR identity are bound to the campaign.
The caller invokes valian:verify before delivery and valian:pr for the unique PR,
then records **unmodified** output of:

```sh
gh pr view <number> --json number,url,state,headRefName,headRefOid,baseRefName,isDraft,mergeCommit,statusCheckRollup
```

`delivery observe --from <file>` rejects a different repo/branch/base/SHA, draft, closed
or second PR. Supply statusCheckRollup even if empty. `delivery check` requires current
verification, clean tree and green CI. Empty rollup means no checks; unknown types,
incomplete CheckRuns, failed conclusions and pending/error StatusContexts are not green.
Allowed completed CheckRun conclusions: SUCCESS, NEUTRAL, SKIPPED; StatusContext: SUCCESS.

Before an explicitly authorized merge, refresh the observation and check, then use
GitHub's `--match-head-commit <verifiedSHA>` guard and repository-required checks/reviews.
Never use admin bypass. After merge, refresh observation; `delivery complete` requires
MERGED, a full merge commit SHA and green checks. Only then close the root in Linear.
Keep the branch and worktree even after merge and suite/release; no automatic cleanup.
Old Atelier ship-it remains available autonomously outside campaigns; Next delivery
has no standalone no-campaign path.

`suite show --campaign <id>` returns only deferred entries and current pr-open/delivered
state; it does not reread historical reports. Publish the registry both in Linear and
chat with consequences/triggers, then `suite publish
--reference <document>` under the live lease, then release. The CLI cannot attest
root closure in Linear; the delivery skill reads/writes it idempotently after merge.

### Bounded CI plumbing repairs

Delivery may fix CI configuration, lockfiles, generated snapshots, lint/format only.
Business defects return to verification. Reopen first, save the finding, edit/test,
independent targeted review, `repair prepare` with `kind:"ci"`, valian:commit, finish.
The CLI requires a bound OPEN campaign PR and permits at most **three integrated CI
repairs**, durable across sessions. An uncommitted canceled intent does not use a round.
Do not relabel a fourth CI round as ordinary QA. Every changed tree invalidates the
attestation/captures; reverify affected promises and complete verification before
`delivery start`, re-push and observation of the same PR. A closed PR blocks, never
create a replacement or reopen it. Recheck app/UI evidence if that content changed.

## Scope and history

`campaign block --class scope-local|scope-transversal|environment --reason <fact>`;
uncertain extent is transversal. Scope resume needs `--decision-ref <datedComment>`
and `--reason <resolution>`. A transversal scope also requires a plan revision +1,
with matching decisionReference. A local replan can change only the active child.
Integrated contracts/order stay immutable; before root repairs a parent plan may add
a corrective contribution, afterward preserve contributions and use root repairs.
`campaign stop --reason <user-request>` records explicit user stop; resume requires a
resolution. Neither command constitutes asking the user: the skill must actually ask.
Finish/cancel a prepared child/root integration before blocking. Requested stops live
in CLI state; no additional Linear stop document or mandatory final campaign comment.
Manual handoffs are short self-contained prose, without harness-specific commands.

## Operational state, compatibility and limits

Every successful new operation atomically stores the current state and its idempotency
receipt (request fingerprint plus result). Replays return that result without rewriting
state or repeating the transition. Receipt revisions must be unique and contiguous.
Operational records retain ownership, prepared parent/tree intentions, evidence, block
reasons/resolutions and scope-decision references, trial, verification, PR and suite.
A resolved block stores the supplied resume reason in `resolution`, next to `resolvedAt`
and `decisionReference`; recovery does not depend on reconstructing a telemetry history.

**Next collects, stores and ships no telemetry events.** There is no event journal,
spool/outbox or events command. Event types, Firestore shipment and retention will be
defined with the actual dashboard requirements; there is no speculative collection or
promise of historical backfill. Existing forge/skill/ship/session telemetry is untouched.

Telemetry was removed before the first publication of contract 3/schema 2 in 0.17.0.
Old schemas and earlier experimental snapshots containing `events` are rejected, not
silently rewritten or purged. Operational state still persists after completion: removing
telemetry is not automatic cleanup of recovery evidence or idempotency receipts.

Limits: 8 MiB per state/staging file, 128 contributions/tests, 256 findings, 4096
operations, 4096 bytes per text field. Capacity failures do not evict evidence.
Git calls time out after 10 s. No mode conversion, automatic migration of old Atelier
runs, rebase reconciliation or arbitrary state mutation is provided.

## Tests and release

```sh
go test ./...
go vet ./...
go test -race ./internal/atelierd/next ./internal/atelierd/cmds
GOTOOLCHAIN=go1.25.5 golangci-lint run
```

The pinned linter invocation matches the workstation linter built with Go 1.25; CI
uses its own compatible toolchain. Tests cover static, multi-child and visual campaign
flows plus ownership races, crash recovery, scope, missing evidence, stale captures,
wrong PR, ambiguous CI and merge guards, trailer-free recovery, user-trial gate,
deployment-only criteria (without waiving tests/captures), unique report and CI cap.
They also check event-free storage, removed events CLI, durable replay/decisions, receipt
integrity without an event journal, and refusal of obsolete event-bearing snapshots.
These are technical simulations, not claims
of completed business-ticket/LLM pilots. Run real pilots before replacing old Atelier.

Release 0.17.0 is published as `atelierd-0.17.0`; the stable formula uses the actual
GitHub archive checksum. For future releases: source PR and green checks/review, merge,
tag the reviewed source, compute its tarball SHA, update/test the formula, then publish
any dependent plugin. Never relabel an old tarball or invent a checksum. No daemon
replacement is necessary to publish; users upgrade when ready.
