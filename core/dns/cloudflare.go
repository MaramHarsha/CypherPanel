package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// cloudflareAPI is the only destination this package talks to. Unlike an
// outbound webhook (threat-model §5.11) the address is a constant, not
// operator-supplied, so this feature adds no SSRF surface (§7 of the spec).
const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// Client is the provider seam. One implementation today; `kind` exists in the
// schema so a second is a new implementation of this interface rather than a
// migration (spec §2). The shape is deliberately the same five operations
// Dokploy's DnsClient settled on — concept only, never code (CLAUDE.md rule 1).
type Client interface {
	// ListAccounts returns the accounts the credential can see. Used to resolve
	// which account an account-owned token belongs to, so the operator picks
	// from a list instead of pasting an ID. A token without Account:Read cannot
	// call this, which is not an error — see Service.Set.
	ListAccounts(ctx context.Context) ([]Account, error)
	// ListZones returns every zone the credential can see, narrowed to one
	// account when accountID is set. This IS the ownership proof: a zone you
	// cannot list is a domain you cannot claim.
	ListZones(ctx context.Context, accountID string) ([]Zone, error)
	// VerifyToken checks the credential is live without reading anything else.
	// The endpoint differs by token type: an account-owned token verifies at
	// /accounts/{id}/tokens/verify, a user-owned one at /user/tokens/verify.
	VerifyToken(ctx context.Context, accountID string) error
	// FindRecord returns the record at (zone, name, type), or ok=false.
	FindRecord(ctx context.Context, zoneID, name, recordType string) (Record, bool, error)
	CreateRecord(ctx context.Context, zoneID string, r Record) (Record, error)
	UpdateRecord(ctx context.Context, zoneID, recordID string, r Record) (Record, error)
	// DeleteRecord removes a record. Deleting one that is already gone is a
	// SUCCESS, not an error — the caller's desired state is "absent" either way
	// (ENGINEERING rule 12).
	DeleteRecord(ctx context.Context, zoneID, recordID string) error
}

// Account is a Cloudflare account the credential can see. An account-owned
// token belongs to exactly one; a user-owned token may see several.
type Account struct {
	ID   string
	Name string
}

// Zone is a domain the credential is authoritative for.
type Zone struct {
	ProviderID string
	Name       string
	// Status is Cloudflare's activation state. Reported, never filtered on —
	// see ListZones.
	Status string
}

// Record is one DNS record as the provider sees it.
type Record struct {
	ProviderID string
	Type       string
	Name       string
	Content    string
	TTL        int
	Proxied    bool
}

// AuthError marks a credential the operator must fix — a bad token, or one
// missing a permission. REST maps it to 400 on save rather than 500, because it
// is input, not a fault.
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// ConflictError marks a record that exists at the provider with content that is
// not ours. We never silently overwrite an operator's own DNS (spec §4.4), so
// this surfaces as a named state the operator resolves.
type ConflictError struct{ Name, Existing string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("a %s record already exists with a different value (%s); remove it in Cloudflare or choose another hostname", e.Name, e.Existing)
}

type cloudflare struct {
	token string
	http  *http.Client
	// base is PARSED, not a string to concatenate onto. Every request is built
	// with URL.JoinPath and url.Values from it, so the scheme and host are
	// structure rather than convention: no operator-supplied value can move a
	// request off this host, and this client carries a bearer token that must
	// never be sent anywhere else (threat-model §5.12).
	base *url.URL
}

// NewCloudflare builds a client for one API token. The token is held in memory
// for the life of the call chain only — it is unsealed per operation by the
// service and never stored on a long-lived struct the logger can reach.
func NewCloudflare(token string) Client {
	// cloudflareAPI is a compile-time constant, so this cannot fail; parsing it
	// once here is what lets every call site build a URL structurally.
	base, _ := url.Parse(cloudflareAPI)
	return &cloudflare{
		token: token,
		base:  base,
		http: &http.Client{
			Timeout: 15 * time.Second,
			// Cloudflare never redirects its API; following one would be a way
			// to send a bearer token somewhere else.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// cfEnvelope is Cloudflare's uniform response wrapper.
type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Chain carries the specific reason behind a generic code. Cloudflare
	// answers a malformed token with 6003 "Invalid request headers" and puts
	// the useful half — "Invalid format for Authorization header" — in here.
	Chain []cfError `json:"error_chain"`
}

// message renders the provider's own words. The operator needs to know whether
// it is a bad token or a missing permission, and paraphrasing would make them
// guess (spec §5.1).
func (e cfEnvelope) message() string {
	parts := make([]string, 0, len(e.Errors))
	for _, x := range e.Errors {
		msg := x.Message
		// Prefer the chained reason: "Invalid format for Authorization header"
		// tells the operator what to fix; "Invalid request headers" does not.
		for _, c := range x.Chain {
			if c.Message != "" {
				msg = c.Message
			}
		}
		if msg != "" {
			parts = append(parts, msg)
		}
	}
	if len(parts) == 0 {
		return "Cloudflare rejected the request"
	}
	return strings.Join(parts, "; ")
}

// credentialCodes are the Cloudflare error codes that mean "the problem is your
// token", whatever HTTP status they arrive with. Cloudflare answers a malformed
// token with 400 rather than 401, so classifying on status alone reports a
// credential problem as an unreachable API — which sends the operator looking
// at their network instead of their token.
var credentialCodes = map[int]bool{
	6003:  true, // invalid request headers (malformed Authorization)
	6111:  true, // invalid format for Authorization header
	9109:  true, // invalid access token
	10000: true, // authentication error
}

// isCredentialProblem reports whether any error in the envelope, at any depth,
// is about the credential.
func (e cfEnvelope) isCredentialProblem() bool {
	var walk func([]cfError) bool
	walk = func(errs []cfError) bool {
		for _, x := range errs {
			if credentialCodes[x.Code] || walk(x.Chain) {
				return true
			}
		}
		return false
	}
	return walk(e.Errors)
}

// do issues one API call and unwraps the envelope. The token goes in a header,
// never a query string, so it cannot land in an intermediary's access log.
//
// The URL is assembled from path SEGMENTS and query VALUES rather than a
// formatted string. JoinPath escapes each segment (a "/" in an id becomes %2F,
// so it cannot open a new path element) and Values.Encode escapes both halves
// of every parameter. That is what makes "this request goes to Cloudflare"
// something the types enforce instead of something the caller remembers —
// CodeQL flagged the previous string-concatenated form as request forgery, and
// it was right to: a client holding a bearer token is exactly where an
// attacker-influenced URL turns into a leaked credential.
func (c *cloudflare) do(ctx context.Context, method string, segments []string, query url.Values, body any, out any) error {
	// Validate before joining. URL.JoinPath CLEANS the path it builds, so a
	// segment of "../../.." is not escaped into harmlessness — it is resolved,
	// and an operator-supplied account id could redirect the call to a
	// different Cloudflare endpoint with this client's bearer token attached.
	// Escaping was never the control here; rejecting is.
	for _, seg := range segments {
		if err := checkSegment(seg); err != nil {
			return err
		}
	}
	u := c.base.JoinPath(segments...)
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	// Defense in depth, and a tripwire for a future refactor: JoinPath cannot
	// change the host, so this can only fire if someone changes how the URL is
	// built. Failing closed here is much cheaper than posting a token to
	// somewhere it was never meant to go.
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host {
		return fmt.Errorf("dns: refusing to send a credential to %s", u.Host)
	}
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("dns: encoding request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return fmt.Errorf("dns: building request: %w", redactURL(err))
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dns: calling Cloudflare: %w", redactURL(err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded: a provider that answers with something enormous must not be able
	// to make the plane allocate it (threat-model §5.9).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("dns: reading Cloudflare response: %w", redactURL(err))
	}

	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("dns: Cloudflare returned %s with an unreadable body", resp.Status)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || env.isCredentialProblem() {
		return &AuthError{Msg: env.message()}
	}
	if !env.Success {
		return errors.New(env.message())
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("dns: decoding Cloudflare result: %w", err)
		}
	}
	return nil
}

// checkSegment rejects anything that would change the SHAPE of the path rather
// than fill one element of it. Every interpolated segment is an identifier — a
// Cloudflare account, zone or record id — so this is a narrow allowance, not a
// blocklist: no separators, no dot-segments, nothing empty, and nothing outside
// the characters an id actually uses.
func checkSegment(seg string) error {
	if seg == "" || seg == "." || seg == ".." {
		return fmt.Errorf("dns: %q is not a usable identifier", seg)
	}
	for _, r := range seg {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.'
		if !ok {
			return fmt.Errorf("dns: %q is not a usable identifier", seg)
		}
	}
	return nil
}

// redactURL strips the request URL out of a transport error before it is
// returned and later logged — the same defense core/notify uses, for the same
// reason: a *url.Error's message embeds the URL (ENGINEERING rule 20).
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (c *cloudflare) ListAccounts(ctx context.Context) ([]Account, error) {
	var out []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	q := url.Values{"per_page": {"50"}}
	if err := c.do(ctx, http.MethodGet, []string{"accounts"}, q, nil, &out); err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(out))
	for _, a := range out {
		accounts = append(accounts, Account{ID: a.ID, Name: a.Name})
	}
	return accounts, nil
}

func (c *cloudflare) ListZones(ctx context.Context, accountID string) ([]Zone, error) {
	var out []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	// per_page=50 is Cloudflare's documented maximum for this endpoint (their
	// OpenAPI schema caps it there); an operator with more zones than that is
	// beyond what one panel-wide token should be managing, and §9 records paging
	// as out of scope for this slice.
	//
	// account.id is the filter name Cloudflare uses — dotted, not account_id.
	// Narrowing to the account an account-owned token belongs to is what makes
	// the zone list mean "the zones this panel may manage" rather than "every
	// zone this credential happens to reach".
	//
	// There is deliberately NO status filter. Asking for status=active excluded
	// every zone whose nameservers had not been repointed yet, so an operator
	// who had just added their domain to Cloudflare was told the panel could see
	// no zones at all. Activation is not ownership; it is reported instead
	// (§3.2). Only `moved` is dropped, below, because that zone has genuinely
	// left this provider.
	q := url.Values{"per_page": {"50"}}
	if accountID != "" {
		q.Set("account.id", accountID)
	}
	if err := c.do(ctx, http.MethodGet, []string{"zones"}, q, nil, &out); err != nil {
		return nil, err
	}
	zones := make([]Zone, 0, len(out))
	for _, z := range out {
		if z.Status == domain.DNSZoneMoved {
			continue
		}
		zones = append(zones, Zone{ProviderID: z.ID, Name: z.Name, Status: z.Status})
	}
	return zones, nil
}

// VerifyToken calls the verify endpoint that matches the token type. Getting
// this wrong is a real failure mode: an account-owned token calling
// /user/tokens/verify is rejected, which would report a perfectly good
// credential as broken.
func (c *cloudflare) VerifyToken(ctx context.Context, accountID string) error {
	segments := []string{"user", "tokens", "verify"}
	if accountID != "" {
		segments = []string{"accounts", accountID, "tokens", "verify"}
	}
	return c.do(ctx, http.MethodGet, segments, nil, nil, nil)
}

type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl,omitempty"`
	Proxied bool   `json:"proxied"`
}

func (r cfRecord) toRecord() Record {
	return Record{ProviderID: r.ID, Type: r.Type, Name: r.Name, Content: r.Content, TTL: r.TTL, Proxied: r.Proxied}
}

func (c *cloudflare) FindRecord(ctx context.Context, zoneID, name, recordType string) (Record, bool, error) {
	var out []cfRecord
	q := url.Values{"type": {recordType}, "name": {name}}
	if err := c.do(ctx, http.MethodGet, []string{"zones", zoneID, "dns_records"}, q, nil, &out); err != nil {
		return Record{}, false, err
	}
	if len(out) == 0 {
		return Record{}, false, nil
	}
	return out[0].toRecord(), true, nil
}

func (c *cloudflare) CreateRecord(ctx context.Context, zoneID string, r Record) (Record, error) {
	var out cfRecord
	body := cfRecord{Type: r.Type, Name: r.Name, Content: r.Content, TTL: r.TTL, Proxied: r.Proxied}
	if err := c.do(ctx, http.MethodPost, []string{"zones", zoneID, "dns_records"}, nil, body, &out); err != nil {
		return Record{}, err
	}
	return out.toRecord(), nil
}

func (c *cloudflare) UpdateRecord(ctx context.Context, zoneID, recordID string, r Record) (Record, error) {
	var out cfRecord
	body := cfRecord{Type: r.Type, Name: r.Name, Content: r.Content, TTL: r.TTL, Proxied: r.Proxied}
	if err := c.do(ctx, http.MethodPut, []string{"zones", zoneID, "dns_records", recordID}, nil, body, &out); err != nil {
		return Record{}, err
	}
	return out.toRecord(), nil
}

func (c *cloudflare) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	err := c.do(ctx, http.MethodDelete, []string{"zones", zoneID, "dns_records", recordID}, nil, nil, nil)
	if err != nil && isAlreadyGone(err) {
		// Desired state is "absent" and it is absent. That is convergence, not
		// a failure (ENGINEERING rule 12).
		return nil
	}
	return err
}

// isAlreadyGone recognises Cloudflare's "record does not exist" so a delete of
// something already deleted converges instead of retrying forever.
func isAlreadyGone(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "record does not exist") ||
		strings.Contains(msg, "could not be found") ||
		strings.Contains(msg, "not found")
}
