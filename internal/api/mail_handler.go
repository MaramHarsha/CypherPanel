package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/dns"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// MailHandler manages hosted-account email mailboxes. On mailbox creation it
// also publishes the deliverability DNS records (MX/SPF/DMARC) via the DNS
// provider — outbound mail from a fresh VPS is worthless without them.
type MailHandler struct {
	Accounts    *store.Accounts
	Mail        *store.MailAccounts
	Packages    *store.Packages
	Servers     *store.Servers
	Tasks       *store.Tasks
	Publisher   *jobs.Publisher
	Audit       *audit.Logger
	DNS         dns.Provider // nil when DNS is not configured
	Nameservers []string
}

var localPartRe = regexp.MustCompile(`^[a-z0-9._%+-]{1,64}$`)

type mailResponse struct {
	ID        string    `json:"id"`
	Address   string    `json:"address"`
	QuotaMB   int       `json:"quota_mb"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func toMailResponse(m store.MailAccount) mailResponse {
	return mailResponse{ID: m.ID, Address: m.Address, QuotaMB: m.QuotaMB, Status: m.Status, CreatedAt: m.CreatedAt}
}

func (h *MailHandler) scopedAccount(c *gin.Context) *store.Account {
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

// List returns an account's mailboxes.
//
//	@Summary  List an account's mailboxes
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Account ID"
//	@Success  200 {array} mailResponse
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/mail [get]
func (h *MailHandler) List(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	items, err := h.Mail.ListByAccount(c.Request.Context(), account.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]mailResponse, 0, len(items))
	for _, m := range items {
		out = append(out, toMailResponse(m))
	}
	c.JSON(http.StatusOK, out)
}

type createMailRequest struct {
	LocalPart string `json:"local_part" binding:"required"`
	Password  string `json:"password" binding:"required,min=8"`
	QuotaMB   int    `json:"quota_mb"`
}

// Create provisions a mailbox at localpart@<account domain>.
//
//	@Summary  Create a mailbox for an account
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string            true "Account ID"
//	@Param    request body createMailRequest true "Mailbox"
//	@Success  202 {object} mailResponse
//	@Failure  400 {object} map[string]string
//	@Failure  403 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/mail [post]
func (h *MailHandler) Create(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	var req createMailRequest
	if err := c.ShouldBindJSON(&req); err != nil || !localPartRe.MatchString(strings.ToLower(req.LocalPart)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "local_part (1-64 chars) and password (8+) are required"})
		return
	}
	domain := account.PrimaryDomain
	address := strings.ToLower(req.LocalPart) + "@" + domain

	// Enforce the package email_accounts limit (0 = unlimited).
	if pkg, err := h.Packages.GetByID(c.Request.Context(), account.PackageID); err == nil && pkg.Limits.EmailAccounts > 0 {
		if n, cerr := h.Mail.CountByAccount(c.Request.Context(), account.ID); cerr == nil && n >= pkg.Limits.EmailAccounts {
			c.JSON(http.StatusForbidden, gin.H{"error": "mailbox limit reached for this package"})
			return
		}
	}

	// Hash the password in Core (Dovecot BLF-CRYPT compatible); plaintext never
	// leaves this handler — the payload carries only the bcrypt hash.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	rec, err := h.Mail.Create(c.Request.Context(), account.ID, address, req.QuotaMB)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create mailbox (duplicate address?)"})
		return
	}

	maildir := domain + "/" + strings.ToLower(req.LocalPart) + "/"
	payload, _ := json.Marshal(jobs.MailCreatePayload{
		Address: address, Domain: domain, Maildir: maildir,
		PasswordHash: string(hash), QuotaMB: req.QuotaMB,
	})
	task, terr := h.Tasks.Create(c.Request.Context(), account.ServerID, jobs.TypeMailCreate, payload, claims.Subject, account.ID)
	if terr != nil {
		_ = h.Mail.SetStatus(c.Request.Context(), rec.ID, "failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if perr := h.Publisher.Publish(c.Request.Context(), jobs.Task{ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload}); perr != nil {
		slog.Error("dispatching mail create", "mail_id", rec.ID, "error", perr)
		_ = h.Mail.SetStatus(c.Request.Context(), rec.ID, "failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mailbox recorded but provisioning dispatch failed"})
		return
	}

	// Publish deliverability records (MX/SPF/DMARC) best-effort. DKIM is
	// published from the task result once the agent has generated the key.
	h.publishMailDNS(c.Request.Context(), account, domain)

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "mail.create", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"address": address}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, toMailResponse(*rec))
}

// publishMailDNS ensures the zone exists and publishes MX + SPF + DMARC.
func (h *MailHandler) publishMailDNS(ctx context.Context, account *store.Account, domain string) {
	if h.DNS == nil {
		return
	}
	mailHost := "mail." + domain
	_ = h.DNS.EnsureZone(ctx, domain, h.Nameservers, nil)

	serverIP := ""
	if srv, err := h.Servers.GetByID(ctx, account.ServerID); err == nil {
		serverIP = srv.IPAddress
	}
	records := []dns.Record{
		{Name: domain, Type: "MX", TTL: 3600, Contents: []string{"10 " + mailHost}},
		{Name: domain, Type: "TXT", TTL: 3600, Contents: []string{"v=spf1 mx ~all"}},
		{Name: "_dmarc." + domain, Type: "TXT", TTL: 3600, Contents: []string{"v=DMARC1; p=quarantine; rua=mailto:postmaster@" + domain}},
	}
	if serverIP != "" {
		records = append(records, dns.Record{Name: mailHost, Type: "A", TTL: 3600, Contents: []string{serverIP}})
	}
	for _, r := range records {
		if err := h.DNS.UpsertRecord(ctx, domain, r); err != nil {
			slog.Warn("publishing mail DNS record", "domain", domain, "type", r.Type, "error", err)
		}
	}
}

// Delete removes a mailbox.
//
//	@Summary  Delete a mailbox
//	@Tags     admin
//	@Produce  json
//	@Param    id     path string true "Account ID"
//	@Param    mailid path string true "Mailbox ID"
//	@Success  202 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/accounts/{id}/mail/{mailid} [delete]
func (h *MailHandler) Delete(c *gin.Context) {
	account := h.scopedAccount(c)
	if account == nil {
		return
	}
	claims := auth.ClaimsFrom(c)
	rec, err := h.Mail.GetByID(c.Request.Context(), c.Param("mailid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && rec.AccountID != account.ID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if err := h.Mail.SetStatus(c.Request.Context(), rec.ID, "deleting"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	local := strings.SplitN(rec.Address, "@", 2)[0]
	maildir := account.PrimaryDomain + "/" + local + "/"
	payload, _ := json.Marshal(jobs.MailDeletePayload{Address: rec.Address, Maildir: maildir})
	task, terr := h.Tasks.Create(c.Request.Context(), account.ServerID, jobs.TypeMailDelete, payload, claims.Subject, account.ID)
	if terr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if perr := h.Publisher.Publish(c.Request.Context(), jobs.Task{ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload}); perr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "dispatch failed"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "mail.delete", TargetType: "account", TargetID: account.ID,
		Detail: map[string]any{"address": rec.Address}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "deleting"})
}
