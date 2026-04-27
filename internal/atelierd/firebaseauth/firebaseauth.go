// Package firebaseauth wraps the Firebase Auth REST endpoints atelierd needs:
// custom-token sign-in (during `atelierd link`) and idToken refresh (every
// expiry-5min in `atelierd run`).
//
// Note on revocation: the Firebase Auth REST API does not expose a
// user-facing refresh-token revocation endpoint — that capability lives only
// in the Admin SDK. `atelierd unlink` therefore deletes credentials locally
// and skips the REST call; AC 9's "best-effort" wording covers this.
package firebaseauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/app"
)

// AuthError wraps an HTTP error from a Firebase Auth REST call. The status
// code is the discriminator the run loop uses to tell transient (5xx,
// network) from terminal (401/403 on refresh = auth-lost).
type AuthError struct {
	Status  int
	Message string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("firebaseauth: HTTP %d: %s", e.Status, e.Message)
}

// IsAuthLost reports whether err means "the refresh token is invalid" — i.e.
// the daemon should enter auth-lost mode rather than retry. 401/403 only;
// 5xx and network errors are transient.
func IsAuthLost(err error) bool {
	var ae *AuthError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden
}

// SignInResult is the subset of the signInWithCustomToken response we need.
type SignInResult struct {
	UID              string
	Email            string
	IDToken          string
	RefreshToken     string
	IDTokenExpiresAt time.Time
}

type signInRequest struct {
	Token             string `json:"token"`
	ReturnSecureToken bool   `json:"returnSecureToken"`
}

// signInResponse mirrors the documented signInWithCustomToken response, which
// only carries idToken, refreshToken, and expiresIn — not localId or email.
// The uid + email are recovered by decoding the idToken JWT claims; see
// claimsFromIDToken below.
type signInResponse struct {
	IDToken      string `json:"idToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    string `json:"expiresIn"`
}

// idTokenClaims is the subset of Firebase Auth idToken JWT claims we need.
// The token is signed by Google; we only base64-decode the payload — without
// signature verification — because we just received it from the Identity
// Platform REST endpoint over TLS in the same call. Trusting the round-trip
// is fine for our purposes (extracting display fields).
type idTokenClaims struct {
	UserID string `json:"user_id"`
	Sub    string `json:"sub"`
	Email  string `json:"email"`
}

// SignInWithCustomToken exchanges a Firebase custom token (returned by
// `exchangeDeviceCode`) for an idToken + refreshToken pair.
func SignInWithCustomToken(ctx context.Context, customToken string) (*SignInResult, error) {
	body, err := json.Marshal(signInRequest{Token: customToken, ReturnSecureToken: true})
	if err != nil {
		return nil, fmt.Errorf("marshal signin: %w", err)
	}
	endpoint := app.IdentityToolkitURL + "/accounts:signInWithCustomToken?key=" + url.QueryEscape(app.FirebaseAPIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("build signin request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("signin request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &AuthError{Status: resp.StatusCode, Message: string(raw)}
	}

	var parsed signInResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode signin response: %w", err)
	}
	expiresAt, err := computeExpiry(parsed.ExpiresIn)
	if err != nil {
		return nil, err
	}
	claims, err := claimsFromIDToken(parsed.IDToken)
	if err != nil {
		return nil, fmt.Errorf("decode idToken claims: %w", err)
	}
	return &SignInResult{
		UID:              claims.uid(),
		Email:            claims.Email,
		IDToken:          parsed.IDToken,
		RefreshToken:     parsed.RefreshToken,
		IDTokenExpiresAt: expiresAt,
	}, nil
}

// uid prefers the Firebase-canonical `user_id` claim and falls back to the
// JWT-standard `sub` claim, which Identity Platform also populates with the
// Firebase uid.
func (c idTokenClaims) uid() string {
	if c.UserID != "" {
		return c.UserID
	}
	return c.Sub
}

// claimsFromIDToken extracts the payload segment of a Firebase idToken JWT
// and parses the fields we need. The token is `header.payload.signature`,
// each segment base64url-encoded without padding.
func claimsFromIDToken(idToken string) (idTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return idTokenClaims{}, errors.New("idToken does not have three segments")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idTokenClaims{}, fmt.Errorf("base64 decode payload: %w", err)
	}
	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return idTokenClaims{}, fmt.Errorf("unmarshal claims: %w", err)
	}
	return claims, nil
}

// RefreshResult is the subset of the securetoken refresh response we need.
type RefreshResult struct {
	IDToken          string
	RefreshToken     string
	IDTokenExpiresAt time.Time
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    string `json:"expires_in"`
	UserID       string `json:"user_id"`
}

// RefreshIDToken trades a refresh token for a new idToken (and possibly a
// rotated refresh token). On 401/403 the caller should enter auth-lost; on
// 5xx / network the caller should retry with backoff.
func RefreshIDToken(ctx context.Context, refreshToken string) (*RefreshResult, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, app.SecureTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &AuthError{Status: resp.StatusCode, Message: string(raw)}
	}

	var parsed refreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	expiresAt, err := computeExpiry(parsed.ExpiresIn)
	if err != nil {
		return nil, err
	}
	return &RefreshResult{
		IDToken:          parsed.IDToken,
		RefreshToken:     parsed.RefreshToken,
		IDTokenExpiresAt: expiresAt,
	}, nil
}

func computeExpiry(expiresIn string) (time.Time, error) {
	secs, err := strconv.Atoi(expiresIn)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse expiresIn %q: %w", expiresIn, err)
	}
	return time.Now().UTC().Add(time.Duration(secs) * time.Second), nil
}
