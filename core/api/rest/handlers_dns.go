package rest

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/dns"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// DNSService is the panel's DNS Provider (consumer-defined, rule 6).
type DNSService interface {
	Get(ctx context.Context) (dns.Settings, error)
	Set(ctx context.Context, c dns.Config) (dns.Settings, error)
	Delete(ctx context.Context) error
	Test(ctx context.Context) error
	RefreshZones(ctx context.Context) ([]domain.DNSZone, error)
	Verify(ctx context.Context, host string) (dns.Verification, error)
	SyncApplication(ctx context.Context, app domain.Application, serverPublicAddress string) error
	ForgetApplication(ctx context.Context, appID string) error
}

type dnsSettingsDTO struct {
	Configured bool       `json:"configured"`
	Kind       string     `json:"kind"`
	ConfigHint string     `json:"config_hint"`
	ZoneCount  int        `json:"zone_count"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// The token goes in and is never read back — the notifier contract, for the
// same reason a settings screen that can display a credential can leak one.
// This one is A3b (threat-model §5.12): it can repoint any zone it covers.
type setDNSRequest struct {
	APIToken string `json:"api_token"`
}

type dnsZoneDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	RefreshedAt time.Time `json:"refreshed_at"`
}

// applicationDNSDTO is the one project-scoped view: a member sees whether their
// own application's domain is verified and what its record is doing, without
// the token, the full zone list, or another project's records (spec §5).
type applicationDNSDTO struct {
	// Enforced is false when no provider is configured — the signal the UI uses
	// to say nothing at all rather than "unverified".
	Enforced bool   `json:"enforced"`
	Verified bool   `json:"verified"`
	Zone     string `json:"zone,omitempty"`
	// AvailableZones lets a failure say "here is what you do have" instead of
	// just "no" (ui-principles §11).
	AvailableZones []string `json:"available_zones"`
	Domain         string   `json:"domain"`
	// Reason names why no record exists when the domain is verified.
	Reason string `json:"reason,omitempty"`

	RecordName    string     `json:"record_name,omitempty"`
	RecordContent string     `json:"record_content,omitempty"`
	RecordCreated bool       `json:"record_created"`
	LastError     string     `json:"last_error,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
}

func dnsSettingsToDTO(s dns.Settings) dnsSettingsDTO {
	dto := dnsSettingsDTO{Configured: s.Configured, Kind: s.Kind, ConfigHint: s.Hint, ZoneCount: s.ZoneCount}
	if !s.UpdatedAt.IsZero() {
		u := s.UpdatedAt
		dto.UpdatedAt = &u
	}
	return dto
}

func zonesToDTO(zones []domain.DNSZone) []dnsZoneDTO {
	out := make([]dnsZoneDTO, 0, len(zones))
	for _, z := range zones {
		out = append(out, dnsZoneDTO{ID: z.ID, Name: z.Name, RefreshedAt: z.RefreshedAt})
	}
	return out
}

// ─── Panel routes (panel admin) ─────────────────────────────────────────────

func (a *API) handleGetPanelDNS(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if a.deps.DNS == nil {
		writeJSON(w, http.StatusOK, dnsSettingsDTO{})
		return
	}
	s, err := a.deps.DNS.Get(r.Context())
	if err != nil {
		a.deps.Log.Error("reading dns provider", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the DNS settings")
		return
	}
	writeJSON(w, http.StatusOK, dnsSettingsToDTO(s))
}

func (a *API) handleSetPanelDNS(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if a.deps.DNS == nil {
		writeError(w, http.StatusNotFound, "DNS automation is not available")
		return
	}
	var req setDNSRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	s, err := a.deps.DNS.Set(r.Context(), dns.Config{APIToken: req.APIToken})
	if err != nil {
		a.writeDNSError(w, "saving dns provider", err)
		return
	}
	writeJSON(w, http.StatusOK, dnsSettingsToDTO(s))
}

func (a *API) handleDeletePanelDNS(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if a.deps.DNS == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.deps.DNS.Delete(r.Context()); err != nil {
		a.writeDNSError(w, "deleting dns provider", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleTestPanelDNS(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if a.deps.DNS == nil {
		writeError(w, http.StatusNotFound, "DNS automation is not available")
		return
	}
	if err := a.deps.DNS.Test(r.Context()); err != nil {
		a.writeDNSError(w, "testing dns provider", err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) handleListDNSZones(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if a.deps.DNS == nil {
		writeJSON(w, http.StatusOK, []dnsZoneDTO{})
		return
	}
	s, err := a.deps.DNS.Get(r.Context())
	if err != nil || !s.Configured {
		writeJSON(w, http.StatusOK, []dnsZoneDTO{})
		return
	}
	// Reading the cache is the fast path; refreshing is an explicit action.
	zones, err := a.deps.DNSZones.ListDNSZones(r.Context())
	if err != nil {
		a.deps.Log.Error("listing dns zones", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list the zones")
		return
	}
	writeJSON(w, http.StatusOK, zonesToDTO(zones))
}

func (a *API) handleRefreshDNSZones(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok || !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	if a.deps.DNS == nil {
		writeError(w, http.StatusNotFound, "DNS automation is not available")
		return
	}
	zones, err := a.deps.DNS.RefreshZones(r.Context())
	if err != nil {
		a.writeDNSError(w, "refreshing dns zones", err)
		return
	}
	writeJSON(w, http.StatusOK, zonesToDTO(zones))
}

// ─── Application route (project member) ─────────────────────────────────────

func (a *API) handleGetApplicationDNS(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.DNS == nil {
		writeJSON(w, http.StatusOK, applicationDNSDTO{AvailableZones: []string{}})
		return
	}
	app, err := a.deps.Applications.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		a.deps.Log.Error("getting application for dns", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the DNS state")
		return
	}

	v, err := a.deps.DNS.Verify(r.Context(), app.Route.Domain)
	if err != nil {
		a.deps.Log.Error("verifying domain", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the DNS state")
		return
	}
	out := applicationDNSDTO{
		Enforced: v.Enforced, Verified: v.Verified, Zone: v.Zone,
		AvailableZones: v.AvailableZones, Domain: app.Route.Domain,
	}
	if out.AvailableZones == nil {
		out.AvailableZones = []string{}
	}

	rec, err := a.deps.DNSZones.GetDNSRecordByApplication(r.Context(), app.ID)
	switch {
	case err == nil:
		out.RecordName, out.RecordContent = rec.Name, rec.Content
		out.RecordCreated = rec.ProviderRecordID != nil
		out.LastError, out.NextAttemptAt = rec.LastError, rec.NextAttemptAt
	case errors.Is(err, store.ErrNotFound):
		// No record. If the domain verified, say WHY there is none — a silent
		// absence is the dead end ui-principles §11 forbids.
		if v.Enforced && v.Verified && app.Route.Domain != "" {
			out.Reason = a.dnsMissingReason(r.Context(), app)
		}
	default:
		a.deps.Log.Error("reading dns record", "error", err)
	}
	writeJSON(w, http.StatusOK, out)
}

// dnsMissingReason names the one condition that stops a verified domain getting
// a record: the server has nowhere to point at (spec §3.4).
func (a *API) dnsMissingReason(ctx context.Context, app domain.Application) string {
	srv, err := a.deps.Servers.Get(ctx, app.Runtime.ServerID)
	if err == nil && srv.PublicAddress == "" {
		return "This application's server has no public address, so there is nothing to point a record at. Set one on the server."
	}
	return ""
}

// writeDNSError maps the service's error kinds. A ValidationError is the
// operator's to fix and carries the provider's own words; everything else is
// ours and is logged rather than shown.
func (a *API) writeDNSError(w http.ResponseWriter, op string, err error) {
	var ve *dns.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Msg)
		return
	}
	if errors.Is(err, dns.ErrNotConfigured) {
		writeError(w, http.StatusBadRequest, "connect a DNS provider first")
		return
	}
	a.deps.Log.Error(op, "error", err)
	writeError(w, http.StatusInternalServerError, "the DNS provider could not be reached")
}

// DNSReader is the read-only slice of the store the DNS handlers need: the
// cached zone list and one application's record. Narrow on purpose — the
// handlers have no business writing records, which is the service's job.
type DNSReader interface {
	ListDNSZones(ctx context.Context) ([]domain.DNSZone, error)
	GetDNSRecordByApplication(ctx context.Context, appID string) (domain.DNSRecord, error)
}

// ServerAddressWriter is the one write the DNS feature needs on a server.
type ServerAddressWriter interface {
	SetServerPublicAddress(ctx context.Context, id, address string) (domain.Server, error)
}
