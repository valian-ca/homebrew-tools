# `atelierd next` — campaign contract 2

Atelier Next's independent campaign book-keeper. Target release: **atelierd 0.17.0**.
`next contract` prints `2`; the existing `forge contract` still prints `2`, with no
changes to its commands, state, exit codes or event taxonomy.

```sh
go build -o /tmp/atelierd-next-dev ./cmd/atelierd
/tmp/atelierd-next-dev next contract
/tmp/atelierd-next-dev next --help
```

Use the development binary until the release is actually published. This CLI
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

States: `planning → ready → realizing → verifying → verified → delivering → pr-open
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
atelierd next events --campaign <id>
```

Status excludes the operation/event journals but retains all working evidence and
pending sync. Find returns exactly one match; `--repository <canonical-common-dir>`
can disambiguate. Reads do not renew leases or generate campaign events. They may
initialize the namespace lock directory.

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
capture for each. Final criteria/tests must be non-empty, unique and stable. Earlier
local contract-1 plans without finalChecks remain readable, but **cannot seal or
pass delivery** until amended. Plans contain 1–128 contributions and 1–128 required
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

Preparation journals parent, tree, evidence, unique integrationId and exact trailer
`Atelier-Next-Integration: <id>` **before** Git moves the branch. Commit normally,
with that trailer exactly once, preserving hooks/signing. Then `integration finish
--integration <id>` proves the HEAD is one non-merge commit matching parent/tree/trailer,
with clean checkout. A commit subject or ticket footer alone never proves identity.
Contract 2 still supports one commit per contribution, not adoption of arbitrary
prior commits. `integration cancel --integration <id>` is permitted only before HEAD
moves, so evidence can be redone safely. Unknown history blocks, never resets Git.

Crash after preparation: commit with that trailer or cancel. Crash after commit:
fresh owner reads state and finishes the **same ID**, without another commit. Crash
after finish: replay or inspect the integrated state. Re-finishing an integrated ID
returns its commit without another integration event.

Parent children then have `linear.pending=true`. The caller writes Done/completed
in Linear, reads it back, and calls `linear ack --ticket <child> --commit <integratedSHA>
--state-id <observedUUID> --state-type completed`. Failed sync does not recode the
child. Standalone roots **cannot** be acknowledged at integration; they close only
on delivery. Keep cross-ticket references out of commits/PRs; correlate in Linear.

### Final verification

The final integration enters verifying, even if Linear sync is still pending.
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
Tests/review are targeted. Commit using the returned trailer, `repair finish
--integration <id>`, then update the full verification report and all final captures
for the new HEAD. `repair cancel` works only before committing, like contribution cancel.

`verification complete` requires all contributions and Linear sync complete, all
planned criteria/tests/surfaces passed, independent review, no open/accepted findings,
all final visual captures published for the current HEAD, and resolved decisions.
It seals the branch as verified. `verification reopen --reason <fact>` returns a
verified/delivering/pr-open branch to QA before a later repair; old PR identity remains.

### Delivery and suite

`delivery start --base <target>` requires the gate and clean checkout before push.
GitHub origin, target branch and subsequent PR identity are bound to the campaign.
The caller pushes, creates/reuses a single PR, and records **unmodified** output of:

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
No local branch removal until suite/release, so the bound checkout remains resumable.

`suite show --campaign <id>` returns only deferred entries and current pr-open/delivered
state; it does not reread historical reports. Publish the registry and `suite publish
--reference <document>` under the live lease, then release. The CLI cannot attest
root closure in Linear; the delivery skill reads/writes it idempotently after merge.

## Scope and history

`campaign block --class scope-local|scope-transversal|environment --reason <fact>`;
uncertain extent is transversal. Scope resume needs `--decision-ref <datedComment>`
and `--reason <resolution>`. A transversal scope also requires a plan revision +1,
with matching decisionReference. A local replan can change only the active child.
Integrated contracts/order stay immutable; before root repairs a parent plan may add
a corrective contribution, afterward preserve contributions and use root repairs.
`campaign stop --reason <user-request>` records explicit user stop; resume requires a
resolution. Neither command constitutes asking the user: the skill must actually ask.
Finish/cancel a prepared child/root integration before blocking.

## Events, compatibility and limits

Every successful new operation atomically stores a receipt and local event. Envelope:
schemaVersion=1, eventId, type, operationId, occurredAt UTC, pipeline=atelier-next,
campaignId, rootTicketId, ticketId, skillName, mode=standalone|parent|child, sessionId,
integer campaign revision and data. Administrative roles use orchestration, contribution
roles realisation, final roles verification/livraison/suite. Types (all atelier-next:):
campaign-created, campaign-updated, contribution-started, contribution-integrated,
campaign-blocked, campaign-resumed, verification-completed, pr-linked, delivery-completed,
suite-published. Replays emit nothing; new observations of prior integration use updated.

**Cloud transport and detailed Next dashboard remain separate**, allowed by the product
plan. The transactional journal is retained locally and readable through events. No new
Next type is injected into the old cloud outbox/whitelist before consumer compatibility;
old forge/skill/ship telemetry is unaffected. Future shipment must deduplicate eventId,
order by revision and acknowledge before journal compaction.

Limits: 8 MiB per state/staging file, 128 contributions/tests, 256 findings, 4096
operations/events, 4096 bytes per text field. Capacity failures do not evict evidence.
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
wrong PR, ambiguous CI and merge guards. These are technical simulations, not claims
of completed business-ticket/LLM pilots. Run real pilots before replacing old Atelier.

Release order: source PR and green checks/review, merge, tag `atelierd-0.17.0`, compute
the actual GitHub tarball SHA, update/test the formula, then publish the plugin. Never
label the old tarball as a new source release or invent a checksum. No daemon replacement
is necessary to publish; users upgrade when ready.
