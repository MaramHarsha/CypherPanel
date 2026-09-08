// Package fetch is a bounded HTTP GET both sides of the wire may hold
// (agent-updates.md §3.2). Its first caller is the agent's updater, which has
// to pull a release manifest, a signature and a binary over its own outbound
// HTTPS without acquiring a soft HTTP path on the way.
//
// It lives in pkg/ rather than being reused from core/updates for a structural
// reason: `go.work` declares ./agent, ./core and ./pkg as three modules, so
// agent/ cannot import core/updates. The caps and the redirect ceiling are
// therefore RE-IMPLEMENTED here, and a reader comparing the two should know
// that is deliberate rather than an oversight.
//
// One control is deliberately ABSENT: core/updates refuses a redirect towards
// a private address (threat-model §5.13). That is a PLANE-side control and does
// not transplant. An air-gapped fleet's mirror lives at a private address by
// definition, and refusing it agent-side would break the case agent updates
// exist to allow. What stands in its place is the signature the caller checks:
// the agent fetches from wherever the operator points it and runs nothing a
// baked-in key does not verify — a stronger bound than a destination check, and
// the only one that survives a mirror.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Defaults. A caller may lower any of them; none of them may be removed.
const (
	DefaultTimeout = 60 * time.Second
	// MaxRedirects matches the plane's ceiling. Three is enough for the
	// download.host → cdn → object-store shape a release asset actually takes.
	MaxRedirects = 3
)

// Sentinels, so a caller can tell a refusal from a transport failure
// (ENGINEERING rule 3).
var (
	ErrBodyTooLarge  = errors.New("fetch: body exceeds the size cap")
	ErrTooManyHops   = errors.New("fetch: too many redirects")
	ErrSchemeRefused = errors.New("fetch: only http and https are fetched")
)

// Client is a bounded HTTP getter. The zero value is not usable; call New.
type Client struct {
	http      *http.Client
	userAgent string
}

// New builds a client with one timeout covering the whole exchange — dial,
// TLS, headers and body — and a redirect ceiling. A zero timeout takes
// DefaultTimeout.
func New(userAgent string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		userAgent: userAgent,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= MaxRedirects {
					return ErrTooManyHops
				}
				return checkURL(req.URL)
			},
		},
	}
}

// Get reads at most maxBytes from rawURL. A body larger than the cap is an
// error rather than a truncation: a caller about to hash what it read must
// never hash a prefix and call it a match.
func (c *Client) Get(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	body, err := c.open(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	buf, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch: reading body: %w", err)
	}
	if int64(len(buf)) > maxBytes {
		return nil, ErrBodyTooLarge
	}
	return buf, nil
}

// Stream hands the caller a reader capped at maxBytes, for a body too large to
// hold in memory — a binary. The caller closes it, and must treat a read that
// reaches exactly maxBytes as a body over the cap rather than a complete one.
func (c *Client) Stream(ctx context.Context, rawURL string, maxBytes int64) (io.ReadCloser, error) {
	body, err := c.open(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return capped{r: io.LimitReader(body, maxBytes), c: body}, nil
}

type capped struct {
	r io.Reader
	c io.Closer
}

func (c capped) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c capped) Close() error               { return c.c.Close() }

func (c *Client) open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch: parsing %q: %w", rawURL, err)
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: building request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()
		// The status only. Whatever prose a mirror puts in an error page is not
		// ours to copy into an operator-facing detail.
		return nil, fmt.Errorf("fetch: %s answered %d", u.Redacted(), resp.StatusCode)
	}
	return resp.Body, nil
}

// checkURL is the shape check every hop passes, first request included.
// Credentials and a fragment are shapes a legitimate artifact URL never has, so
// refusing them costs nothing and removes two ways to smuggle something past
// whoever reads the configured base.
func checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %s", ErrSchemeRefused, u.Scheme)
	}
	if u.User != nil {
		return errors.New("fetch: a url with credentials is refused")
	}
	if u.Fragment != "" {
		return errors.New("fetch: a url with a fragment is refused")
	}
	return nil
}
