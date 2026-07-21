package auth

import (
	"strings"
	"testing"
	"time"
)

// The RFC 6238 Appendix B reference vectors (SHA1, seed "12345678901234567890",
// 8 digits). Passing these proves the HOTP truncation and counter derivation are
// correct — the part it would be dangerous to get subtly wrong.
func TestHOTPMatchesRFC6238Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	cases := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, c := range cases {
		counter := uint64(c.unix) / uint64(totpPeriod.Seconds())
		if got := hotp(key, counter, 8); got != c.want {
			t.Errorf("hotp(t=%d) = %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestValidTOTPRoundtripAndSkew(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatalf("newTOTPSecret: %v", err)
	}
	now := time.Unix(1700000000, 0)

	code, err := totpCode(secret, now)
	if err != nil {
		t.Fatalf("totpCode: %v", err)
	}
	if len(code) != totpDigits {
		t.Fatalf("code %q is not %d digits", code, totpDigits)
	}
	if !validTOTP(secret, code, now) {
		t.Error("current code did not validate")
	}
	// Adjacent step accepted (clock drift tolerance).
	if !validTOTP(secret, code, now.Add(totpPeriod)) {
		t.Error("code from one step ago should validate within skew")
	}
	// Two steps away is outside the window.
	if validTOTP(secret, code, now.Add(2*totpPeriod+time.Second)) {
		t.Error("code two steps away should be rejected")
	}
	// Wrong code rejected; wrong length rejected.
	if validTOTP(secret, "000000", now) && code != "000000" {
		t.Error("a wrong code validated")
	}
	if validTOTP(secret, "123", now) {
		t.Error("a short code validated")
	}
}

func TestTOTPURICarriesProvisioningParams(t *testing.T) {
	uri := totpURI("ABCDEF", "sam@example.com")
	for _, want := range []string{"otpauth://totp/", "secret=ABCDEF", "issuer=CypherPanel", "digits=6", "period=30", "algorithm=SHA1"} {
		if !strings.Contains(uri, want) {
			t.Errorf("otpauth URI missing %q: %s", want, uri)
		}
	}
}
