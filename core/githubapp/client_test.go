package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func testKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

// A malformed key is the operator's to fix and must surface when they SAVE it,
// not when a build runs an hour later — the same stance core/dns takes for a
// Cloudflare token that cannot list zones.
func TestABadPrivateKeyIsRefusedAtConstructionNotAtBuildTime(t *testing.T) {
	for _, bad := range []string{"", "not a pem", "-----BEGIN RSA PRIVATE KEY-----\nZm9v\n-----END RSA PRIVATE KEY-----"} {
		if _, err := NewClient(Config{AppID: 1, PrivateKeyPEM: bad}); err == nil {
			t.Fatalf("NewClient accepted %q", bad)
		}
	}
	if _, err := NewClient(Config{AppID: 1, PrivateKeyPEM: testKeyPEM(t)}); err != nil {
		t.Fatalf("NewClient rejected a good key: %v", err)
	}
}

// The assertion GitHub checks. iat is backdated deliberately: GitHub rejects a
// token whose iat is in its future, and a control plane's clock is not
// guaranteed to agree with theirs.
func TestTheAppAssertionIsBackdatedAndShortLived(t *testing.T) {
	c, err := NewClient(Config{AppID: 4242, PrivateKeyPEM: testKeyPEM(t)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok, err := c.appJWT(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", tok)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "4242" {
		t.Fatalf("iss = %q, want the app id", claims.Iss)
	}
	if claims.Iat >= now.Unix() {
		t.Fatalf("iat is not backdated: %d vs now %d", claims.Iat, now.Unix())
	}
	// GitHub's ceiling is ten minutes and it refuses anything longer.
	if d := time.Duration(claims.Exp-claims.Iat) * time.Second; d > 10*time.Minute {
		t.Fatalf("the assertion lives %s, over GitHub's ten-minute ceiling", d)
	}
}

// Tokens are cached in memory and evicted BEFORE GitHub expires them, so a
// build that starts at 59 minutes does not clone with a credential that dies
// mid-fetch (github-app.md §4).
func TestACachedTokenIsEvictedBeforeItExpires(t *testing.T) {
	c, err := NewClient(Config{AppID: 1, PrivateKeyPEM: testKeyPEM(t)})
	if err != nil {
		t.Fatal(err)
	}
	// Valid, but inside the eviction skew: it must NOT be reused.
	c.tokens[7] = cachedToken{value: "nearly-dead", expiresAt: time.Now().Add(tokenSkew / 2)}
	if _, err := c.Token(t.Context(), 7); err == nil {
		t.Fatal("a token inside the eviction window was reused instead of re-minted")
	}
	// Comfortably valid: reused without touching the network, which is what
	// makes the absence of an error here the assertion.
	c.tokens[8] = cachedToken{value: "healthy", expiresAt: time.Now().Add(time.Hour)}
	got, err := c.Token(t.Context(), 8)
	if err != nil || got != "healthy" {
		t.Fatalf("Token = %q, %v; want the cached value", got, err)
	}
}

// The clone credential's shape is fixed by GitHub: a constant username and the
// token as the password. Asserting it here is what stops someone "tidying" it
// into the URL, which is where it would be baked into an image layer.
func TestTheCloneCredentialPutsTheTokenInThePassword(t *testing.T) {
	user, pass := CloneCredential("ghs_secret")
	if user != "x-access-token" {
		t.Fatalf("username = %q", user)
	}
	if pass != "ghs_secret" {
		t.Fatalf("password = %q", pass)
	}
}
