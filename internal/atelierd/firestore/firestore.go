// Package firestore is a thin REST client for the two Firestore operations
// the daemon performs as the authenticated end user:
//
//  1. Bulk-write events to /events/{ulid} via the :commit endpoint.
//  2. Bump /users/{uid}.lastHeartbeat to serverTimestamp via the same endpoint
//     (using the fieldTransforms.setToServerValue=REQUEST_TIME idiom — the
//     literal serverTimestamp() semantics AC 14 mandates).
//
// We use REST + Bearer idToken (rather than the Firestore Go SDK) because the
// SDK assumes Application Default Credentials and fights any attempt to
// authenticate as a user. REST gives us total control with stdlib only.
package firestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/app"
)

// Error wraps an HTTP error from Firestore REST, carrying the status so the
// caller can distinguish auth-lost (401/403) from transient (5xx, network).
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("firestore: HTTP %d: %s", e.Status, e.Message)
}

// IsAuthLost reports whether err means the bearer token was rejected.
func IsAuthLost(err error) bool {
	var fe *Error
	if !errors.As(err, &fe) {
		return false
	}
	return fe.Status == http.StatusUnauthorized || fe.Status == http.StatusForbidden
}

// EventDoc is the shape persisted at /events/{ulid}. Mirrors EventZod in
// common/schema/src/atelier/event-zod.ts.
type EventDoc struct {
	ULID            string         `json:"ulid"`
	Type            string         `json:"type"`
	ClaudeSessionID string         `json:"claudeSessionId"`
	UID             string         `json:"uid"`
	Host            string         `json:"host"`
	TS              time.Time      `json:"ts"`
	Payload         map[string]any `json:"payload"`
}

// CommitEvents writes len(events) /events/{ulid} documents atomically via a
// single :commit batch. Order is preserved.
func CommitEvents(ctx context.Context, idToken string, events []*EventDoc) error {
	if len(events) == 0 {
		return nil
	}
	writes := make([]map[string]any, 0, len(events))
	for _, e := range events {
		writes = append(writes, map[string]any{
			"update": map[string]any{
				"name":   "projects/" + app.FirebaseProjectID + "/databases/(default)/documents/events/" + e.ULID,
				"fields": encodeEventFields(e),
			},
		})
	}
	return commit(ctx, idToken, writes)
}

// SetUserHeartbeat writes /users/{uid}.lastHeartbeat to serverTimestamp
// (REQUEST_TIME). The doc is expected to already exist (created at first login).
func SetUserHeartbeat(ctx context.Context, idToken, uid string) error {
	writes := []map[string]any{
		{
			"transform": map[string]any{
				"document": "projects/" + app.FirebaseProjectID + "/databases/(default)/documents/users/" + uid,
				"fieldTransforms": []map[string]any{
					{
						"fieldPath":        "lastHeartbeat",
						"setToServerValue": "REQUEST_TIME",
					},
				},
			},
		},
	}
	return commit(ctx, idToken, writes)
}

// PingUser performs a minimal read of /users/{uid} as a connectivity probe.
// `atelierd status` uses this to verify the bearer token is valid against
// Firestore (separately from the local credential file's freshness).
func PingUser(ctx context.Context, idToken, uid string) error {
	endpoint := app.UserDocumentURL(uid) + "?mask.fieldPaths=email"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+idToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		// 404 means the doc isn't there but the token was accepted — for the
		// purpose of a connectivity check that's still success.
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	return &Error{Status: resp.StatusCode, Message: string(raw)}
}

func commit(ctx context.Context, idToken string, writes []map[string]any) error {
	body, err := json.Marshal(map[string]any{"writes": writes})
	if err != nil {
		return fmt.Errorf("marshal commit: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, app.CommitURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build commit request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+idToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("commit request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return &Error{Status: resp.StatusCode, Message: string(raw)}
	}
	return nil
}

// encodeEventFields converts an EventDoc into the Firestore typed-value map
// the REST API expects: {"ulid": {"stringValue": "..."}, …}.
func encodeEventFields(e *EventDoc) map[string]any {
	return map[string]any{
		"ulid":            stringValue(e.ULID),
		"type":            stringValue(e.Type),
		"claudeSessionId": stringValue(e.ClaudeSessionID),
		"uid":             stringValue(e.UID),
		"host":            stringValue(e.Host),
		"ts":              timestampValue(e.TS),
		"payload":         mapValue(e.Payload),
	}
}

// EncodeValue is exported for testing — converts a Go value into the Firestore
// type-tagged JSON shape ({"stringValue": "..."}, {"integerValue": "42"}, etc.).
func EncodeValue(v any) map[string]any {
	return encodeValue(v)
}

func encodeValue(v any) map[string]any {
	switch t := v.(type) {
	case nil:
		return map[string]any{"nullValue": nil}
	case string:
		return stringValue(t)
	case bool:
		return map[string]any{"booleanValue": t}
	case int:
		return map[string]any{"integerValue": strconv.FormatInt(int64(t), 10)}
	case int64:
		return map[string]any{"integerValue": strconv.FormatInt(t, 10)}
	case float64:
		return map[string]any{"doubleValue": t}
	case time.Time:
		return timestampValue(t)
	case map[string]any:
		return mapValue(t)
	case []any:
		values := make([]map[string]any, 0, len(t))
		for _, item := range t {
			values = append(values, encodeValue(item))
		}
		return map[string]any{"arrayValue": map[string]any{"values": values}}
	default:
		// Fallback: stringify via JSON. Loses fidelity but never fails.
		raw, _ := json.Marshal(t)
		return stringValue(string(raw))
	}
}

func stringValue(s string) map[string]any {
	return map[string]any{"stringValue": s}
}

func timestampValue(t time.Time) map[string]any {
	return map[string]any{"timestampValue": t.UTC().Format(time.RFC3339Nano)}
}

func mapValue(m map[string]any) map[string]any {
	fields := make(map[string]any, len(m))
	for k, v := range m {
		fields[k] = encodeValue(v)
	}
	return map[string]any{"mapValue": map[string]any{"fields": fields}}
}
