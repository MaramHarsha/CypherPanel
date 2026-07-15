package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/dns"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// DNSHandler is the per-account zone editor. Each account manages records only
// within its own primary-domain zone. Zones are created lazily on first view
// with sensible defaults (apex + www pointing at the account's server).
type DNSHandler struct {
	Accounts    *store.Accounts
	Servers     *store.Servers
	Provider    dns.Provider // nil when DNS is not configured
	Nameservers []string
	Audit       *audit.Logger
}

func (h *DNSHandler) enabled(c *gin.Context) bool {
	if h.Provider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "DNS management is not configured"})
		return false
	}
	return true
}

func (h *DNSHandler) scopedAccount(c *gin.Context) *store.Account {
	claims := auth.ClaimsFrom(c)
	account, err := h.Accounts.GetByID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, account.ResellerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return nil
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return nil
	}
	return account
}

// RecordTypes lists the record types the editor supports.
//
//	@Summary  List supported DNS record types
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} string
//	@Security BearerAuth
//	@Router   /admin/dns/record-types [get]
func (h *DNSHandler) RecordTypes(c *gin.Context) {
	c.JSON(http.StatusOK, dns.SupportedTypes)
}

// List returns the account zone's records, creating the zone on first access.
//
//	@Summary  List an account's DNS records
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  200 {array} dns.Record
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/dns [get]
func (h *DNSHandler) List(c *gin.Context) {
	if !h.enabled(c) {
		return
	}
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	if err := h.ensureZone(c, account); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "DNS backend error: " + err.Error()})
		return
	}
	records, err := h.Provider.ListRecords(c.Request.Context(), account.PrimaryDomain)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "DNS backend error"})
		return
	}
	c.JSON(http.StatusOK, records)
}

// ensureZone creates the account's zone with default records if it does not yet
// exist. The apex + www point at the account's server IP.
func (h *DNSHandler) ensureZone(c *gin.Context, account *store.Account) error {
	serverIP := ""
	if srv, err := h.Servers.GetByID(c.Request.Context(), account.ServerID); err == nil {
		serverIP = srv.IPAddress
	}
	var defaults []dns.Record
	if serverIP != "" {
		defaults = []dns.Record{
			{Name: account.PrimaryDomain, Type: "A", TTL: 3600, Contents: []string{serverIP}},
			{Name: "www." + account.PrimaryDomain, Type: "A", TTL: 3600, Contents: []string{serverIP}},
		}
	}
	return h.Provider.EnsureZone(c.Request.Context(), account.PrimaryDomain, h.Nameservers, defaults)
}

type dnsRecordRequest struct {
	Name     string   `json:"name" binding:"required"`
	Type     string   `json:"type" binding:"required"`
	TTL      int      `json:"ttl"`
	Contents []string `json:"contents" binding:"required"`
}

// Upsert creates or replaces a record in the account's zone.
//
//	@Summary  Create or update a DNS record
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string           true "Account ID"
//	@Param    request body dnsRecordRequest true "DNS record"
//	@Success  200 {object} map[string]string
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/dns [post]
func (h *DNSHandler) Upsert(c *gin.Context) {
	if !h.enabled(c) {
		return
	}
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	var req dnsRecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, type and contents are required"})
		return
	}
	// The record must belong to the account's zone (no editing other domains).
	if !dns.NameInZone(req.Name, account.PrimaryDomain) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "record name must be within " + account.PrimaryDomain})
		return
	}
	rec := dns.Record{Name: req.Name, Type: req.Type, TTL: req.TTL, Contents: req.Contents}
	if err := dns.ValidateRecord(rec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	existing, err := h.Provider.ListRecords(c.Request.Context(), account.PrimaryDomain)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "DNS backend error"})
		return
	}
	if err := dns.ValidateAgainstZone(rec, existing); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.Provider.UpsertRecord(c.Request.Context(), account.PrimaryDomain, rec); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "DNS backend error"})
		return
	}
	h.audit(c, account.ID, "dns.upsert", req.Name+" "+req.Type)
	c.JSON(http.StatusOK, gin.H{"status": "saved"})
}

// Delete removes a record from the account's zone.
//
//	@Summary  Delete a DNS record
//	@Tags     admin
//	@Produce  json
//	@Param    id   path  string true "Account ID"
//	@Param    name query string true "Record name"
//	@Param    type query string true "Record type"
//	@Success  200 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/dns [delete]
func (h *DNSHandler) Delete(c *gin.Context) {
	if !h.enabled(c) {
		return
	}
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	name, rtype := c.Query("name"), c.Query("type")
	if !dns.NameInZone(name, account.PrimaryDomain) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "record not in this account's zone"})
		return
	}
	if err := h.Provider.DeleteRecord(c.Request.Context(), account.PrimaryDomain, name, rtype); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "DNS backend error"})
		return
	}
	h.audit(c, account.ID, "dns.delete", name+" "+rtype)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *DNSHandler) audit(c *gin.Context, accountID, action, detail string) {
	claims := auth.ClaimsFrom(c)
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: action, TargetType: "account", TargetID: accountID,
		Detail: map[string]any{"record": detail}, IP: c.ClientIP(),
	})
}
