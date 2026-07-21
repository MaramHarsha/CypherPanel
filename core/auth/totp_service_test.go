package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The full second-factor lifecycle: enroll (inactive) → login still works
// without a code → verify activates it and yields recovery codes → login now
// demands a code → a live code and a recovery code both satisfy it → disable
// needs a valid factor and then removes the requirement.
func TestTOTPLifecycle(t *testing.T) {
	a, fs := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()
	const uid = "usr_1"

	// Enroll: secret issued, but 2FA not yet active — login needs no code.
	enr, err := a.EnrollTOTP(ctx, uid, "sam@example.com")
	if err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	if enr.Secret == "" || enr.URI == "" {
		t.Fatal("enrollment missing secret/uri")
	}
	if _, _, err := a.Login(ctx, "sam@example.com", "pw", "", "ip"); err != nil {
		t.Fatalf("login during pending enrollment should succeed: %v", err)
	}

	// A wrong code does not activate.
	if _, err := a.VerifyTOTP(ctx, uid, "000000"); !errors.Is(err, ErrInvalidTOTPCode) {
		t.Fatalf("verify wrong code: %v", err)
	}
	st, _ := a.TOTPStatus(ctx, uid)
	if st.Enabled {
		t.Fatal("2FA active after a failed verify")
	}

	// Verify with the real code activates and returns recovery codes.
	code, _ := totpCode(enr.Secret, time.Now())
	recovery, err := a.VerifyTOTP(ctx, uid, code)
	if err != nil {
		t.Fatalf("VerifyTOTP: %v", err)
	}
	if len(recovery) != recoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(recovery), recoveryCodeCount)
	}
	// Refresh the cached user so Login sees TOTPEnabled.
	u := fs.users["sam@example.com"]
	u.TOTPEnabled = true
	fs.users["sam@example.com"] = u

	// Login now requires a second factor.
	if _, _, err := a.Login(ctx, "sam@example.com", "pw", "", "ip"); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("login without code: %v, want ErrTOTPRequired", err)
	}
	if _, _, err := a.Login(ctx, "sam@example.com", "pw", "000000", "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login with wrong code: %v, want ErrInvalidCredentials", err)
	}
	code, _ = totpCode(enr.Secret, time.Now())
	if _, _, err := a.Login(ctx, "sam@example.com", "pw", code, "ip"); err != nil {
		t.Fatalf("login with valid code: %v", err)
	}

	// A recovery code logs in once, then is spent.
	if _, _, err := a.Login(ctx, "sam@example.com", "pw", recovery[0], "ip"); err != nil {
		t.Fatalf("login with recovery code: %v", err)
	}
	if _, _, err := a.Login(ctx, "sam@example.com", "pw", recovery[0], "ip"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("reused recovery code should fail: %v", err)
	}
	st, _ = a.TOTPStatus(ctx, uid)
	if st.RecoveryCodesLeft != recoveryCodeCount-1 {
		t.Fatalf("recovery codes left = %d, want %d", st.RecoveryCodesLeft, recoveryCodeCount-1)
	}

	// Disable requires a valid factor.
	if err := a.DisableTOTP(ctx, uid, "000000"); !errors.Is(err, ErrInvalidTOTPCode) {
		t.Fatalf("disable with wrong code: %v", err)
	}
	code, _ = totpCode(enr.Secret, time.Now())
	if err := a.DisableTOTP(ctx, uid, code); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	st, _ = a.TOTPStatus(ctx, uid)
	if st.Enabled || st.RecoveryCodesLeft != 0 {
		t.Fatalf("after disable: %+v", st)
	}
}

func TestEnrollRefusedWhenAlreadyEnabled(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	ctx := context.Background()
	enr, _ := a.EnrollTOTP(ctx, "usr_1", "sam@example.com")
	code, _ := totpCode(enr.Secret, time.Now())
	if _, err := a.VerifyTOTP(ctx, "usr_1", code); err != nil {
		t.Fatalf("VerifyTOTP: %v", err)
	}
	if _, err := a.EnrollTOTP(ctx, "usr_1", "sam@example.com"); !errors.Is(err, ErrTOTPAlreadyEnabled) {
		t.Fatalf("re-enroll while enabled: %v, want ErrTOTPAlreadyEnabled", err)
	}
}

func TestVerifyBeforeEnrollFails(t *testing.T) {
	a, _ := newAuthWithUser(t, "sam@example.com", "pw")
	if _, err := a.VerifyTOTP(context.Background(), "usr_1", "123456"); !errors.Is(err, ErrTOTPNotEnrolled) {
		t.Fatalf("verify before enroll: %v, want ErrTOTPNotEnrolled", err)
	}
}
