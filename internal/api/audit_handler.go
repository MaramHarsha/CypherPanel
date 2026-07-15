package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/audit"
)

// AuditHandler serves the audit-log dashboard (root-admin only). The log is the
// durable record of privileged actions; this is a read/query surface over it.
type AuditHandler struct {
	Audit *audit.Logger
}

// List returns audit entries newest-first, filterable by action prefix.
//
//	@Summary  List audit-log entries (root admin only)
//	@Tags     admin
//	@Produce  json
//	@Param    action query string false "Filter by action prefix (e.g. account.)"
//	@Param    limit  query int    false "Max entries (default 100, max 500)"
//	@Param    offset query int    false "Offset for pagination"
//	@Success  200 {array} audit.Record
//	@Security BearerAuth
//	@Router   /admin/audit [get]
func (h *AuditHandler) List(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	records, err := h.Audit.List(c.Request.Context(), c.Query("action"), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if records == nil {
		records = []audit.Record{}
	}
	c.JSON(http.StatusOK, records)
}
