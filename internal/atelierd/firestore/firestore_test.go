package firestore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/app"
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

func TestErrorClassifiers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		err            error
		wantAuthLost   bool
		wantPermDenied bool
		wantExists     bool
	}{
		{"nil", nil, false, false, false},
		{"401 unauthorized", &Error{Status: http.StatusUnauthorized}, true, false, false},
		// 403: the token is valid but the rules forbid this write — not auth-lost.
		{"403 forbidden", &Error{Status: http.StatusForbidden}, false, true, false},
		{"409 already exists", &Error{Status: http.StatusConflict}, false, false, true},
		{"500 internal", &Error{Status: http.StatusInternalServerError}, false, false, false},
		{"non-firestore error", errors.New("boom"), false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsAuthLost(tc.err); got != tc.wantAuthLost {
				t.Errorf("IsAuthLost(%v) = %v, want %v", tc.err, got, tc.wantAuthLost)
			}
			if got := IsPermissionDenied(tc.err); got != tc.wantPermDenied {
				t.Errorf("IsPermissionDenied(%v) = %v, want %v", tc.err, got, tc.wantPermDenied)
			}
			if got := IsAlreadyExists(tc.err); got != tc.wantExists {
				t.Errorf("IsAlreadyExists(%v) = %v, want %v", tc.err, got, tc.wantExists)
			}
		})
	}
}

func TestCommitEvents_CreateOnlyPreconditionAnd409(t *testing.T) {
	var body map[string]any
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	defer app.SetCommitURLForTest(srv.URL)()

	minute := time.Date(2026, 10, 10, 10, 3, 0, 0, time.UTC)
	doc := &EventDoc{ULID: "cs-1_202610101003", Type: "activity:minute", ClaudeSessionID: "cs-1", UID: "u", Host: "h", TS: minute, Payload: map[string]any{}}
	if err := CommitEvents(context.Background(), "tok", []*EventDoc{doc}); err != nil {
		t.Fatalf("CommitEvents: %v", err)
	}

	writes, _ := body["writes"].([]any)
	if len(writes) != 1 {
		t.Fatalf("writes = %v, want 1", body["writes"])
	}
	w := writes[0].(map[string]any)
	if pre, _ := w["currentDocument"].(map[string]any); pre["exists"] != false {
		t.Errorf("currentDocument = %v, want {exists: false}", w["currentDocument"])
	}
	update := w["update"].(map[string]any)
	if name, _ := update["name"].(string); !strings.HasSuffix(name, "/documents/events/cs-1_202610101003") {
		t.Errorf("doc name = %q", name)
	}
	fields := update["fields"].(map[string]any)
	if ts := fields["ts"].(map[string]any)["timestampValue"]; ts != "2026-10-10T10:03:00Z" {
		t.Errorf("ts = %v, want 2026-10-10T10:03:00Z", ts)
	}

	status = http.StatusConflict
	if err := CommitEvents(context.Background(), "tok", []*EventDoc{doc}); !IsAlreadyExists(err) {
		t.Fatalf("409 response: err = %v, want IsAlreadyExists", err)
	}
}
