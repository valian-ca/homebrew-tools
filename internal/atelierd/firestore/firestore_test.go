package firestore

import (
	"strings"
	"testing"
)

func TestUserHeartbeatWrites(t *testing.T) {
	t.Parallel()
	writes := userHeartbeatWrites("uid-123", "0.7.0")
	if len(writes) != 1 {
		t.Fatalf("got %d writes, want 1", len(writes))
	}
	w := writes[0]

	update, ok := w["update"].(map[string]any)
	if !ok {
		t.Fatalf("write has no update map: %#v", w)
	}
	name, _ := update["name"].(string)
	if !strings.HasSuffix(name, "/documents/users/uid-123") {
		t.Fatalf("update targets %q, want it to end at /users/uid-123", name)
	}

	fields, ok := update["fields"].(map[string]any)
	if !ok {
		t.Fatalf("update has no fields map: %#v", update)
	}
	version, ok := fields["atelierdVersion"].(map[string]any)
	if !ok {
		t.Fatalf("missing atelierdVersion field: %#v", fields)
	}
	if version["stringValue"] != "0.7.0" {
		t.Fatalf("atelierdVersion = %#v, want stringValue 0.7.0", version)
	}

	mask, ok := w["updateMask"].(map[string]any)
	if !ok {
		t.Fatalf("write has no updateMask: %#v", w)
	}
	paths, ok := mask["fieldPaths"].([]string)
	if !ok || len(paths) != 1 || paths[0] != "atelierdVersion" {
		t.Fatalf("updateMask.fieldPaths = %#v, want [atelierdVersion]", mask["fieldPaths"])
	}

	transforms, ok := w["updateTransforms"].([]map[string]any)
	if !ok || len(transforms) != 1 {
		t.Fatalf("updateTransforms = %#v, want one entry", w["updateTransforms"])
	}
	tr := transforms[0]
	if tr["fieldPath"] != "lastHeartbeat" || tr["setToServerValue"] != "REQUEST_TIME" {
		t.Fatalf("transform = %#v, want lastHeartbeat=REQUEST_TIME", tr)
	}
}
