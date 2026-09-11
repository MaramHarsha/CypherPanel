package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP mandates HMAC-SHA1; it is a keyed MAC here, never a plain hash, and is what every authenticator app implements.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TOTP parameters. Six digits, a 30-second step, and HMAC-SHA1 are the de-facto
// standard every authenticator app (Google Authenticator, 1Password, Aegis…)
// expects; deviating breaks interoperability for no security gain. RFC 6238.
const (
	totpDigits      = 6
	totpPeriod      = 30 * time.Second
	totpSecretBytes = 20 // 160-bit, the RFC 4226 §4 recommended key length
	totpSkew        = 1  // accept the adjacent steps (±30s) for clock drift
	totpIssuer      = "CypherPanel"
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns a fresh random secret, base32-encoded the way
// authenticator apps and the otpauth URI expect it.
func newTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generating totp secret: %w", err)
	}
	return totpEncoding.EncodeToString(buf), nil
}

// totpCode computes the RFC 6238 code for secret at time t. secret is base32
// (case-insensitive, unpadded).
func totpCode(secret string, t time.Time) (string, error) {
	key, err := totpEncoding.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("auth: decoding totp secret: %w", err)
	}
	counter := uint64(t.Unix()) / uint64(totpPeriod.Seconds())
	return hotp(key, counter, totpDigits), nil
}

// hotp is the RFC 4226 HMAC-based one-time password with dynamic truncation.
func hotp(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	truncated := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, truncated%mod)
}

// validTOTP reports whether code is valid for secret at now, tolerating a
// one-step clock skew on either side. The comparison is constant-time so a
// timing side channel cannot narrow the code space (ENGINEERING rule 21).
func validTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	ok := false
	for i := -totpSkew; i <= totpSkew; i++ {
		want, err := totpCode(secret, now.Add(time.Duration(i)*totpPeriod))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			ok = true // keep looping: don't leak the matching step via timing
		}
	}
	return ok
}

// totpURI builds the otpauth:// provisioning URI an authenticator app consumes
// (typically rendered as a QR code by the client — we never generate images
// server-side, keeping the plane's footprint small).
func totpURI(secret, account string) string {
	label := totpIssuer + ":" + account
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", totpIssuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", strconv.Itoa(totpDigits))
	v.Set("period", strconv.Itoa(int(totpPeriod.Seconds())))
	return "otpauth://totp/" + url.PathEscape(label) + "?" + v.Encode()
}
