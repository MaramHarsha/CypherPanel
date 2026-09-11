// Package githubapp is the panel's GitHub App: repository discovery and a
// short-lived clone credential (docs/features/github-app.md).
//
// TOKENS ARE MINTED, NEVER STORED. An installation access token lives one hour;
// storing one would mean storing a credential that is usually expired and
// occasionally valid, which is the worst of both. So the flow is: sign a short
// JWT with the App private key, exchange it for an installation token, use it,
// discard it. Tokens are cached in memory only and evicted before they expire,
// so a long build never carries one over the line.
//
// That is the security argument for this whole feature. A deploy key is a
// long-lived credential sitting in the database; an installation token does not
// exist until a build needs one.
package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors a handler maps to a status code (ENGINEERING rule 3).
var (
	// ErrNotConfigured: no App has been connected.
	ErrNotConfigured = errors.New("githubapp: no GitHub App is connected")
	// ErrBadKey: the PEM is not an RSA private key GitHub could have issued.
	ErrBadKey = errors.New("githubapp: that is not a valid RSA private key")
	// ErrAuth: GitHub rejected the App's own credentials — a wrong app id, a
	// key from a different App, or one that was regenerated.
	ErrAuth = errors.New("githubapp: GitHub rejected this App's credentials")
)

// apiBase is github.com's API. A GitHub Enterprise host is a later field on the
// config, not a constant to guess at now.
const apiBase = "https://api.github.com"

// Bounds on every call. The panel is an HTTP client here (threat-model §5.13),
// so the same shape its other outbound calls take: one timeout, a body cap.
const (
	callTimeout  = 15 * time.Second
	maxBodyBytes = 4 << 20
	// jwtTTL is well inside GitHub's ten-minute ceiling, with room for clock
	// skew on both sides.
	jwtTTL = 8 * time.Minute
	// tokenSkew evicts a cached token before GitHub expires it, so a build that
	// starts at 59 minutes does not clone with a credential that dies mid-fetch.
	tokenSkew = 5 * time.Minute
)

// Config is the App's own credentials. The private key is the whole risk (§2).
type Config struct {
	AppID int64
	Slug  string
	// PrivateKeyPEM is the PKCS#1 or PKCS#8 RSA key GitHub issued.
	PrivateKeyPEM string
	// WebhookSecret verifies the App's deliveries.
	WebhookSecret string
}

// Installation is one place the App is installed, as GitHub reports it.
type Installation struct {
	InstallationID int64
	AccountLogin   string
	AccountType    string
	RepoSelection  string
}

// Repository is one repository an installation can see.
type Repository struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	// Installation is filled in by the lister so a caller knows which
	// installation to mint a token from.
	Installation int64 `json:"-"`
}

// Client talks to GitHub as the App. Construct with NewClient.
type Client struct {
	cfg  Config
	key  *rsa.PrivateKey
	http *http.Client

	mu     sync.Mutex
	tokens map[int64]cachedToken
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

// NewClient parses the key once, at construction: a malformed PEM is the
// operator's to fix and must surface when they save it, not when a build runs.
func NewClient(cfg Config) (*Client, error) {
	key, err := parseKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:    cfg,
		key:    key,
		http:   &http.Client{Timeout: callTimeout},
		tokens: map[int64]cachedToken{},
	}, nil
}

func parseKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemText)))
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrBadKey)
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: the key is not RSA", ErrBadKey)
	}
	return key, nil
}

// appJWT signs the short-lived assertion that authenticates the App itself.
// RS256 by hand rather than a JWT library: it is a header, a claim set and one
// signature, and the dependency would be larger than the code.
func (c *Client) appJWT(now time.Time) (string, error) {
	header := base64URL([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64URL([]byte(fmt.Sprintf(
		// iat is backdated a minute: GitHub rejects a token whose iat is in its
		// future, and a control plane's clock is not guaranteed to agree.
		`{"iat":%d,"exp":%d,"iss":"%d"}`,
		now.Add(-time.Minute).Unix(), now.Add(jwtTTL).Unix(), c.cfg.AppID,
	)))
	signing := header + "." + claims
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: signing the app assertion: %w", err)
	}
	return signing + "." + base64URL(sig), nil
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// ListInstallations reads where the App is installed. This is the observation
// the cache is refreshed from (§3).
func (c *Client) ListInstallations(ctx context.Context) ([]Installation, error) {
	jwt, err := c.appJWT(time.Now())
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
		RepositorySelection string `json:"repository_selection"`
	}
	if err := c.do(ctx, http.MethodGet, "/app/installations?per_page=100", "Bearer "+jwt, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Installation, 0, len(raw))
	for _, r := range raw {
		out = append(out, Installation{
			InstallationID: r.ID,
			AccountLogin:   r.Account.Login,
			AccountType:    r.Account.Type,
			RepoSelection:  r.RepositorySelection,
		})
	}
	return out, nil
}

// Token returns a usable installation access token, minting one if the cached
// value is missing or close to expiry.
//
// This is the ONE place the private key is used for anything other than
// listing installations, and the one moment a clone credential exists.
func (c *Client) Token(ctx context.Context, installationID int64) (string, error) {
	c.mu.Lock()
	if t, ok := c.tokens[installationID]; ok && time.Now().Before(t.expiresAt.Add(-tokenSkew)) {
		c.mu.Unlock()
		return t.value, nil
	}
	c.mu.Unlock()

	jwt, err := c.appJWT(time.Now())
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	if err := c.do(ctx, http.MethodPost, path, "Bearer "+jwt, nil, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("githubapp: GitHub returned no token for installation %d", installationID)
	}
	c.mu.Lock()
	c.tokens[installationID] = cachedToken{value: out.Token, expiresAt: out.ExpiresAt}
	c.mu.Unlock()
	return out.Token, nil
}

// ListRepositories reads what one installation can see. Not cached in the
// database: it changes when someone adds a repository, and a stale list that
// omits the repository you just made is worse than a request (§5).
func (c *Client) ListRepositories(ctx context.Context, installationID int64) ([]Repository, error) {
	token, err := c.Token(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var out []Repository
	// Bounded rather than "until GitHub stops": a runaway pager against a
	// 10,000-repository org is a request storm the operator did not ask for.
	for page := 1; page <= 10; page++ {
		var body struct {
			Repositories []Repository `json:"repositories"`
		}
		path := fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page)
		if err := c.do(ctx, http.MethodGet, path, "token "+token, nil, &body); err != nil {
			return nil, err
		}
		for i := range body.Repositories {
			body.Repositories[i].Installation = installationID
		}
		out = append(out, body.Repositories...)
		if len(body.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

// CloneCredential is the token in the form git understands. The username is
// fixed by GitHub and the token is the password.
func CloneCredential(token string) (username, password string) {
	return "x-access-token", token
}

func (c *Client) do(ctx context.Context, method, path, authorization string, body io.Reader, out any) error {
	u, err := url.Parse(apiBase + path)
	if err != nil {
		return fmt.Errorf("githubapp: building request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return fmt.Errorf("githubapp: building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", authorization)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("githubapp: calling GitHub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// Named rather than generic: a wrong app id and a regenerated key are
		// the two mistakes an operator actually makes, and both land here.
		return fmt.Errorf("%w (HTTP %d)", ErrAuth, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The status, never the body: GitHub's error prose is not ours to
		// forward into a panel screen.
		return fmt.Errorf("githubapp: GitHub answered %d for %s", resp.StatusCode, path)
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("githubapp: reading GitHub's reply: %w", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("githubapp: decoding GitHub's reply: %w", err)
	}
	return nil
}
