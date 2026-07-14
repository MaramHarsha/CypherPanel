package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/events"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type PackagesHandler struct {
	Packages *store.Packages
	Events   *events.Bus
	Audit    *audit.Logger
}

type packageLimits struct {
	DiskMB        int `json:"disk_mb"`
	BandwidthMB   int `json:"bandwidth_mb"`
	Domains       int `json:"domains"`
	Databases     int `json:"databases"`
	EmailAccounts int `json:"email_accounts"`
	CPUQuotaPct   int `json:"cpu_quota_pct"`
	MemoryMaxMB   int `json:"memory_max_mb"`
}

type packageResponse struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Limits    packageLimits `json:"limits"`
	CreatedAt time.Time     `json:"created_at"`
}

type createPackageRequest struct {
	Name   string        `json:"name" binding:"required"`
	Limits packageLimits `json:"limits"`
}

func toPackageResponse(p store.Package) packageResponse {
	return packageResponse{
		ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt,
		Limits: packageLimits(p.Limits),
	}
}

// List returns all hosting packages.
//
//	@Summary  List packages (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Success  200 {array} packageResponse
//	@Security BearerAuth
//	@Router   /admin/packages [get]
func (h *PackagesHandler) List(c *gin.Context) {
	// Root admin sees all packages; a reseller sees only their own.
	pkgs, err := h.Packages.List(c.Request.Context(), auth.OwnerFilter(auth.ClaimsFrom(c)))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]packageResponse, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, toPackageResponse(p))
	}
	c.JSON(http.StatusOK, out)
}

// Create adds a hosting package.
//
//	@Summary  Create a package (root admin only)
//	@Tags     admin
//	@Accept   json
//	@Produce  json
//	@Param    request body createPackageRequest true "Package definition"
//	@Success  201 {object} packageResponse
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/packages [post]
func (h *PackagesHandler) Create(c *gin.Context) {
	var req createPackageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	claims := auth.ClaimsFrom(c)
	pkg, err := h.Packages.Create(c.Request.Context(), req.Name, claims.Subject, store.PackageLimits(req.Limits))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create package"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "package.create", TargetType: "package", TargetID: pkg.ID,
		Detail: map[string]any{"name": pkg.Name}, IP: c.ClientIP(),
	})
	h.Events.Publish(c.Request.Context(), events.SubjectPackageCreated, "package", pkg.ID,
		map[string]any{"id": pkg.ID, "name": pkg.Name})
	c.JSON(http.StatusCreated, toPackageResponse(*pkg))
}

// Delete removes an unused package.
//
//	@Summary  Delete a package (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    id path string true "Package ID"
//	@Success  200 {object} map[string]string
//	@Failure  404 {object} map[string]string
//	@Failure  409 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/packages/{id} [delete]
func (h *PackagesHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	claims := auth.ClaimsFrom(c)

	// Ownership check BEFORE acting: a reseller may only delete its own
	// packages (root admin may delete any). Same 404 for missing and
	// not-owned, so a reseller can't probe other resellers' package IDs.
	pkg, err := h.Packages.GetByID(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, pkg.OwnerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "package not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	err = h.Packages.Delete(c.Request.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "package not found"})
		return
	case errors.Is(err, store.ErrInUse):
		c.JSON(http.StatusConflict, gin.H{"error": "package is in use by existing accounts"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	_ = h.Audit.Record(c.Request.Context(), audit.Entry{
		ActorID: claims.Subject, ActorRole: string(claims.Role),
		Action: "package.delete", TargetType: "package", TargetID: id, IP: c.ClientIP(),
	})
	h.Events.Publish(c.Request.Context(), events.SubjectPackageDeleted, "package", id, map[string]any{"id": id})
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
