package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Image pinning. A bundled template has to resolve to the same software on
// every panel running a given CypherPanel release (template-catalog.md §6),
// and Coolify's library is full of mutable references — nearly half its images
// are `:latest`. A mutable tag is re-pulled on every deploy, so leaving one in
// the catalog means a routine redeploy can cross a major version with no
// operator involvement.
//
// So every image is rewritten to `repo@sha256:…`, resolved once here against
// the registry. The digest form is what the agent can prove: EnsureImage skips
// the pull when a digest reference is already local, precisely because those
// are provably the right bits, while a tag always has to be re-fetched.
//
// The human-readable version does not disappear — it moves to the template's
// `version` field, which is what the catalog UI shows.

// imageInfo is everything the registry can tell us about one reference: the
// immutable form to ship, the version to display, and the port the image says
// it serves on.
type imageInfo struct {
	pinned  string
	version string
	port    int
	err     error
}

// cacheEntry is imageInfo as it survives between runs. Registries rate-limit
// anonymous clients hard enough that re-resolving 200 images to re-run one
// changed mapping rule is the slowest part of the whole import, so successful
// answers are written beside the run and reused. Failures are never cached:
// a 429 is a statement about the moment, not about the image.
type cacheEntry struct {
	Pinned  string `json:"pinned"`
	Version string `json:"version,omitempty"`
	Port    int    `json:"port,omitempty"`
	// PortKnown separates "asked, exposes nothing usable" from "never asked".
	PortKnown bool `json:"port_known,omitempty"`
}

type imageCache struct {
	path    string
	mu      sync.Mutex
	entries map[string]*cacheEntry
	dirty   bool
}

func loadCache(path string) *imageCache {
	c := &imageCache{path: path, entries: map[string]*cacheEntry{}}
	if path == "" {
		return c
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied cache path
	if err != nil {
		return c
	}
	if err := json.Unmarshal(body, &c.entries); err != nil {
		c.entries = map[string]*cacheEntry{}
	}
	return c
}

func (c *imageCache) get(ref string) (*cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ref]
	return e, ok
}

// put records an answer and flushes immediately. A rate-limited run spends
// most of its time waiting, so an interruption that discarded an hour of
// resolved digests would be the expensive failure — writing a small JSON file
// per answer is not.
func (c *imageCache) put(ref string, e *cacheEntry) {
	c.mu.Lock()
	c.entries[ref] = e
	c.dirty = true
	c.mu.Unlock()
	if err := c.save(); err != nil {
		fmt.Fprintf(os.Stderr, "coolify-import: %v\n", err)
	}
}

func (c *imageCache) save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" || !c.dirty {
		return nil
	}
	body, err := json.MarshalIndent(c.entries, "", " ")
	if err != nil {
		return fmt.Errorf("encoding image cache: %w", err)
	}
	// Rename into place: a run killed mid-write must not leave a truncated
	// cache that the next run silently discards as unparseable.
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("writing image cache: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("replacing image cache: %w", err)
	}
	c.dirty = false
	return nil
}

// warmPorts resolves EXPOSE for a batch of images concurrently, filling the
// cache the oracle then reads. Concurrency does not evade the registry's
// quota — nothing can — but it does mean one rate-limited image waits
// alongside the others instead of in front of them.
func warmPorts(ctx context.Context, reg *registry, refs []string) {
	work := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range work {
				reg.portFor(ctx, ref)
			}
		}()
	}
	for _, ref := range refs {
		select {
		case work <- ref:
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return
		}
	}
	close(work)
	wg.Wait()
}

// applyPins rewrites a converted template's images to their immutable form and
// takes its display version from the routed application. A template with an
// unresolvable image becomes a rejection: shipping it with a mutable reference
// would defeat the point of bundling it.
//
// Resolution happens here, one template at a time, rather than for every image
// the sources mention. Most of Coolify's library does not convert, and asking
// a rate-limited registry about images destined for the report would spend the
// run's entire quota on templates nobody will install.
func applyPins(ctx context.Context, r *outcome, reg *registry) {
	for j := range r.tpl.Resources.Applications {
		a := &r.tpl.Resources.Applications[j]
		got := reg.resolve(ctx, a.Image)
		if got.err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %s unresolved (%v)\n", r.slug, a.Image, got.err)
			r.reasons = append(r.reasons,
				fmt.Sprintf("image %s could not be pinned to an immutable reference: %v", a.Image, got.err))
			continue
		}
		version := got.version
		if version == "" {
			// No release could be named for this digest — the registry offers
			// no tag listing, or every tag pointing at it moves. The tag the
			// import pinned from is still the most specific true thing we can
			// say, and a card reading "monitoring · 2" beats a bare category.
			// "latest" is the one tag that says nothing at all.
			if _, _, tag, _ := splitReference(a.Image); tag != "latest" {
				version = tag
			}
		}
		if a.Route || r.tpl.Version == "" {
			r.tpl.Version = version
		}
		a.Image = got.pinned
	}
}

type registry struct {
	http   *http.Client
	cache  *imageCache
	mu     sync.Mutex
	tokens map[string]string // "host|repository" -> bearer token
}

func newRegistry(cache *imageCache) *registry {
	return &registry{
		http:   &http.Client{Timeout: 60 * time.Second},
		cache:  cache,
		tokens: map[string]string{},
	}
}

const acceptManifests = "application/vnd.oci.image.index.v1+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

// resolve pins one reference to `repo@sha256:…` and reports the most specific
// version tag it could name it by.
func (r *registry) resolve(ctx context.Context, ref string) imageInfo {
	// A cached entry may hold only a port: the two lookups are independent and
	// either can be the first to answer. Reuse it only once it carries a pin.
	cached, _ := r.cache.get(ref)
	if cached != nil && cached.Pinned != "" {
		return imageInfo{pinned: cached.Pinned, version: cached.Version, port: cached.Port}
	}
	host, repo, tag, digest := splitReference(ref)
	if digest == "" {
		got, err := r.digest(ctx, host, repo, tag)
		if err != nil {
			return imageInfo{err: err}
		}
		digest = got
	}
	info := imageInfo{
		pinned:  canonicalName(host, repo) + "@" + digest,
		version: tagVersion(tag),
	}
	if info.version == "" {
		info.version = r.versionTagFor(ctx, host, repo, digest)
	}
	if cached == nil {
		cached = &cacheEntry{}
	}
	cached.Pinned, cached.Version = info.pinned, info.version
	r.cache.put(ref, cached)
	return info
}

// portFor answers the port oracle. Reading EXPOSE costs three more requests
// than a digest lookup — index, manifest, config blob — and registries count
// every one of them against an anonymous quota, so it is asked lazily and only
// for the images a template actually leaves portless, then cached.
func (r *registry) portFor(ctx context.Context, ref string) (int, bool) {
	if e, ok := r.cache.get(ref); ok && e.PortKnown {
		return e.Port, e.Port > 0
	}
	// Short fuse, unlike a digest lookup. A registry that is rate-limiting us
	// will still be rate-limiting us in ten minutes, and an image whose port
	// we never learn costs one template — while waiting for it costs every
	// template behind it. Giving up records nothing, so the next run (which
	// reads the same cache and so re-asks only the unanswered) picks it up
	// once quota has returned.
	portCtx, cancel := context.WithTimeout(ctx, portLookupBudget)
	defer cancel()

	host, repo, tag, digest := splitReference(ref)
	if digest == "" {
		digest = tag
	}
	port, ok := r.exposedPort(portCtx, host, repo, digest)
	if !ok {
		return 0, false
	}

	e, cached := r.cache.get(ref)
	if !cached {
		e = &cacheEntry{}
	}
	e.Port, e.PortKnown = port, true
	r.cache.put(ref, e)
	return port, port > 0
}

// portLookupBudget bounds one image's EXPOSE lookup end to end.
const portLookupBudget = 90 * time.Second

// exposedPort reads the image's own EXPOSE declaration — the same source
// Docker consults when a compose file names no port. Best-effort: an image
// that exposes several ports states no single answer, and guessing between
// them is exactly what this import refuses to do.
// The bool separates "the image says nothing usable" — a settled answer worth
// caching — from "the registry did not answer", which is worth asking again.
func (r *registry) exposedPort(ctx context.Context, host, repo, ref string) (int, bool) {
	// On Docker Hub the unmetered API already knows which child manifest is
	// linux/amd64, so start there rather than spending a metered request to
	// read the index.
	child := ""
	if isDockerHub(host) {
		if _, amd64, err := r.hubTag(ctx, repo, ref); err == nil {
			child = amd64
		}
	}
	if child == "" {
		index, err := r.manifest(ctx, host, repo, ref)
		if err != nil {
			return 0, false
		}
		child = ref
		// A multi-architecture index names no config of its own; follow it to
		// the linux/amd64 image, whose EXPOSE is the one every arch shares in
		// practice.
		if len(index.Manifests) > 0 {
			child = ""
			for _, m := range index.Manifests {
				if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
					child = m.Digest
					break
				}
			}
			if child == "" {
				return 0, true // no linux/amd64 image: settled, and unusable
			}
		}
	}
	manifest, err := r.manifest(ctx, host, repo, child)
	if err != nil {
		return 0, false
	}
	if manifest.Config.Digest == "" {
		return 0, true
	}
	url := fmt.Sprintf("https://%s/v2/%s/blobs/%s", registryHost(host), repo, manifest.Config.Digest)
	resp, err := r.do(ctx, http.MethodGet, url, repo)
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var config struct {
		Config struct {
			ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return 0, false
	}
	var tcp []int
	for spec := range config.Config.ExposedPorts {
		port, proto, _ := strings.Cut(spec, "/")
		if proto != "" && proto != "tcp" {
			continue
		}
		if p, err := atoiStrict(port); err == nil {
			tcp = append(tcp, p)
		}
	}
	switch {
	case len(tcp) == 1:
		return tcp[0], true
	case len(tcp) > 1:
		// Several ports, one of which is the web one by convention.
		for _, p := range tcp {
			if p == 80 {
				return 80, true
			}
		}
	}
	return 0, true
}

type ociManifest struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

func (r *registry) manifest(ctx context.Context, host, repo, ref string) (ociManifest, error) {
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registryHost(host), repo, ref)
	resp, err := r.do(ctx, http.MethodGet, url, repo)
	if err != nil {
		return ociManifest{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ociManifest{}, fmt.Errorf("registry answered %s", resp.Status)
	}
	var m ociManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return ociManifest{}, fmt.Errorf("decoding manifest: %w", err)
	}
	return m, nil
}

// splitReference decomposes an OCI reference into registry host, repository,
// tag, and digest, applying Docker's defaults: no host means Docker Hub, and a
// single-component repository there lives under `library/`.
func splitReference(ref string) (host, repo, tag, digest string) {
	rest := ref
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		digest, rest = rest[i+1:], rest[:i]
	}
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		tag, rest = rest[i+1:], rest[:i]
	}
	if tag == "" {
		tag = "latest"
	}
	host = "docker.io"
	first, remainder, hasSlash := strings.Cut(rest, "/")
	if hasSlash && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		host, rest = first, remainder
	}
	if host == "docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	return host, rest, tag, digest
}

// canonicalName rebuilds the reference prefix, keeping Docker Hub images in
// their familiar short form so the catalog reads the way operators write it.
func canonicalName(host, repo string) string {
	if isDockerHub(host) {
		return strings.TrimPrefix(repo, "library/")
	}
	return host + "/" + repo
}

func registryHost(host string) string {
	if host == "docker.io" || host == "index.docker.io" {
		return "registry-1.docker.io"
	}
	return host
}

// digest asks which bits a tag currently designates. The answer is the
// multi-architecture index digest, not one platform's manifest, so the pin
// still resolves on both amd64 and arm64.
//
// Docker Hub is asked through its own API rather than its registry: the
// registry meters anonymous clients at 100 manifest requests an hour, and two
// thirds of Coolify's images live there, so going through the registry would
// turn one import into a multi-hour crawl. Hub's tag endpoint returns the same
// digests and is not metered against that quota. Every other registry answers
// a plain HEAD.
func (r *registry) digest(ctx context.Context, host, repo, tag string) (string, error) {
	if isDockerHub(host) {
		// No registry fallback here, deliberately. When Hub's API cannot
		// answer, the registry almost certainly cannot either — the pull quota
		// is what pushed us onto the API in the first place — and retrying
		// there only parks a worker behind a ten-minute backoff that ends in
		// the same failure. Reporting the image as unpinnable immediately
		// keeps the run moving and puts it in the report where it belongs.
		return r.hubDigest(ctx, repo, tag)
	}
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registryHost(host), repo, tag)
	resp, err := r.do(ctx, http.MethodHead, url, repo)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry answered %s", resp.Status)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(d, "sha256:") {
		return "", fmt.Errorf("registry returned no content digest")
	}
	return d, nil
}

func isDockerHub(host string) bool { return host == "docker.io" || host == "index.docker.io" }

func (r *registry) hubDigest(ctx context.Context, repo, tag string) (string, error) {
	d, _, err := r.hubTag(ctx, repo, tag)
	return d, err
}

// hubTag reads one tag from Docker Hub's API, returning both the index digest
// to pin and the linux/amd64 child digest — which saves the metered registry
// request that reading the image's EXPOSE would otherwise start with.
func (r *registry) hubTag(ctx context.Context, repo, tag string) (index, amd64 string, err error) {
	url := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags/%s", repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("building Docker Hub request: %w", err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("querying Docker Hub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("docker Hub answered %s", resp.Status)
	}
	var body struct {
		Digest string `json:"digest"`
		Images []struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Digest       string `json:"digest"`
		} `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("decoding Docker Hub tag: %w", err)
	}
	if !strings.HasPrefix(body.Digest, "sha256:") {
		return "", "", fmt.Errorf("docker Hub returned no digest")
	}
	for _, i := range body.Images {
		if i.OS == "linux" && i.Architecture == "amd64" {
			amd64 = i.Digest
			break
		}
	}
	return body.Digest, amd64, nil
}

// do performs a registry request, acquiring an anonymous pull token when the
// registry asks for one and waiting out a rate limit when it imposes one.
// Every registry in Coolify's library implements the same bearer challenge, so
// one path covers Docker Hub, ghcr.io, quay.io and the rest.
//
// Backing off matters more than speed here: anonymous quotas are the reason an
// import produces unpinnable templates, and a template refused because a
// registry was busy is a worse outcome than a slower run.
func (r *registry) do(ctx context.Context, method, url, repo string) (*http.Response, error) {
	tokenKey := registryOf(url) + "|" + repo
	attempt := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("Accept", acceptManifests)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return r.http.Do(req) //nolint:bodyclose // closed by the caller
	}

	for backoff := 15 * time.Second; ; backoff *= 2 {
		r.mu.Lock()
		cached := r.tokens[tokenKey]
		r.mu.Unlock()

		resp, err := attempt(cached)
		if err != nil {
			return nil, fmt.Errorf("requesting %s: %w", url, err)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			challenge := resp.Header.Get("Www-Authenticate")
			_ = resp.Body.Close()
			token, err := r.fetchToken(ctx, challenge, repo)
			if err != nil {
				return nil, err
			}
			r.mu.Lock()
			r.tokens[tokenKey] = token
			r.mu.Unlock()
			if resp, err = attempt(token); err != nil {
				return nil, fmt.Errorf("requesting %s: %w", url, err)
			}
		}
		if resp.StatusCode != http.StatusTooManyRequests || backoff > maxBackoff {
			return resp, nil
		}
		_ = resp.Body.Close()
		wait := backoff
		if after := retryAfter(resp); after > 0 {
			wait = after
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// maxBackoff bounds the wait on a rate-limited registry. Kept short on
// purpose: nothing is cached for an image we fail to resolve, so a later run
// re-asks only the unanswered ones and picks up capacity as it frees. Waiting
// longer inside one run buys a template or two and costs every template queued
// behind it.
const maxBackoff = 90 * time.Second

func retryAfter(resp *http.Response) time.Duration {
	n, err := atoiStrict(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || n <= 0 || n > 600 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// registryOf extracts the scheme-and-host prefix of a registry URL, which is
// the scope a bearer token is cached under together with the repository.
func registryOf(url string) string {
	rest := strings.TrimPrefix(url, "https://")
	host, _, _ := strings.Cut(rest, "/")
	return host
}

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

func (r *registry) fetchToken(ctx context.Context, challenge, repo string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("registry requires unsupported auth %q", challenge)
	}
	params := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry auth challenge names no realm")
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	url := fmt.Sprintf("%s?scope=%s", realm, scope)
	if s := params["service"]; s != "" {
		url += "&service=" + s
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building token request: %w", err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching pull token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pull token request answered %s", resp.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decoding pull token: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	return body.AccessToken, nil
}

// concreteTagRe recognizes a tag that already names one release rather than a
// moving target: a full major.minor.patch, optionally prefixed and suffixed.
var concreteTagRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+`)

// tagVersion reports the version a tag states outright, or "" when the tag is
// a moving one (latest, a bare major, a channel name).
func tagVersion(tag string) string {
	if concreteTagRe.MatchString(tag) {
		return strings.TrimPrefix(tag, "v")
	}
	return ""
}

// versionTagFor makes a mutable tag readable: it asks Docker Hub which of the
// repository's other tags currently point at the same bits, and takes the most
// specific version among them. Best-effort by design — the pin is the digest,
// this only decides what the catalog card says — so it is skipped entirely for
// registries without this (non-standard) listing API.
func (r *registry) versionTagFor(ctx context.Context, host, repo, digest string) string {
	if host != "docker.io" && host != "index.docker.io" {
		return ""
	}
	url := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags?page_size=100&ordering=last_updated", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body struct {
		Results []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}
	best := ""
	for _, t := range body.Results {
		if t.Digest != digest {
			continue
		}
		v := tagVersion(t.Name)
		if v == "" {
			continue
		}
		// The longest match is the most specific: 1.2.3 over 1.2, and
		// 1.2.3-alpine over 1.2.3 only if that is all the repository offers.
		if len(v) > len(best) {
			best = v
		}
	}
	if len(best) > 40 {
		return ""
	}
	return best
}
