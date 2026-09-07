package statuspage

// Serving the public page (status-pages.md §3).
//
// This is the first server-rendered HTML in cypherd, and threat-model §5.8
// specifically banks on there being none: "No server-side web framework to
// CVE." The exception is bounded so the property that sentence is really
// claiming still holds:
//
//   - ONE template, embedded, parsed once at init. No template is ever built
//     from data and none is ever parsed at request time.
//   - html/template contextual auto-escaping. Every interpolated value is
//     operator-authored text landing in a text node or a plain attribute —
//     never in a URL, script, style or srcset context. The page's own domain
//     is rendered as text, not as a link.
//   - NO REQUEST DATA REACHES THE TEMPLATE. Not the query string, not a
//     header, not the user agent. The only request-derived value is the slug,
//     which is used to look the page up and is never rendered.
//   - The response forbids script outright in its CSP.
//
// A page that ships no script and forbids script cannot be made to run one,
// which is most of what "no SSR" was buying. The reason for no JavaScript is
// not purity: this page is read when things are broken, and a document that
// needs a bundle, a fetch and a parse has three ways to fail where one
// response has one.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

//go:embed page.html
var pageHTML string

var pageTmpl = template.Must(template.New("status").Funcs(template.FuncMap{
	"barX":  func(i int) string { return strconv.Itoa(i * 10) },
	"stamp": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") },
}).Parse(pageHTML))

// The response headers. `default-src 'none'` with only inline style allowed is
// the whole policy: no script can load, none can be inlined, no frame can hold
// the page, and no form can post from it.
const csp = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

type view struct {
	PublicPage
	UpdatedLabel string
}

// Lookup finds an enabled page by its public slug. It is the only
// request-derived input this whole surface takes.
type Lookup interface {
	GetStatusPageBySlug(ctx context.Context, slug string) (domain.StatusPage, error)
}

// Server renders and caches public pages.
//
// The cache is the answer to §10.4: the plane is now in a public request path,
// so a link that goes viral costs one render per page per TTL and zero
// database work in between. It is not a correctness mechanism — an enabled
// page that is disabled is invisible for at most one TTL, which the spec
// states as the acceptance criterion rather than pretending is instant.
type Server struct {
	lookup Lookup
	reader Reader
	ttl    time.Duration
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	html []byte
	json []byte
	etag string
	at   time.Time
}

func NewServer(lookup Lookup, reader Reader, ttl time.Duration) *Server {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return &Server{lookup: lookup, reader: reader, ttl: ttl, now: time.Now, cache: map[string]cached{}}
}

// SetClock injects the clock (ENGINEERING rule 9).
func (s *Server) SetClock(now func() time.Time) { s.now = now }

// Routes registers the two public routes on a mux. They are the first routes
// in this product meant for an anonymous audience rather than for someone
// holding a secret, and they are deliberately NOT rate limited by client
// address the way sign-in and invitations are: a status page is read hardest
// exactly when it matters, and throttling the audience during an incident
// would be a self-inflicted outage of the outage page. The cache is the
// protection instead.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /status/{slug}", s.handleHTML)
	mux.HandleFunc("GET /status/{slug}/data.json", s.handleJSON)
}

func (s *Server) handleHTML(w http.ResponseWriter, r *http.Request) {
	c, ok := s.render(r.Context(), r.PathValue("slug"))
	if !ok {
		notFound(w, "text/html; charset=utf-8", []byte(missingHTML))
		return
	}
	s.write(w, r, c, "text/html; charset=utf-8", c.html)
}

func (s *Server) handleJSON(w http.ResponseWriter, r *http.Request) {
	c, ok := s.render(r.Context(), r.PathValue("slug"))
	if !ok {
		notFound(w, "application/json", []byte(`{"error":"not found"}`))
		return
	}
	s.write(w, r, c, "application/json", c.json)
}

// notFound answers one undifferentiated 404 for an unknown slug, a disabled
// page and a deleted page alike. Distinguishing them would let anyone
// enumerate which projects have a page and which have turned theirs off.
func notFound(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(body)
}

func (s *Server) write(w http.ResponseWriter, r *http.Request, c cached, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(s.ttl.Seconds())))
	w.Header().Set("ETag", c.etag)
	if match := r.Header.Get("If-None-Match"); match != "" && match == c.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(body)
}

func (s *Server) render(ctx context.Context, slug string) (cached, bool) {
	now := s.now()

	s.mu.Lock()
	if c, ok := s.cache[slug]; ok && now.Sub(c.at) < s.ttl {
		s.mu.Unlock()
		return c, true
	}
	s.mu.Unlock()

	page, err := s.lookup.GetStatusPageBySlug(ctx, slug)
	if err != nil || !page.Enabled {
		s.mu.Lock()
		delete(s.cache, slug)
		s.mu.Unlock()
		return cached{}, false
	}

	payload, err := Build(ctx, s.reader, page, now)
	if err != nil {
		// A status page that fails must not fail cheerful, and it must never
		// serve a stale cached green page in place of an error.
		return cached{}, false
	}

	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, view{PublicPage: payload, UpdatedLabel: ago(now, payload.UpdatedAt)}); err != nil {
		return cached{}, false
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return cached{}, false
	}

	c := cached{
		html: buf.Bytes(),
		json: body,
		etag: fmt.Sprintf(`W/"%s-%d"`, slug, now.Unix()/int64(s.ttl.Seconds())),
		at:   now,
	}
	s.mu.Lock()
	s.cache[slug] = c
	s.mu.Unlock()
	return c, true
}

// Invalidate drops one page from the cache, so an operator who turns a page
// off sees it gone rather than waiting out the TTL from the settings tab.
func (s *Server) Invalidate(slug string) {
	s.mu.Lock()
	delete(s.cache, slug)
	s.mu.Unlock()
}

func ago(now, then time.Time) string {
	d := now.Sub(then)
	if d < time.Minute {
		return "just now"
	}
	return humanDuration(d) + " ago"
}

// The 404 body. Plain, and it does not say whether a page exists.
const missingHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
	`<title>Not found</title><style>body{margin:0;display:grid;place-items:center;min-height:100vh;` +
	`font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;color:#55555f}</style>` +
	`</head><body><p>No status page here.</p></body></html>`
