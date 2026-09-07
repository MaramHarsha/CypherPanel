package rest

// Email for verified domains, via a provider (managed-email.md §§4, 5).
//
// PANEL ADMIN throughout: connecting a provider spends a credential the whole
// install shares, and enabling mail on a domain writes records that decide where
// that domain's mail is delivered.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/mailhost"
)

// MailHostService is the provider-backed mail surface (consumer-defined).
type MailHostService interface {
	Connected(ctx context.Context) (bool, string)
	Connect(ctx context.Context, cfg mailhost.MigaduConfig) error
	Disconnect(ctx context.Context) error
	EnableDomain(ctx context.Context, domainName string) (domain.MailDomain, []mailhost.Record, error)
	Records(ctx context.Context, id string) (domain.MailDomain, []mailhost.Record, error)
	Rewrite(ctx context.Context, id string) (domain.MailDomain, error)
	Domains(ctx context.Context) ([]domain.MailDomain, error)
	DisableDomain(ctx context.Context, id string) error
	Mailboxes(ctx context.Context, domainID string) ([]mailhost.MailboxView, error)
	CreateMailbox(ctx context.Context, domainID, local, name, userID string) (mailhost.MailboxView, string, error)
	DeleteMailbox(ctx context.Context, domainID, address string) error
	ResetPassword(ctx context.Context, domainID, address string) (string, error)
}

type mailHostStatusDTO struct {
	Connected  bool   `json:"connected"`
	ConfigHint string `json:"config_hint"`
}

type mailDomainDTO struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
	// RecordsWrittenAt null means asked but NOT confirmed — pending, not done.
	RecordsWrittenAt *string `json:"records_written_at"`
	LastError        string  `json:"last_error"`
}

type mailRecordDTO struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority,omitempty"`
	// Purpose is what the record is FOR, in words: four TXT records are
	// indistinguishable without it.
	Purpose string `json:"purpose"`
}

func toMailDomainDTO(d domain.MailDomain) mailDomainDTO {
	var written *string
	if d.RecordsWrittenAt != nil {
		s := d.RecordsWrittenAt.UTC().Format(time.RFC3339)
		written = &s
	}
	return mailDomainDTO{ID: d.ID, Domain: d.Domain, RecordsWrittenAt: written, LastError: d.LastError}
}

func toMailRecordDTOs(records []mailhost.Record, domainName string) []mailRecordDTO {
	out := make([]mailRecordDTO, 0, len(records))
	for _, r := range records {
		out = append(out, mailRecordDTO{
			Type: r.Type, Name: mailhost.RecordName(r, domainName), Content: r.Content,
			TTL: r.TTL, Priority: r.Priority, Purpose: r.Purpose,
		})
	}
	return out
}

func (a *API) mailHostReady(w http.ResponseWriter) bool {
	if a.deps.MailHost == nil {
		writeError(w, http.StatusNotImplemented, "provider mail is not enabled on this panel")
		return false
	}
	return true
}

func (a *API) mailHostAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !a.mailHostReady(w) {
		return false
	}
	user, _ := userFromContext(r.Context())
	return a.requirePanelRole(w, user, domain.RoleAdmin)
}

// writeMailError maps the two kinds of failure that are INPUT rather than
// faults: a credential the operator must fix, and a request that was never
// valid. Both are 400 — a 500 would tell them to check the panel's logs for a
// problem in their own typing.
func (a *API) writeMailError(w http.ResponseWriter, err error) {
	var invalid *mailhost.ValidationError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, invalid.Msg)
		return
	}
	var authErr *mailhost.AuthError
	if errors.As(err, &authErr) {
		writeError(w, http.StatusBadRequest, authErr.Msg)
		return
	}
	a.deps.Log.Error("mail provider", "error", err)
	writeError(w, http.StatusInternalServerError, "the mail provider could not be reached")
}

func (a *API) handleGetMailHost(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	connected, hint := a.deps.MailHost.Connected(r.Context())
	writeJSON(w, http.StatusOK, mailHostStatusDTO{Connected: connected, ConfigHint: hint})
}

func (a *API) handleConnectMailHost(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	var cfg mailhost.MigaduConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := a.deps.MailHost.Connect(r.Context(), cfg); err != nil {
		a.writeMailError(w, err)
		return
	}
	// The account, never the key.
	a.audit(r, audit.Entry{
		Action:   audit.ActionMailProviderConnected,
		Resource: audit.Resource(audit.ResourcePanel, "panel", "mail provider"),
		Detail:   map[string]any{"account": cfg.Account},
	})
	connected, hint := a.deps.MailHost.Connected(r.Context())
	writeJSON(w, http.StatusOK, mailHostStatusDTO{Connected: connected, ConfigHint: hint})
}

func (a *API) handleDisconnectMailHost(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	if err := a.deps.MailHost.Disconnect(r.Context()); err != nil {
		a.writeMailError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionMailProviderDisconnected,
		Resource: audit.Resource(audit.ResourcePanel, "panel", "mail provider"),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListMailDomains(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	domains, err := a.deps.MailHost.Domains(r.Context())
	if err != nil {
		a.writeMailError(w, err)
		return
	}
	out := make([]mailDomainDTO, 0, len(domains))
	for _, d := range domains {
		out = append(out, toMailDomainDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleEnableMailDomain(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	md, records, err := a.deps.MailHost.EnableDomain(r.Context(), req.Domain)
	if err != nil {
		a.writeMailError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionMailDomainEnabled,
		Resource: audit.Resource(audit.ResourcePanel, md.ID, md.Domain),
		Detail:   map[string]any{"records": len(records)},
	})
	writeJSON(w, http.StatusCreated, struct {
		Domain  mailDomainDTO   `json:"domain"`
		Records []mailRecordDTO `json:"records"`
	}{toMailDomainDTO(md), toMailRecordDTOs(records, md.Domain)})
}

func (a *API) handleMailDomainRecords(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	md, records, err := a.deps.MailHost.Records(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Domain  mailDomainDTO   `json:"domain"`
		Records []mailRecordDTO `json:"records"`
	}{toMailDomainDTO(md), toMailRecordDTOs(records, md.Domain)})
}

func (a *API) handleRewriteMailRecords(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	md, err := a.deps.MailHost.Rewrite(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMailDomainDTO(md))
}

func (a *API) handleDisableMailDomain(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	if err := a.deps.MailHost.DisableDomain(r.Context(), r.PathValue("id")); err != nil {
		a.writeMailError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionMailDomainDisabled,
		Resource: audit.Resource(audit.ResourcePanel, r.PathValue("id"), "mail domain"),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListMailboxes(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	boxes, err := a.deps.MailHost.Mailboxes(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, boxes)
}

func (a *API) handleCreateMailbox(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	var req struct {
		LocalPart string `json:"local_part"`
		Name      string `json:"name"`
		UserID    string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	box, password, err := a.deps.MailHost.CreateMailbox(r.Context(), r.PathValue("id"), req.LocalPart, req.Name, req.UserID)
	if err != nil {
		a.writeMailError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionMailboxCreated,
		Resource: audit.Resource(audit.ResourcePanel, r.PathValue("id"), box.Address),
	})
	// The password appears HERE and never again. The panel cannot read it back
	// because it does not have it — the provider stores the hash and the panel
	// forwarded it once.
	writeJSON(w, http.StatusCreated, struct {
		Mailbox  mailhost.MailboxView `json:"mailbox"`
		Password string               `json:"password"`
	}{box, password})
}

func (a *API) handleDeleteMailbox(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	address := r.URL.Query().Get("address")
	if address == "" {
		writeError(w, http.StatusBadRequest, "name the address to delete")
		return
	}
	if err := a.deps.MailHost.DeleteMailbox(r.Context(), r.PathValue("id"), address); err != nil {
		a.writeMailError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionMailboxDeleted,
		Resource: audit.Resource(audit.ResourcePanel, r.PathValue("id"), address),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleResetMailboxPassword(w http.ResponseWriter, r *http.Request) {
	if !a.mailHostAdmin(w, r) {
		return
	}
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	password, err := a.deps.MailHost.ResetPassword(r.Context(), r.PathValue("id"), req.Address)
	if err != nil {
		a.writeMailError(w, err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionMailboxPasswordReset,
		Resource: audit.Resource(audit.ResourcePanel, r.PathValue("id"), req.Address),
	})
	writeJSON(w, http.StatusOK, struct {
		Password string `json:"password"`
	}{password})
}
