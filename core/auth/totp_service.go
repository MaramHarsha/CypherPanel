package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Errors specific to the two-factor enrollment lifecycle.
var (
	ErrTOTPAlreadyEnabled = errors.New("auth: two-factor already enabled")
	ErrTOTPNotEnrolled    = errors.New("auth: two-factor not enrolled")
	ErrInvalidTOTPCode    = errors.New("auth: invalid two-factor code")
)

const recoveryCodeCount = 10

// TOTPEnrollment is the one-time enrollment payload: the secret to load into an
// authenticator app (raw and as a scannable otpauth URI). Enrollment is not
// active until VerifyTOTP confirms a code.
type TOTPEnrollment struct {
	Secret string
	URI    string
}

// TOTPStatus reports a user's second-factor state for the account UI.
type TOTPStatus struct {
	Enabled           bool
	RecoveryCodesLeft int
}

// EnrollTOTP generates a fresh secret for a user and stores it sealed, leaving
// two-factor inactive until VerifyTOTP. Re-enrolling before verification simply
// replaces the pending secret; enrolling while already enabled is refused so an
// active secret is never silently rotated out from under the user.
func (a *Authenticator) EnrollTOTP(ctx context.Context, user string, account string) (TOTPEnrollment, error) {
	sec, err := a.store.GetTOTPSecret(ctx, user)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	if sec.Enabled {
		return TOTPEnrollment{}, ErrTOTPAlreadyEnabled
	}

	secret, err := newTOTPSecret()
	if err != nil {
		return TOTPEnrollment{}, err
	}
	ct, nonce, err := a.box.Seal([]byte(secret))
	if err != nil {
		return TOTPEnrollment{}, fmt.Errorf("auth: sealing totp secret: %w", err)
	}
	if err := a.store.SetTOTPSecret(ctx, user, ct, nonce); err != nil {
		return TOTPEnrollment{}, err
	}
	// Clear any stale recovery codes from a previous enrollment attempt.
	if err := a.store.DeleteRecoveryCodes(ctx, user); err != nil {
		return TOTPEnrollment{}, err
	}
	return TOTPEnrollment{Secret: secret, URI: totpURI(secret, account)}, nil
}

// VerifyTOTP confirms a code against the pending secret, activates two-factor,
// and issues a fresh set of single-use recovery codes (returned once, stored
// hashed). It is idempotent-safe: a wrong code changes nothing.
func (a *Authenticator) VerifyTOTP(ctx context.Context, user, code string) (recoveryCodes []string, err error) {
	secret, sec, err := a.openTOTPSecret(ctx, user)
	if err != nil {
		return nil, err
	}
	if len(secret) == 0 {
		return nil, ErrTOTPNotEnrolled
	}
	if !validTOTP(secret, code, a.now()) {
		return nil, ErrInvalidTOTPCode
	}
	if !sec.Enabled {
		if err := a.store.EnableTOTP(ctx, user); err != nil {
			return nil, err
		}
	}
	return a.regenerateRecoveryCodes(ctx, user)
}

// DisableTOTP turns off two-factor, but only when the caller proves possession
// with a valid authenticator or recovery code — a stolen session alone cannot
// strip an account's second factor. Secret and recovery codes are cleared.
func (a *Authenticator) DisableTOTP(ctx context.Context, user, code string) error {
	sec, err := a.store.GetTOTPSecret(ctx, user)
	if err != nil {
		return err
	}
	if !sec.Enabled {
		return ErrTOTPNotEnrolled
	}
	ok, err := a.checkSecondFactor(ctx, user, code)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidTOTPCode
	}
	if err := a.store.DisableTOTP(ctx, user); err != nil {
		return err
	}
	return a.store.DeleteRecoveryCodes(ctx, user)
}

// TOTPStatus reports whether two-factor is enabled and how many recovery codes
// remain.
func (a *Authenticator) TOTPStatus(ctx context.Context, user string) (TOTPStatus, error) {
	sec, err := a.store.GetTOTPSecret(ctx, user)
	if err != nil {
		return TOTPStatus{}, err
	}
	left, err := a.store.CountUnusedRecoveryCodes(ctx, user)
	if err != nil {
		return TOTPStatus{}, err
	}
	return TOTPStatus{Enabled: sec.Enabled, RecoveryCodesLeft: left}, nil
}

// checkSecondFactor accepts either a valid live TOTP code or an unused recovery
// code (which it consumes). Used by Login and DisableTOTP.
func (a *Authenticator) checkSecondFactor(ctx context.Context, user, code string) (bool, error) {
	secret, _, err := a.openTOTPSecret(ctx, user)
	if err != nil {
		return false, err
	}
	if len(secret) > 0 && validTOTP(secret, code, a.now()) {
		return true, nil
	}
	// Fall back to a recovery code (single use).
	return a.store.ConsumeRecoveryCode(ctx, user, HashToken(normalizeRecoveryCode(code)))
}

// openTOTPSecret returns the decrypted base32 secret (empty string if none is
// stored) alongside the raw record.
func (a *Authenticator) openTOTPSecret(ctx context.Context, user string) (secret string, sec store.TOTPSecret, err error) {
	sec, err = a.store.GetTOTPSecret(ctx, user)
	if err != nil {
		return "", store.TOTPSecret{}, err
	}
	if len(sec.CT) == 0 {
		return "", sec, nil
	}
	pt, err := a.box.Open(sec.CT, sec.Nonce)
	if err != nil {
		return "", store.TOTPSecret{}, fmt.Errorf("auth: opening totp secret: %w", err)
	}
	return string(pt), sec, nil
}

// regenerateRecoveryCodes replaces a user's recovery codes with a fresh set,
// returning the plaintext once (only hashes are stored).
func (a *Authenticator) regenerateRecoveryCodes(ctx context.Context, user string) ([]string, error) {
	if err := a.store.DeleteRecoveryCodes(ctx, user); err != nil {
		return nil, err
	}
	codes := make([]string, 0, recoveryCodeCount)
	for range recoveryCodeCount {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		if err := a.store.AddRecoveryCode(ctx, ids.New(ids.PrefixRecoveryCode), user, HashToken(normalizeRecoveryCode(code))); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// newRecoveryCode returns a human-transcribable single-use code, formatted as
// two dash-separated base32 groups (e.g. "k7v2q-9dv8t").
func newRecoveryCode() (string, error) {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generating recovery code: %w", err)
	}
	raw := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
	return raw[:5] + "-" + raw[5:10], nil
}

// normalizeRecoveryCode canonicalizes a submitted code (case- and dash-
// insensitive) so a user can type it however they like.
func normalizeRecoveryCode(code string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}
