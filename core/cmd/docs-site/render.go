package main

// The three page shapes canvas turn 19 draws: the home (19a), the guide article
// (19b) and the API endpoint (19c). The search overlay (19d) is markup in the
// shell plus assets/search.js, because it is opened from every page.
//
// html/template rather than string concatenation, so a document title carrying
// an ampersand cannot break the page — and the page bodies goldmark produced
// are marked template.HTML exactly once, at the one place that knows they are
// already rendered.

import (
	"embed"
	"fmt"
	"html/template"
	"strings"
)

//go:embed assets/docs.css assets/search.js
var assets embed.FS

// site is everything a template can see.
type site struct {
	Nav     []navSection
	APINav  []apiSection
	Version string
	Counts  counts
	ADRs    []page
	// The handful of URLs the chrome and the home page link to by name. They
	// are COMPUTED from the nav rather than written into the templates, so a
	// document that moves between sections cannot leave a dead link in the top
	// bar — and site_test.go walks every generated page to prove none exists.
	SelfHostURL   string
	QuickstartURL string
	MigrateURL    string
	ProductionURL string
}

type navSection struct {
	Title string
	Items []navItem
}

type navItem struct {
	Title   string
	URL     string
	Summary string
	Active  bool
}

type apiSection struct {
	Title string
	Items []apiNavItem
}

type apiNavItem struct {
	Method string
	Path   string
	URL    string
	Active bool
}

// counts are COMPUTED, never typed (documentation-site.md §3). Canvas 19a
// prints "47 features, 12 decisions, 6 references" on the page; if the numbers
// were literals the contents block could claim a number the site does not have.
type counts struct {
	Guides, Decisions, References, Endpoints int
}

func (c counts) Sentence() string {
	return fmt.Sprintf("every feature in the panel has a page — %d guides, %d decisions, %d references",
		c.Guides, c.Decisions, c.References)
}

// tmpl holds the parsed templates. Split into a shell plus three bodies so the
// top bar, the search overlay and the theme toggle exist once.
var tmpl = template.Must(template.New("shell").Funcs(template.FuncMap{
	"raw":         func(s string) template.HTML { return template.HTML(s) }, //nolint:gosec // goldmark output, from this repository's own docs
	"methodClass": func(m string) string { return "m-" + strings.ToLower(m) },
	"adrNumber":   adrNumber,
	"adrTitle":    adrTitle,
	"urlFor":      urlOf,
}).Parse(shellHTML + homeHTML + articleHTML + endpointHTML + apiIndexHTML))

// adrNumber and adrTitle split "ADR-005: Desired-state reconciliation" into the
// ordinal canvas 19a sets in mono and the sentence beside it. Derived from the
// file name and the h1 rather than stored, so a thirteenth ADR needs no edit.
func adrNumber(rel string) string {
	base := slugOf(rel)
	parts := strings.SplitN(base, "-", 3)
	if len(parts) >= 2 && parts[0] == "adr" {
		return parts[1]
	}
	return ""
}

func adrTitle(title string) string {
	if _, rest, ok := strings.Cut(title, ":"); ok {
		return strings.TrimSpace(rest)
	}
	// "ADR-005 — Desired-state reconciliation" is the other shape in the tree.
	if _, rest, ok := strings.Cut(title, "—"); ok {
		return strings.TrimSpace(rest)
	}
	return title
}

// view is what a template sees. The body is rendered first and handed to the
// shell as already-safe HTML: html/template cannot take a template name from a
// variable, and a switch in the shell would list every page shape twice.
type view struct {
	Site        *site
	Title       string
	Description string
	Tab         string
	EditURL     string
	FeedbackURL string
	Body        template.HTML

	// article
	Page        *page
	Prev        *navItem
	Next        *navItem
	ReadMinutes int

	// endpoint
	Endpoint        *endpoint
	EndpointTitle   string
	DescriptionHTML string
	// EndpointNav is THIS endpoint's tag only, and OtherTags is a count per
	// remaining tag. Canvas 19c draws exactly that — one group expanded, then
	// "DATABASES · SERVERS · TEAMS · 31 more endpoints" — and it is also what
	// keeps 276 sidebar entries out of 276 pages.
	EndpointNav []apiSection
	OtherTags   []tagCount
}

type tagCount struct {
	Title string
	Count int
	URL   string
}

// render executes one body template inside the shell.
func render(body string, v *view) ([]byte, error) {
	var inner strings.Builder
	if err := tmpl.ExecuteTemplate(&inner, body, v); err != nil {
		return nil, fmt.Errorf("docs-site: rendering %s: %w", body, err)
	}
	v.Body = template.HTML(inner.String()) //nolint:gosec // our own template output
	var out strings.Builder
	if err := tmpl.ExecuteTemplate(&out, "shell", v); err != nil {
		return nil, fmt.Errorf("docs-site: rendering the shell for %s: %w", body, err)
	}
	return []byte(out.String()), nil
}

const shellHTML = `{{define "shell"}}<!doctype html>
<html lang="en" data-theme="">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · CypherPanel docs</title>
<meta name="description" content="{{.Description}}">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Instrument+Sans:wght@400;500;600;700&family=Fragment+Mono&display=swap" rel="stylesheet">
<link rel="stylesheet" href="/docs.css">
<script>
/* Before first paint, so a dark reader never sees a white flash. The stored
   choice wins; with none, the system decides — the panel's own rule. */
(function(){try{var t=localStorage.getItem("cp-docs-theme");
if(t==="dark"||t==="light"){document.documentElement.dataset.theme=t;}}catch(e){}})();
</script>
</head>
<body>
<header class="topbar">
  <a class="wordmark" href="/">Cypher<span>Panel</span> <b>docs</b></a>
  <nav class="tabs">
    <a href="/"{{if eq .Tab "guides"}} class="on"{{end}}>Guides</a>
    <a href="/api/"{{if eq .Tab "api"}} class="on"{{end}}>API</a>
    <a href="{{.Site.SelfHostURL}}"{{if eq .Tab "selfhost"}} class="on"{{end}}>Self-host</a>
    <a href="https://github.com/MaramHarsha/CypherPanel/blob/main/CHANGELOG.md">Changelog</a>
  </nav>
  <div class="topright">
    <button class="searchbtn" data-search-open aria-label="Search the docs">⌕ Search the docs<kbd>/</kbd></button>
    <span class="version" title="This site documents one version">{{.Site.Version}}</span>
    <button class="themebtn" data-theme-toggle aria-label="Switch between light and dark"><span class="in-light">☾</span><span class="in-dark">☀</span></button>
    <a class="ghlink" href="https://github.com/MaramHarsha/CypherPanel">GitHub ↗</a>
  </div>
</header>
{{.Body}}
<div class="searchveil" data-search-veil hidden>
  <div class="searchbox" role="dialog" aria-modal="true" aria-label="Search the docs">
    <div class="searchhead"><span class="mag">⌕</span><input data-search-input type="search" placeholder="Search guides, decisions and endpoints" autocomplete="off" spellcheck="false"><kbd>ESC</kbd></div>
    <div class="searchresults" data-search-results></div>
    <div class="searchfoot"><span>↑↓ move</span><span>↵ open</span><span data-search-count></span></div>
  </div>
</div>
<footer class="sitefoot">
  <span>CypherPanel · Apache-2.0 · {{.Site.Version}}</span>
  <span>Generated from the repository's own documentation. <a href="{{.EditURL}}">Edit this page ↗</a></span>
</footer>
<script src="/search.js" defer></script>
</body>
</html>{{end}}`

// ── 19a: the docs home ──────────────────────────────────────────────────────

const homeHTML = `{{define "home"}}{{$s := .Site}}
<section class="hero">
  <div class="heroleft">
    <div class="eyebrow">DOCUMENTATION · APACHE-2.0 · {{$s.Version}}</div>
    <h1>Deploy on your own servers.</h1>
    <p>One binary, one database, and an agent that dials home. These pages cover installing the panel, connecting a server, and shipping your first app — then every API route behind it.</p>
  </div>
  <div class="heroright">
    <div class="eyebrow">INSTALL THE PANEL</div>
    <div class="cmd" data-copy="curl -fsSL https://cypherpanel.in/install | sh"><span class="dollar">$</span><code>curl -fsSL cypherpanel.in/install | sh</code><button class="copy">copy</button></div>
    <div class="herocta">
      <a class="btn primary" href="{{$s.SelfHostURL}}">Quickstart →</a>
      <a class="btn ghost" href="{{$s.MigrateURL}}">Migrate an existing panel</a>
    </div>
  </div>
</section>

<section class="startwhere">
  <div class="eyebrow">START WHERE YOU ARE</div>
  <div class="cards">
    <a class="card lead" href="{{$s.QuickstartURL}}">
      <h3>First deploy in 10 minutes</h3>
      <p>Install, join a server, connect a repo, get a URL with TLS. The golden path, nothing skipped.</p>
      <span class="cardmeta">GETTING STARTED →</span>
    </a>
    <a class="card" href="{{$s.ProductionURL}}">
      <h3>Run it in production</h3>
      <p>Backups you have restored, deploy protection, quotas, alerts, disaster recovery for the panel itself.</p>
      <span class="cardmeta quiet">OPERATE →</span>
    </a>
    <a class="card" href="/api/">
      <h3>Automate it</h3>
      <p>REST API, scoped tokens, signed outbound webhooks, and the OpenAPI spec every client is generated from.</p>
      <span class="cardmeta quiet">{{$s.Counts.Endpoints}} ENDPOINTS →</span>
    </a>
  </div>
</section>

<section class="contents">
  <div class="contentshead"><span class="eyebrow">COMPLETE CONTENTS</span><span class="note">{{$s.Counts.Sentence}}</span></div>
  <div class="contentsgrid">
    {{range $s.Nav}}<div class="contentsgroup">
      <h4>{{.Title}}</h4>
      <ul>{{range .Items}}<li><a href="{{.URL}}">{{.Title}}</a></li>{{end}}</ul>
    </div>{{end}}
  </div>
  <div class="adrblock">
    <div class="contentshead"><span class="eyebrow">DESIGN DECISIONS (ADR)</span><span class="note">why the panel is built this way — each one names what it rules out</span></div>
    <div class="adrgrid">
      {{range $s.ADRs}}<a class="adr" href="{{.URL}}"><span class="adrnum">{{adrNumber .Rel}}</span>{{adrTitle .Title}}</a>{{end}}
    </div>
  </div>
</section>
{{end}}`

// ── 19b: the guide article ──────────────────────────────────────────────────

const articleHTML = `{{define "article"}}{{$s := .Site}}
<div class="cols">
  <aside class="railleft">
    {{range $s.Nav}}<div class="railgroup">
      <div class="eyebrow">{{.Title}}</div>
      <ul>{{range .Items}}<li{{if .Active}} class="on"{{end}}><a href="{{.URL}}">{{.Title}}</a></li>{{end}}</ul>
    </div>{{end}}
    <div class="railgroup">
      <div class="eyebrow">DESIGN DECISIONS</div>
      <ul>{{range $s.ADRs}}<li{{if eq .URL $.Page.URL}} class="on"{{end}}><a href="{{.URL}}">{{adrTitle .Title}}</a></li>{{end}}</ul>
    </div>
  </aside>
  <main class="article">
    <div class="crumb">{{.Page.Group}} / <span>{{.Page.Title}}</span></div>
    <h1>{{.Page.Title}}</h1>
    <div class="articlemeta"><span>{{.Page.Section}}</span><span>·</span><span>{{.ReadMinutes}} MIN READ</span><span>·</span><a href="{{.EditURL}}">EDIT THIS PAGE ↗</a></div>
    <div class="prose">{{raw .Page.HTML}}</div>
    <nav class="prevnext">
      {{with .Prev}}<a class="pn prev" href="{{.URL}}"><span>PREVIOUS</span>← {{.Title}}</a>{{else}}<span></span>{{end}}
      {{with .Next}}<a class="pn next" href="{{.URL}}"><span>NEXT</span>{{.Title}} →</a>{{end}}
    </nav>
  </main>
  <aside class="railright">
    <div class="eyebrow">ON THIS PAGE</div>
    <ul class="onthispage">{{range .Page.Headings}}<li class="h{{.Level}}"><a href="#{{.ID}}">{{.Text}}</a></li>{{end}}</ul>
    <div class="useful">Was this page useful?<div><a class="btn tiny" href="{{.FeedbackURL}}&amp;title=docs%3A%20{{.Page.Title}}%20was%20useful">Yes</a><a class="btn tiny" href="{{.FeedbackURL}}&amp;title=docs%3A%20{{.Page.Title}}%20needs%20work">No</a></div></div>
  </aside>
</div>
{{end}}`

// ── 19c: one endpoint per page ──────────────────────────────────────────────

const endpointHTML = `{{define "endpoint"}}{{$s := .Site}}
<div class="cols api">
  <aside class="railleft">
    {{range .EndpointNav}}<div class="railgroup">
      <div class="eyebrow">{{.Title}}</div>
      <ul class="apilist">{{range .Items}}<li{{if .Active}} class="on"{{end}}><a href="{{.URL}}"><span class="{{methodClass .Method}}">{{.Method}}</span>{{.Path}}</a></li>{{end}}</ul>
    </div>{{end}}
    <div class="railgroup">
      <div class="eyebrow">EVERY OTHER GROUP</div>
      <ul class="othertags">{{range .OtherTags}}<li><a href="{{.URL}}">{{.Title}} <span class="quiet">{{.Count}}</span></a></li>{{end}}</ul>
      <a class="allapi" href="/api/">All {{$s.Counts.Endpoints}} endpoints →</a>
    </div>
  </aside>
  <main class="article">
    <div class="endpointhead">
      <span class="chip {{methodClass .Endpoint.Method}}">{{.Endpoint.Method}}</span>
      <span class="epath">{{.Endpoint.Path}}</span>
      {{with .Endpoint.Scope}}<span class="scope">scope: <b>{{.}}</b></span>{{end}}
    </div>
    <h1>{{.EndpointTitle}}</h1>
    <div class="prose">{{raw .DescriptionHTML}}</div>
    {{with .Endpoint.Params}}
    <div class="eyebrow tight">PARAMETERS</div>
    <div class="fieldtable">
      <div class="fthead"><span>NAME</span><span>IN</span><span>NOTES</span></div>
      {{range .}}<div class="ftrow"><span class="mono">{{.Name}}{{if .Required}}<i>*</i>{{end}}</span><span class="mono quiet">{{.In}}</span><span>{{.Description}}</span></div>{{end}}
    </div>{{end}}
    {{with .Endpoint.Body}}
    <div class="eyebrow tight">BODY</div>
    <div class="fieldtable">
      <div class="fthead"><span>FIELD</span><span>TYPE</span><span>NOTES</span></div>
      {{range .}}<div class="ftrow"><span class="mono">{{.Name}}{{if .Required}}<i>*</i>{{end}}</span><span class="mono quiet">{{.Type}}</span><span>{{.Notes}}</span></div>{{end}}
    </div>{{end}}
    {{with .Endpoint.Errors}}
    <div class="eyebrow tight">ERRORS</div>
    <ul class="errors">{{range .}}<li><span class="status s{{slice .Status 0 1}}">{{.Status}}</span><span>{{.Meaning}}</span></li>{{end}}</ul>{{end}}
  </main>
  <aside class="rail-ink">
    <div class="inkhead"><span>REQUEST</span><span class="quiet">cURL</span></div>
    <pre class="ink" data-copy="{{.Endpoint.Curl}}"><code>{{.Endpoint.Curl}}</code></pre>
    {{with .Endpoint.Sample}}
    <div class="inkhead"><span class="ok">RESPONSE</span></div>
    <pre class="ink"><code>{{.}}</code></pre>{{end}}
    <p class="inknote">Every response carries <code>X-Request-Id</code>, repeated as <code>trace_id</code> in error bodies — paste it into a bug report.</p>
  </aside>
</div>
{{end}}`

const apiIndexHTML = `{{define "apiindex"}}{{$s := .Site}}
<section class="hero">
  <div class="heroleft">
    <div class="eyebrow">REST API · {{$s.Counts.Endpoints}} ENDPOINTS</div>
    <h1>Every route the panel serves.</h1>
    <p>Generated from the OpenAPI document the binary itself serves at <code>/api/v1/openapi.yaml</code> — so this reference and the running panel cannot disagree. Authenticate with a bearer session token or a scoped API token.</p>
  </div>
  <div class="heroright">
    <div class="eyebrow">THE SPEC ITSELF</div>
    <div class="cmd" data-copy="curl -H &#34;Authorization: Bearer $CP_TOKEN&#34; https://panel.example.dev/api/v1/openapi.yaml"><span class="dollar">$</span><code>curl … /api/v1/openapi.yaml</code><button class="copy">copy</button></div>
    <div class="herocta"><a class="btn ghost" href="{{urlFor "features/api-tokens.md"}}">Tokens &amp; scopes</a><a class="btn ghost" href="{{urlFor "features/outbound-webhooks.md"}}">Webhooks</a></div>
  </div>
</section>
<section class="contents">
  <div class="contentsgrid api">
    {{range $s.APINav}}<div class="contentsgroup">
      <h4>{{.Title}}</h4>
      <ul class="apilist">{{range .Items}}<li><a href="{{.URL}}"><span class="{{methodClass .Method}}">{{.Method}}</span>{{.Path}}</a></li>{{end}}</ul>
    </div>{{end}}
  </div>
</section>
{{end}}`
