package rest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Enrollment returns a usable secret + otpauth URI, and status reflects that
// 2FA is not yet active (enrollment must be confirmed with a code first).
func TestTOTPEnrollAndStatus(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/auth/totp", session, "")
	if status != http.StatusOK {
		t.Fatalf("status: %d %s", status, body)
	}
	var st struct {
		Enabled           bool `json:"enabled"`
		RecoveryCodesLeft int  `json:"recovery_codes_left"`
	}
	json.Unmarshal(body, &st)
	if st.Enabled {
		t.Fatal("2FA reported enabled before enrollment")
	}

	status, _, body = doJSON(t, "POST", ts.URL+"/api/v1/auth/totp/enroll", session, "")
	if status != http.StatusOK {
		t.Fatalf("enroll: %d %s", status, body)
	}
	var enr struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	json.Unmarshal(body, &enr)
	if enr.Secret == "" || !strings.HasPrefix(enr.URI, "otpauth://totp/") {
		t.Fatalf("bad enrollment payload: %s", body)
	}
}

func TestTOTPVerifyRejectsBadCode(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)
	doJSON(t, "POST", ts.URL+"/api/v1/auth/totp/enroll", session, "")

	// 400, not 401: the session is valid and only the factor is wrong. A 401
	// is indistinguishable from an expired session, and clients that sign the
	// operator out on 401 would log them out for a typo mid-enrollment.
	status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/totp/verify", session, `{"code":"000000"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("verify bad code: %d, want 400", status)
	}
	// The session must still work afterwards — that is the property that broke.
	if s, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/auth/me", session, ""); s != http.StatusOK {
		t.Fatalf("session invalidated by a wrong factor: /auth/me returned %d", s)
	}
}

func TestTOTPDisableWhenNotEnabled(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)
	status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/totp/disable", session, `{"code":"000000"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("disable when not enabled: %d, want 400", status)
	}
}

func TestTOTPRequiresAuth(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/auth/totp/enroll", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated enroll: %d, want 401", status)
	}
}
