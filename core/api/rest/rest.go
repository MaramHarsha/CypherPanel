// Package rest is the human/UI-facing HTTP API and the interim status console.
// It is API-first (vision.md non-negotiable 3): every action here is a plain
// REST call the console makes with a bearer token. All responses use glossary
// vocabulary and mask secrets by default (ENGINEERING rules 5, 20).
package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/servers"
)

// Pinger is the readiness dependency (the store).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the dependencies the API needs.
type Deps struct {
	Auth         *auth.Authenticator
	Servers      *servers.Service
	Projects     *projects.Service
	Applications *applications.Service
	Pinger       Pinger
	CACertPEM    []byte
	EnrollAddr   string // advertised gRPC enrollment address (host:port)
	NATSURL      string // advertised data-plane URL
	ConsoleURL   string // advertised HTTP base URL (installer + CA fetch)
	Log          *slog.Logger
}

// API holds the HTTP handlers and their dependencies.
type API struct {
	deps Deps
}

// New builds the API.
func New(d Deps) *API { return &API{deps: d} }

// Handler returns the fully-routed HTTP handler with global middleware applied.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health (unauthenticated).
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /readyz", a.handleReadyz)

	// Auth.
	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", a.authed(a.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/me", a.authed(a.handleMe))

	// Public CA certificate (needed by agents to pin the plane; not secret).
	mux.HandleFunc("GET /api/v1/ca.pem", a.handleCAPem)

	// The API's own contract (ENGINEERING rule 19: the spec is the source of
	// truth, so it ships with the binary that implements it).
	mux.HandleFunc("GET /api/v1/openapi.yaml", a.handleOpenAPI)

	// The agent join installer (public, no secrets — the token and CA
	// fingerprint arrive via the operator's install command). The canonical
	// file is /install/agent.sh; make generate syncs the embedded copy.
	mux.HandleFunc("GET /install/agent.sh", a.handleInstallScript)

	// Servers.
	mux.HandleFunc("GET /api/v1/servers", a.authed(a.handleListServers))
	mux.HandleFunc("POST /api/v1/servers", a.authed(a.handleCreateServer))
	mux.HandleFunc("GET /api/v1/servers/{id}", a.authed(a.handleGetServer))
	mux.HandleFunc("DELETE /api/v1/servers/{id}", a.authed(a.handleDeleteServer))

	// Projects & environments.
	mux.HandleFunc("GET /api/v1/projects", a.authed(a.handleListProjects))
	mux.HandleFunc("POST /api/v1/projects", a.authed(a.handleCreateProject))
	mux.HandleFunc("GET /api/v1/projects/{id}", a.authed(a.handleGetProject))
	mux.HandleFunc("DELETE /api/v1/projects/{id}", a.authed(a.handleDeleteProject))
	mux.HandleFunc("GET /api/v1/projects/{id}/environments", a.authed(a.handleListEnvironments))
	mux.HandleFunc("POST /api/v1/projects/{id}/environments", a.authed(a.handleCreateEnvironment))

	// Applications (created + listed under an environment; addressed by app id).
	mux.HandleFunc("POST /api/v1/environments/{id}/applications", a.authed(a.handleCreateApplication))
	mux.HandleFunc("GET /api/v1/environments/{id}/applications", a.authed(a.handleListApplications))
	mux.HandleFunc("GET /api/v1/applications/{id}", a.authed(a.handleGetApplication))
	mux.HandleFunc("DELETE /api/v1/applications/{id}", a.authed(a.handleDeleteApplication))
	mux.HandleFunc("GET /api/v1/applications/{id}/env", a.authed(a.handleListEnvVars))
	mux.HandleFunc("PUT /api/v1/applications/{id}/env/{key}", a.authed(a.handleSetEnvVar))
	mux.HandleFunc("DELETE /api/v1/applications/{id}/env/{key}", a.authed(a.handleDeleteEnvVar))

	// Interim console + static assets.
	mux.Handle("GET /", a.consoleHandler())

	return a.recoverer(a.securityHeaders(a.logRequests(mux)))
}

// ─── middleware ─────────────────────────────────────────────────────────────

type ctxKey int

const userKey ctxKey = iota

// authed wraps a handler so it runs only for an authenticated user, which it
// places in the request context.
func (a *API) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		user, err := a.deps.Auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, user)
		next(w, r.WithContext(ctx))
	}
}

func (a *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		a.deps.Log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.deps.Log.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// ─── helpers ────────────────────────────────────────────────────────────────

func userFromContext(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(userKey).(domain.User)
	return u, ok
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return "", false
	}
	return h[len(prefix):], true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
