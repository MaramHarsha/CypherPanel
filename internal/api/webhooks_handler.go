package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/secretcrypt"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// WebhooksHandler manages outbound webhook endpoints and their delivery log.
// Root-admin only: an endpoint receives the whole fleet's event feed, which
// crosses reseller boundaries.
type WebhooksHandler struct {
	Webhooks *store.Webhooks
	Crypt    *secretcrypt.Cipher
	Audit    *audit.Logger
}

type webhookResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	// Secret is set ONLY in the create response — this is the one and only
	// time the plaintext signing key is shown.
	Secret string `json:"secret,omitempty"`
}

func toWebhookResponse(w store.Webhook) webhookResponse {
	evts := w.Events
	if evts == nil {
		evts = []string{}
	}
	return webhookResponse{
		ID: w.ID, Name: w.Name, URL: w.URL, Events: evts,
		Active: w.Active, CreatedAt: w.CreatedAt,
	}
}

type deliveryResponse struct {
	ID             string          `json:"id"`
	WebhookID      string          `json:"webhook_id"`
	WebhookName    string          `json:"webhook_name"`
	EventID string `json:"event_id"`
	Subject string `json:"subject"`
	// swaggertype: the spec generator cannot resolve json.RawMessage, and the
	// payload really is an arbitrary event object to any client.
	Payload        json.RawMessage `json:"payload" swaggertype:"object"`
	Status         string          `json:"status"`
	Attempts       int             `json:"attempts"`
	ResponseStatus int             `json:"response_status"`
	Error          string          `json:"error,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	DeliveredAt    *time.Time      `json:"delivered_at,omitempty"`
}

func toDeliveryResponse(d store.WebhookDelivery) deliveryResponse {
	return deliveryResponse{
		ID: d.ID, WebhookID: d.WebhookID, WebhookName: d.WebhookName,
		EventID: d.EventID, Subject: d.Subject, Payload: d.Payload,
		Status: d.Status, Attempts: d.Attempts, ResponseStatus: d.ResponseStatus,
		Error: d.Error, CreatedAt: d.CreatedAt, DeliveredAt: d.DeliveredAt,
	}
}

// List returns registered webhook endpoints (never their signing keys).
//
//	@Summary  List webhook endpoints
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} webhookResponse
//	@Security BearerAuth
//	@Router   /admin/webhooks [get]
func (h *WebhooksHandler) List(c *gin.Context) {
	items, err := h.Webhooks.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]webhookResponse, 0, len(items))
	for _, w := range items {
		out = append(out, toWebhookResponse(w))
	}
	c.JSON(http.StatusOK, out)
}

type createWebhookRequest struct {
	Name string `json:"name" binding:"required"`
	URL  string `json:"url" binding:"required"`
	// Events is an allowlist of exact subjects. Empty means every event.
	Events []string `json:"events"`
}

// Create registers an endpoint and returns its generated signing key once.
//
//	@Summary  Create a webhook endpoint
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    request body createWebhookRequest true "Endpoint"
//	@Success  201 {object} webhookResponse
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/webhooks [post]
func (h *WebhooksHandler) Create(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	var req createWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and url are required"})
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url must be an absolute http(s) URL"})
		return
	}
	// Reject unknown subjects at creation: a typo'd subject would otherwise
	// silently never fire, and the operator would have no way to tell.
	for _, s := range req.Events {
		if !events.KnownSubject(s) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown event subject: " + s})
			return
		}
	}
	if h.Crypt == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "secret encryption is not configured"})
		return
	}

	// Generate the signing key here rather than accepting one: an
	// operator-chosen HMAC key is usually a weak one.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	enc, err := h.Crypt.Encrypt([]byte(secret))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	hook, err := h.Webhooks.Create(c.Request.Context(), req.Name, req.URL, enc, req.Events)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not create webhook (duplicate name?)"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "webhook.create", TargetType: "webhook", TargetID: hook.ID,
		Detail: map[string]any{"name": hook.Name, "url": hook.URL, "events": req.Events},
		IP:     c.ClientIP(),
	})

	resp := toWebhookResponse(*hook)
	resp.Secret = secret // shown exactly once
	c.JSON(http.StatusCreated, resp)
}

type setWebhookActiveRequest struct {
	Active bool `json:"active"`
}

// SetActive enables or disables an endpoint.
//
//	@Summary  Enable or disable a webhook endpoint
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    hookid  path string                  true "Webhook ID"
//	@Param    request body setWebhookActiveRequest true "State"
//	@Success  200 {object} webhookResponse
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/webhooks/{hookid} [patch]
func (h *WebhooksHandler) SetActive(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	var req setWebhookActiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "active is required"})
		return
	}
	id := c.Param("hookid")
	err := h.Webhooks.SetActive(c.Request.Context(), id, req.Active)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "webhook not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	hook, err := h.Webhooks.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "webhook.set_active", TargetType: "webhook", TargetID: id,
		Detail: map[string]any{"active": req.Active}, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, toWebhookResponse(*hook))
}

// Delete removes an endpoint and its delivery history.
//
//	@Summary  Delete a webhook endpoint
//	@Tags     admin
//	@Produce  json
//	@Param    hookid path string true "Webhook ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/webhooks/{hookid} [delete]
func (h *WebhooksHandler) Delete(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	id := c.Param("hookid")
	err := h.Webhooks.Delete(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "webhook not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "webhook.delete", TargetType: "webhook", TargetID: id, IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// ListDeliveries returns the delivery log, optionally filtered to one endpoint.
//
//	@Summary  List webhook deliveries
//	@Tags     admin
//	@Produce  json
//	@Param    webhook_id query string false "Filter by endpoint"
//	@Param    limit      query int    false "Max rows (default 50)"
//	@Success  200 {array} deliveryResponse
//	@Security BearerAuth
//	@Router   /admin/webhooks/deliveries [get]
func (h *WebhooksHandler) ListDeliveries(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := h.Webhooks.ListDeliveries(c.Request.Context(), c.Query("webhook_id"), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]deliveryResponse, 0, len(items))
	for _, d := range items {
		out = append(out, toDeliveryResponse(d))
	}
	c.JSON(http.StatusOK, out)
}

// Redeliver re-queues a failed or dead delivery.
//
//	@Summary  Redeliver a webhook delivery
//	@Tags     admin
//	@Produce  json
//	@Param    deliveryid path string true "Delivery ID"
//	@Success  202 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/webhooks/deliveries/{deliveryid}/redeliver [post]
func (h *WebhooksHandler) Redeliver(c *gin.Context) {
	claims := auth.ClaimsFrom(c)
	id := c.Param("deliveryid")
	err := h.Webhooks.Redeliver(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "delivery not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "webhook.redeliver", TargetType: "webhook_delivery", TargetID: id, IP: c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "queued"})
}

// EventSubjects lists every event subject an endpoint can subscribe to, so the
// UI offers a real picker instead of a free-text field nobody can get right.
//
//	@Summary  List subscribable event subjects
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} string
//	@Security BearerAuth
//	@Router   /admin/webhooks/event-subjects [get]
func (h *WebhooksHandler) EventSubjects(c *gin.Context) {
	c.JSON(http.StatusOK, events.AllSubjects())
}
