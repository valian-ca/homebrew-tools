package firestore

import (
	"errors"
	"net/http"
	"testing"
)

func TestIsAuthLostAndIsPermissionDenied(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		err            error
		wantAuthLost   bool
		wantPermDenied bool
	}{
		{"nil", nil, false, false},
		// 401 means the bearer token was rejected — auth-lost.
		{"401 unauthorized", &Error{Status: http.StatusUnauthorized}, true, false},
		// 403 means the token is valid but this write is forbidden by rules
		// (e.g. an /events doc that already exists) — permission denied, NOT
		// auth-lost. This separation is the crux of the fix.
		{"403 forbidden", &Error{Status: http.StatusForbidden}, false, true},
		{"500 internal", &Error{Status: http.StatusInternalServerError}, false, false},
		{"non-firestore error", errors.New("boom"), false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsAuthLost(tc.err); got != tc.wantAuthLost {
				t.Errorf("IsAuthLost(%v) = %v, want %v", tc.err, got, tc.wantAuthLost)
			}
			if got := IsPermissionDenied(tc.err); got != tc.wantPermDenied {
				t.Errorf("IsPermissionDenied(%v) = %v, want %v", tc.err, got, tc.wantPermDenied)
			}
		})
	}
}
