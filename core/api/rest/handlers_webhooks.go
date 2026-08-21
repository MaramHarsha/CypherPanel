package rest

// Outbound webhook endpoints and their delivery log (outbound-webhooks.md §7).
// Notifiers talk to people; these talk to machines. The signing secret is
// structurally absent from webhookEndpointDTO so it cannot leak by accident —
// it is returned exactly once, at create and at rotate (ENGINEERING rule 20).

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/webhooks"
)

type webhookEndpointDTO struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"project_id"`
	URL            string     `json:"url"`
	Events         []string   `json:"events"`
	Enabled        bool       `json:"enabled"`
	Health         string     `json:"health"`
	LastDeliveryAt *time.Time `json:"last_delivery_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toWebhookEndpointDTO(v webhooks.EndpointView) webhookEndpointDTO {
	return webhookEndpointDTO{
		ID:             v.Endpoint.ID,
		ProjectID:      v.Endpoint.ProjectID,
		URL:            v.Endpoint.URL,
		Events:         v.Endpoint.Events,
		Enabled:        v.Endpoint.Enabled,
		Health:         v.Health,
		LastDeliveryAt: v.LastDeliveryAt,
		CreatedAt:      v.Endpoint.CreatedAt,
		UpdatedAt:      v.Endpoint.UpdatedAt,
	}
}

type webhookDeliveryDTO struct {
	ID             string     `json:"id"`
	EndpointID     string     `json:"endpoint_id"`
	EventType      string     `json:"event_type"`
	ResourceKind   string     `json:"resource_kind"`
	ResourceID     string     `json:"resource_id"`
	ResourceName   string     `json:"resource_name"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	ResponseStatus *int       `json:"response_status"`
	DurationMS     *int       `json:"duration_ms"`
	NextAttemptAt  *time.Time `json:"next_attempt_at"`
	RedeliveryOf   *string    `json:"redelivery_of"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// toWebhookDeliveryDTO deliberately omits `payload`: the feed is a status log,
// and the signed body is only ever needed by the receiver that was sent it
// (outbound-webhooks.md §6).
func toWebhookDeliveryDTO(v webhooks.DeliveryView) webhookDeliveryDTO {
	d := v.Delivery
	return webhookDeliveryDTO{
		ID:             d.ID,
		EndpointID:     d.EndpointID,
		EventType:      d.EventType,
		ResourceKind:   d.ResourceKind,
		ResourceID:     d.ResourceID,
		ResourceName:   d.ResourceName,
		Status:         d.Status,
		Attempt:        d.Attempt,
		ResponseStatus: v.ResponseStatus,
		DurationMS:     v.DurationMS,
		NextAttemptAt:  d.NextAttemptAt,
		RedeliveryOf:   d.RedeliveryOf,
		CreatedAt:      d.CreatedAt,
		UpdatedAt:      d.UpdatedAt,
	}
}

// justQueued wraps a delivery that has not been attempted yet — what ping and
// redeliver return, since their first attempt runs detached.
func justQueued(d domain.WebhookDelivery) webhooks.DeliveryView {
	return webhooks.DeliveryView{Delivery: d}
}

// webhookSecretDTO carries the raw signing secret. It is the ONLY response
// shape that ever does, and only at create and rotate (spec §2, §7).
type webhookSecretDTO struct {
	Secret string `json:"secret"`
}

type createWebhookEndpointResponse struct {
	Endpoint webhookEndpointDTO `json:"endpoint"`
	Secret   string             `json:"secret"`
}

type deliveryPageDTO struct {
	Deliveries []webhookDeliveryDTO `json:"deliveries"`
	NextBefore string               `json:"next_before"`
}

type createWebhookEndpointRequest struct {
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Enabled *bool    `json:"enabled"` // default true when omitted
}

func (a *API) handleCreateWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requireProjectRole(w, r, user, r.PathValue("id"), domain.RoleMember) {
		return
	}
	if a.deps.WebhookEndpoints == nil {
		writeError(w, http.StatusNotImplemented, "outbound webhooks are not enabled")
		return
	}
	var req createWebhookEndpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	created, err := a.deps.WebhookEndpoints.Create(r.Context(), r.PathValue("id"), webhooks.CreateInput{
		URL:     req.URL,
		Events:  req.Events,
		Enabled: enabled,
	})
	if err != nil {
		a.writeWebhookError(w, "creating webhook endpoint", err)
		return
	}
	// A brand-new endpoint has no deliveries yet, so its derived fields are
	// known without a read-back: health "unknown", no last delivery.
	writeJSON(w, http.StatusCreated, createWebhookEndpointResponse{
		Endpoint: toWebhookEndpointDTO(webhooks.EndpointView{
			Endpoint: created.Endpoint,
			Health:   domain.EndpointHealthUnknown,
		}),
		Secret: created.Secret,
	})
}

func (a *API) handleListWebhookEndpoints(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requireProjectRole(w, r, user, r.PathValue("id"), domain.RoleMember) {
		return
	}
	if a.deps.WebhookEndpoints == nil {
		writeJSON(w, http.StatusOK, []webhookEndpointDTO{})
		return
	}
	list, err := a.deps.WebhookEndpoints.ListViews(r.Context(), r.PathValue("id"))
	if err != nil {
		a.deps.Log.Error("listing webhook endpoints", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list webhook endpoints")
		return
	}
	out := make([]webhookEndpointDTO, 0, len(list))
	for _, v := range list {
		out = append(out, toWebhookEndpointDTO(v))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeWebhookEndpoint(w, r) {
		return
	}
	v, err := a.deps.WebhookEndpoints.View(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeWebhookError(w, "getting webhook endpoint", err)
		return
	}
	writeJSON(w, http.StatusOK, toWebhookEndpointDTO(v))
}

type patchWebhookEndpointRequest struct {
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Enabled *bool    `json:"enabled"`
}

func (a *API) handlePatchWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeWebhookEndpoint(w, r) {
		return
	}
	cur, err := a.deps.WebhookEndpoints.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeWebhookError(w, "getting webhook endpoint", err)
		return
	}
	var req patchWebhookEndpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// PATCH semantics: unspecified fields keep their current value.
	in := webhooks.UpdateInput{URL: cur.URL, Events: cur.Events, Enabled: cur.Enabled}
	if req.URL != "" {
		in.URL = req.URL
	}
	if req.Events != nil {
		in.Events = req.Events
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	v, err := a.deps.WebhookEndpoints.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		a.writeWebhookError(w, "updating webhook endpoint", err)
		return
	}
	writeJSON(w, http.StatusOK, toWebhookEndpointDTO(v))
}

func (a *API) handleDeleteWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeWebhookEndpoint(w, r) {
		return
	}
	if err := a.deps.WebhookEndpoints.Delete(r.Context(), r.PathValue("id")); err != nil {
		a.writeWebhookError(w, "deleting webhook endpoint", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRotateWebhookSecret mints and seals a fresh signing secret, returning it
// exactly once. There is no overlap window — the previous secret stops
// verifying immediately, and ping confirms the new one (spec §7).
func (a *API) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeWebhookEndpoint(w, r) {
		return
	}
	secret, err := a.deps.WebhookEndpoints.RotateSecret(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeWebhookError(w, "rotating webhook signing secret", err)
		return
	}
	writeJSON(w, http.StatusOK, webhookSecretDTO{Secret: secret})
}

// handlePingWebhookEndpoint delivers a synthetic webhook.ping so an operator can
// prove the wiring before any real deploy. 202: the delivery is recorded and
// attempted detached, so the response means "queued and attempted", not
// "received".
func (a *API) handlePingWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeWebhookEndpoint(w, r) {
		return
	}
	d, err := a.deps.WebhookEndpoints.Ping(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeWebhookError(w, "pinging webhook endpoint", err)
		return
	}
	writeJSON(w, http.StatusAccepted, toWebhookDeliveryDTO(justQueued(d)))
}

// handleListWebhookDeliveries pages the delivery log newest-first with a seek
// cursor: ?limit=50&before=whd_… (spec §7).
func (a *API) handleListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeWebhookEndpoint(w, r) {
		return
	}
	limit := 0 // 0 = the service's default
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	page, err := a.deps.WebhookEndpoints.Deliveries(r.Context(), r.PathValue("id"), limit, r.URL.Query().Get("before"))
	if err != nil {
		a.writeWebhookError(w, "listing webhook deliveries", err)
		return
	}
	out := make([]webhookDeliveryDTO, 0, len(page.Deliveries))
	for _, v := range page.Deliveries {
		out = append(out, toWebhookDeliveryDTO(v))
	}
	writeJSON(w, http.StatusOK, deliveryPageDTO{Deliveries: out, NextBefore: page.NextBefore})
}

// handleRedeliverWebhookDelivery replays a delivery as a new one; the original
// row is never mutated because the log is evidence (spec §7).
func (a *API) handleRedeliverWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForWebhookDelivery(ctx, r.PathValue("id"))
	}) {
		return
	}
	if a.deps.WebhookEndpoints == nil {
		writeError(w, http.StatusNotFound, "delivery not found")
		return
	}
	d, err := a.deps.WebhookEndpoints.Redeliver(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeWebhookError(w, "redelivering webhook delivery", err)
		return
	}
	writeJSON(w, http.StatusAccepted, toWebhookDeliveryDTO(justQueued(d)))
}

// authorizeWebhookEndpoint is the shared preamble for every /webhook-endpoints
// item route: resolve the endpoint to its project, enforce member rank, and
// fail the route closed when the service is not wired.
func (a *API) authorizeWebhookEndpoint(w http.ResponseWriter, r *http.Request) bool {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForWebhookEndpoint(ctx, r.PathValue("id"))
	}) {
		return false
	}
	if a.deps.WebhookEndpoints == nil {
		writeError(w, http.StatusNotFound, "webhook endpoint not found")
		return false
	}
	return true
}

// writeWebhookError maps service errors to status codes.
func (a *API) writeWebhookError(w http.ResponseWriter, op string, err error) {
	var ve *webhooks.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, webhooks.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project not found")
	case errors.Is(err, webhooks.ErrEndpointDisabled):
		writeError(w, http.StatusConflict, "this endpoint is disabled — enable it first")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "webhook endpoint not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "an endpoint with that URL already exists in the project")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
}
