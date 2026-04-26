// Package deviceauth wraps the two device-link callables atelierd invokes
// during `atelierd link`:
//
//   - createDeviceCode  — registers a fresh code on the backend and stores
//     {host, code, expiresAt:now+5min}.
//   - exchangeDeviceCode — polled until the user enters the code in the
//     dashboard (which fires linkDeviceCode server-side
//     to attach the uid). Returns {linked: true, customToken}
//     once linked.
//
// Both routes opt out of App Check (see backend api-zod-route.ts:39) since the
// daemon has no Firebase identity at this stage. The whole api surface is a
// single Cloud Function (`api`) that routes internally via CallableRouter on
// the {type, value} envelope shape — see backend api/core/callable-router.ts.
package deviceauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/app"
)

// CallError wraps a non-200 response from the api callable.
type CallError struct {
	Status  int
	Message string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("deviceauth: HTTP %d: %s", e.Status, e.Message)
}

// callableEndpoint is the single api callable hosting every backend route.
var callableEndpoint = app.CallableBaseURL + "/dashboards-api"

// callableRequest mirrors the {data: {type, value}} envelope Firebase callables
// expect. The CallableRouter then dispatches on `type`.
type callableRequest struct {
	Data callableData `json:"data"`
}

type callableData struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// callableResponse matches Firebase callables' {result: ...} wrapper for
// successful responses.
type callableResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

// CreateDeviceCode registers a new device code on the backend with the given
// host (typically os.Hostname()).
//
// On HTTP 409-equivalent ("already-exists" code from the callable), callers
// should regenerate the code and retry once.
func CreateDeviceCode(ctx context.Context, code, host string) error {
	_, err := call(ctx, "createDeviceCode", map[string]any{
		"code": code,
		"host": host,
	})
	return err
}

// ExchangeDeviceCodeResult is the discriminated union the callable returns.
type ExchangeDeviceCodeResult struct {
	Linked      bool
	CustomToken string // populated only when Linked is true
}

// ExchangeDeviceCode polls once. Returns Linked=false until the user has
// entered the code in the dashboard; once linked, returns Linked=true and a
// custom token to be exchanged via firebaseauth.SignInWithCustomToken.
func ExchangeDeviceCode(ctx context.Context, code string) (*ExchangeDeviceCodeResult, error) {
	raw, err := call(ctx, "exchangeDeviceCode", map[string]any{"code": code})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Linked      bool   `json:"linked"`
		CustomToken string `json:"customToken,omitempty"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse exchangeDeviceCode response: %w", err)
	}
	out := &ExchangeDeviceCodeResult{Linked: parsed.Linked}
	if parsed.Linked {
		if parsed.CustomToken == "" {
			return nil, errors.New("exchangeDeviceCode returned linked=true with empty customToken")
		}
		out.CustomToken = parsed.CustomToken
	}
	return out, nil
}

// IsCodeAlreadyExists reports whether err means the backend rejected the code
// because another active code shares the same value (a rare collision worth
// retrying once with a freshly-generated code).
func IsCodeAlreadyExists(err error) bool {
	var ce *CallError
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Status == http.StatusConflict ||
		(ce.Status == http.StatusBadRequest && bytes.Contains([]byte(ce.Message), []byte("already-exists")))
}

func call(ctx context.Context, route string, value any) (json.RawMessage, error) {
	body, err := json.Marshal(callableRequest{Data: callableData{Type: route, Value: value}})
	if err != nil {
		return nil, fmt.Errorf("marshal callable: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callableEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build callable request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("callable request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, &CallError{Status: resp.StatusCode, Message: string(raw)}
	}
	var parsed callableResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode callable response: %w", err)
	}
	if parsed.Error != nil {
		return nil, &CallError{Status: http.StatusBadRequest, Message: parsed.Error.Status + ": " + parsed.Error.Message}
	}
	return parsed.Result, nil
}
