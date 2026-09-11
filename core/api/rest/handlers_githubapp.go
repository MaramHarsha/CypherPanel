package rest

// The GitHub App (github-app.md §7).
//
// Writing is OWNER and SESSION-ONLY: the private key can mint a token for every
// repository the App is installed on, and API tokens live in CI. It is the rule
// break glass and the agent channel already carry, applied to the credential
// with the widest reach in the panel.
//
// Reading is admin — whether an App is connected and where it is installed is
// operational fact, not a secret — and listing repositories is member, because
// that is what creating an application needs.
//
// No route returns the private key, not even redacted. A field that is
// sometimes a secret is a field that eventually leaks one.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/githubapp"
)

type githubInstallationDTO struct {
	InstallationID int64  `json:"installation_id"`
	AccountLogin   string `json:"account_login"`
	AccountType    string `json:"account_type"`
	RepoSelection  string `json:"repo_selection"`
}

type githubAppDTO struct {
	Configured bool   `json:"configured"`
	AppID      int64  `json:"app_id,omitempty"`
	Slug       string `json:"slug,omitempty"`
	// InstallURL is where the operator sends themselves to install it. Built by
	// the panel because it knows the slug, rather than telling someone to "go
	// to GitHub and find your App".
	InstallURL    string                  `json:"install_url,omitempty"`
	Installations []githubInstallationDTO `json:"installations"`
	UpdatedAt     *string                 `json:"updated_at,omitempty"`
}

type setGitHubAppRequest struct {
	AppID int64  `json:"app_id"`
	Slug  string `json:"slug"`
	// PrivateKeyPem is write-only: it goes in, it is sealed, and no route ever
	// hands it back (ui-principles §6).
	PrivateKeyPem string `json:"private_key_pem"`
	WebhookSecret string `json:"webhook_secret"`
}

func toGitHubAppDTO(s githubapp.Settings) githubAppDTO {
	out := githubAppDTO{
		Configured: s.Configured, AppID: s.AppID, Slug: s.Slug,
		InstallURL:    s.InstallURL,
		Installations: make([]githubInstallationDTO, 0, len(s.Installations)),
	}
	if !s.UpdatedAt.IsZero() {
		out.UpdatedAt = formatTime(&s.UpdatedAt)
	}
	for _, i := range s.Installations {
		out.Installations = append(out.Installations, githubInstallationDTO{
			InstallationID: i.InstallationID, AccountLogin: i.AccountLogin,
			AccountType: i.AccountType, RepoSelection: i.RepoSelection,
		})
	}
	return out
}

func (a *API) handleGetGitHubApp(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if a.deps.GitHubApp == nil {
		writeJSON(w, http.StatusOK, githubAppDTO{Installations: []githubInstallationDTO{}})
		return
	}
	s, err := a.deps.GitHubApp.Get(r.Context())
	if err != nil {
		a.deps.Log.Error("reading the github app", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the GitHub App")
		return
	}
	writeJSON(w, http.StatusOK, toGitHubAppDTO(s))
}

func (a *API) handleSetGitHubApp(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if a.deps.GitHubApp == nil {
		writeError(w, http.StatusServiceUnavailable, "the GitHub App is not enabled on this panel")
		return
	}
	var req setGitHubAppRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	entry := audit.Entry{
		Action:   audit.ActionGitHubAppConnected,
		Resource: audit.Resource(audit.ResourcePanel, "github-app", req.Slug),
		// The app id and the slug are public facts about the App. The key and
		// the webhook secret are not, and are not here (threat-model §5.15).
		Detail: map[string]any{"app_id": req.AppID, "slug": req.Slug},
	}
	s, err := a.deps.GitHubApp.Set(r.Context(), githubapp.Config{
		AppID: req.AppID, Slug: strings.TrimSpace(req.Slug),
		PrivateKeyPEM: req.PrivateKeyPem, WebhookSecret: req.WebhookSecret,
	})
	var invalid *githubapp.ValidationError
	switch {
	case err == nil:
	case errors.As(err, &invalid), errors.Is(err, githubapp.ErrBadKey):
		a.auditFailed(r, entry, err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	default:
		a.deps.Log.Error("connecting the github app", "error", err)
		writeError(w, http.StatusInternalServerError, "could not connect the GitHub App")
		return
	}
	a.audit(r, entry)
	writeJSON(w, http.StatusOK, toGitHubAppDTO(s))
}

func (a *API) handleDeleteGitHubApp(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if a.deps.GitHubApp == nil {
		writeError(w, http.StatusServiceUnavailable, "the GitHub App is not enabled on this panel")
		return
	}
	if err := a.deps.GitHubApp.Delete(r.Context()); err != nil {
		a.deps.Log.Error("disconnecting the github app", "error", err)
		writeError(w, http.StatusInternalServerError, "could not disconnect the GitHub App")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionGitHubAppDisconnected,
		Resource: audit.Resource(audit.ResourcePanel, "github-app", ""),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRefreshGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if a.deps.GitHubApp == nil {
		writeError(w, http.StatusServiceUnavailable, "the GitHub App is not enabled on this panel")
		return
	}
	if _, err := a.deps.GitHubApp.RefreshInstallations(r.Context()); err != nil {
		if errors.Is(err, githubapp.ErrNotConfigured) {
			writeError(w, http.StatusBadRequest, "no GitHub App is connected")
			return
		}
		a.deps.Log.Error("refreshing github installations", "error", err)
		writeError(w, http.StatusBadGateway, "GitHub could not be reached")
		return
	}
	s, err := a.deps.GitHubApp.Get(r.Context())
	if err != nil {
		a.deps.Log.Error("reading the github app", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the GitHub App")
		return
	}
	writeJSON(w, http.StatusOK, toGitHubAppDTO(s))
}

type githubRepoDTO struct {
	FullName       string `json:"full_name"`
	Private        bool   `json:"private"`
	DefaultBranch  string `json:"default_branch"`
	CloneURL       string `json:"clone_url"`
	InstallationID int64  `json:"installation_id"`
}

func (a *API) handleListGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleMember) {
		return
	}
	if a.deps.GitHubApp == nil {
		writeJSON(w, http.StatusOK, []githubRepoDTO{})
		return
	}
	repos, err := a.deps.GitHubApp.Repositories(r.Context())
	if errors.Is(err, githubapp.ErrNotConfigured) {
		// Not an error: it is the state of every panel that has not connected
		// one, and the create dialog reads an empty list as "type a URL".
		writeJSON(w, http.StatusOK, []githubRepoDTO{})
		return
	}
	if err != nil {
		a.deps.Log.Error("listing github repositories", "error", err)
		writeError(w, http.StatusBadGateway, "GitHub could not be reached")
		return
	}
	out := make([]githubRepoDTO, 0, len(repos))
	for _, repo := range repos {
		out = append(out, githubRepoDTO{
			FullName: repo.FullName, Private: repo.Private,
			DefaultBranch: repo.DefaultBranch, CloneURL: repo.CloneURL,
			InstallationID: repo.Installation,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGitHubAppWebhook receives the App's deliveries.
//
// An unverified signature is a 401 and nothing else: no lookup, no log of the
// body, no hint about which applications exist. That is the posture the
// per-application HMAC path already set, and the reason is the same — this
// endpoint is unauthenticated by design and must give an unauthenticated caller
// nothing to learn from.
func (a *API) handleGitHubAppWebhook(w http.ResponseWriter, r *http.Request) {
	if a.deps.GitHubApp == nil || a.deps.GitHubPush == nil {
		writeError(w, http.StatusServiceUnavailable, "the GitHub App is not enabled on this panel")
		return
	}
	secret, err := a.deps.GitHubApp.WebhookSecret(r.Context())
	if err != nil || secret == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	// Over the RAW body, before any parsing: a signature checked after decoding
	// is a signature over something the sender did not sign.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Hub-Signature-256"))) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Header.Get("X-GitHub-Event") != "push" {
		// Every other event is acknowledged and dropped. A 2xx is what stops
		// GitHub disabling the delivery for an event we simply do not act on.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	n, err := a.deps.GitHubPush.DeployFromPush(r.Context(), body)
	if err != nil {
		a.deps.Log.Error("handling a github app push", "error", err)
		writeError(w, http.StatusInternalServerError, "could not handle the push")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"deployments": n})
}

// maxWebhookBytes bounds a delivery. GitHub's own ceiling is 25 MB; a push
// payload is orders smaller, and reading an unbounded body from an
// unauthenticated endpoint is how one becomes a memory exhaustion.
const maxWebhookBytes = 2 << 20
