package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type TasksHandler struct {
	Tasks     *store.Tasks
	Publisher *jobs.Publisher
	Audit     *audit.Logger
}

type createTaskRequest struct {
	Type    string          `json:"type" binding:"required"`
	Payload json.RawMessage `json:"payload"`
}

// Create dispatches a task to a server's agent: persist first (durable
// record), then publish; the task ID doubles as the JetStream dedup key so a
// republish after a crash between the two steps is safe.
//
//	@Summary  Dispatch a task to a server's agent (root admin only)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    id      path string            true "Server ID"
//	@Param    request body createTaskRequest true "Task type and payload"
//	@Success  202 {object} map[string]string
//	@Failure  400 {object} map[string]string
//	@Failure  403 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/servers/{id}/tasks [post]
func (h *TasksHandler) Create(c *gin.Context) {
	serverID := c.Param("id")
	var req createTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type is required"})
		return
	}
	if !jobs.ValidType(req.Type) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown task type"})
		return
	}

	claims := auth.ClaimsFrom(c)
	task, err := h.Tasks.Create(c.Request.Context(), serverID, req.Type, req.Payload, claims.Subject)
	if err != nil {
		slog.Error("creating task", "server_id", serverID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create task"})
		return
	}

	if err := h.Publisher.Publish(c.Request.Context(), jobs.Task{
		ID: task.ID, ServerID: task.ServerID, Type: task.Type, Payload: task.Payload,
	}); err != nil {
		slog.Error("publishing task", "task_id", task.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "task recorded but not dispatched; it will need republishing"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "task.create", TargetType: "task", TargetID: task.ID,
		Detail: map[string]any{"server_id": serverID, "type": task.Type},
		IP:     c.ClientIP(),
	})
	c.JSON(http.StatusAccepted, gin.H{"id": task.ID, "status": task.Status})
}

// Get returns a task's current status.
//
//	@Summary  Inspect a task (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Task ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/tasks/{id} [get]
func (h *TasksHandler) Get(c *gin.Context) {
	task, err := h.Tasks.GetByID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":         task.ID,
		"server_id":  task.ServerID,
		"type":       task.Type,
		"status":     task.Status,
		"error":      task.Error,
		"created_at": task.CreatedAt,
		"updated_at": task.UpdatedAt,
	})
}
