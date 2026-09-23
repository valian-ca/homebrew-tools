package next

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func assertOperationalStateOnly(t *testing.T, f *fixture) {
	t.Helper()
	data, err := os.ReadFile(f.s.path(f.id))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["events"]; exists {
		t.Fatal("campaign contains a telemetry journal")
	}
	if len(raw["operations"]) == 0 {
		t.Fatal("idempotency receipts lost")
	}
	entries, err := os.ReadDir(f.s.root)
	if err != nil || len(entries) != 2 || entries[0].Name() != ".lock" || entries[1].Name() != "campaigns" {
		t.Fatal("unexpected Next storage", entries, err)
	}
	entries, err = os.ReadDir(filepath.Join(f.s.root, "campaigns"))
	if err != nil || len(entries) != 1 || entries[0].Name() != f.id+".json" {
		t.Fatal("unexpected campaign sidecar", entries, err)
	}
	if _, err := os.Stat(filepath.Join(f.sandbox, ".atelier")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy storage touched", err)
	}
}

func TestReplayWithoutTelemetryDoesNotWriteState(t *testing.T) {
	f := setup(t, "standalone")
	r := f.request("renew")
	receipt := f.apply(r)
	f.apply(f.request("release"))
	before, err := os.ReadFile(f.s.path(f.id))
	if err != nil {
		t.Fatal(err)
	}
	f.s, err = Open()
	if err != nil {
		t.Fatal(err)
	}
	if f.apply(r) != receipt {
		t.Fatal("lost response cannot be recovered after releasing ownership")
	}
	_ = f.status()
	if id, err := f.s.Find(f.ctx, rootTicket.Identifier, ""); err != nil || id != f.id {
		t.Fatal(id, err)
	}
	after, err := os.ReadFile(f.s.path(f.id))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("replay or read rewrote campaign state", err)
	}
	assertOperationalStateOnly(t, f)
}

func TestReceiptIntegrityWithoutEventJournal(t *testing.T) {
	for _, kind := range []string{"missing", "digest", "key", "campaign", "operation", "duplicate-revision", "zero-revision", "future-revision"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, "standalone")
			f.apply(f.request("renew"))
			c := f.status()
			op := c.Operations["op-2"]
			switch kind {
			case "missing":
				delete(c.Operations, "op-1")
			case "digest":
				op.Digest = "not-a-fingerprint"
			case "key":
				delete(c.Operations, "op-1")
				bad := c.Operations["op-2"]
				bad.Receipt.OperationID, bad.Receipt.Revision = "#invalid", 1
				c.Operations["#invalid"] = bad
			case "campaign":
				op.Receipt.CampaignID = "wrong-campaign"
			case "operation":
				op.Receipt.OperationID = "wrong-operation"
			case "duplicate-revision":
				op.Receipt.Revision = 1
			case "zero-revision":
				op.Receipt.Revision = 0
			case "future-revision":
				op.Receipt.Revision = c.Revision + 1
			}
			c.Operations["op-2"] = op
			if err := validateState(c, f.id); !errors.Is(err, ErrInvalid) {
				t.Fatal("corrupt receipt accepted", err)
			}
		})
	}
}

func TestScopeResolutionSurvivesWithoutTelemetry(t *testing.T) {
	f := setup(t, "standalone")
	f.plan(rootTicket)
	f.open(rootTicket)
	r := f.request("block")
	r.Class, r.Reason = "scope-local", "ambiguous behavior"
	f.apply(r)
	r = f.request("resume")
	r.Reason, r.DecisionReference = "user confirmed the original behavior", "linear-scope-decision"
	f.apply(r)
	var err error
	f.s, err = Open()
	if err != nil {
		t.Fatal(err)
	}
	c := f.status()
	b := c.Blocks[0]
	if c.State != "realizing" || b.Reason != "ambiguous behavior" || b.Resolution != r.Reason || b.DecisionReference != r.DecisionReference || b.ResolvedAt == nil {
		t.Fatal("operational scope decision lost", b)
	}
	c.Blocks[0].Resolution = ""
	if err := validateState(c, f.id); !errors.Is(err, ErrInvalid) {
		t.Fatal("resolved block without resolution accepted", err)
	}
	assertOperationalStateOnly(t, f)
}

func TestUnpublishedEventStateIsNotSilentlyRewritten(t *testing.T) {
	f := setup(t, "standalone")
	data, err := os.ReadFile(f.s.path(f.id))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["events"] = json.RawMessage(`[{"type":"atelier-next:campaign-created"}]`)
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.s.path(f.id), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Status(f.ctx, f.id); !errors.Is(err, ErrInvalid) {
		t.Fatal("unpublished event-bearing state adopted", err)
	}
	f.reject(f.request("renew"), ErrInvalid)
	after, err := os.ReadFile(f.s.path(f.id))
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("old experimental state changed", err)
	}
}
