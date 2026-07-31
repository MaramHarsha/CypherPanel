package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// MetricsHandler serves the Prometheus scrape endpoint and the scoped Metrics
// API (plan.md §16). Current state is read from Postgres (latest snapshot);
// historical time-series live in the operator's TSDB scraping /metrics — never
// in Postgres (observability-and-metrics skill: the cardinal rule).
type MetricsHandler struct {
	Servers   *store.Servers
	Accounts  *store.Accounts
	Databases *store.Databases
	FTP       *store.FTPAccounts
}

// promMetric writes one Prometheus sample line.
func promMetric(b *strings.Builder, name, labels string, value any) {
	if labels != "" {
		fmt.Fprintf(b, "%s{%s} %v\n", name, labels, value)
	} else {
		fmt.Fprintf(b, "%s %v\n", name, value)
	}
}

func promLabel(k, v string) string {
	return fmt.Sprintf("%s=%q", k, v)
}

// Prometheus renders the fleet's current state in the Prometheus text exposition
// format for operators to scrape. Unauthenticated like /healthz — operators
// must network-restrict it (documented). Metric names follow the
// cypher_<subsystem>_<unit> convention with bounded label cardinality.
//
//	@Summary  Prometheus metrics scrape endpoint
//	@Tags     metrics
//	@Produce  plain
//	@Success  200 {string} string
//	@Router   /metrics [get]
func (h *MetricsHandler) Prometheus(c *gin.Context) {
	ctx := c.Request.Context()
	var b strings.Builder

	servers, _ := h.Servers.List(ctx, "")
	online := 0
	b.WriteString("# HELP cypher_server_up Whether the agent on a server is reporting (1) or not (0).\n")
	b.WriteString("# TYPE cypher_server_up gauge\n")
	for _, s := range servers {
		up := 0
		if s.AgentStatus == "online" {
			up = 1
			online++
		}
		lbl := promLabel("server", s.Name)
		promMetric(&b, "cypher_server_up", lbl, up)
	}
	b.WriteString("# HELP cypher_server_load1 Server 1-minute load average.\n# TYPE cypher_server_load1 gauge\n")
	b.WriteString("# HELP cypher_server_memory_used_bytes Server memory used.\n# TYPE cypher_server_memory_used_bytes gauge\n")
	b.WriteString("# HELP cypher_server_memory_total_bytes Server memory total.\n# TYPE cypher_server_memory_total_bytes gauge\n")
	b.WriteString("# HELP cypher_server_disk_used_bytes Server disk used.\n# TYPE cypher_server_disk_used_bytes gauge\n")
	for _, s := range servers {
		lbl := promLabel("server", s.Name)
		promMetric(&b, "cypher_server_load1", lbl, s.Stats.Load1m)
		promMetric(&b, "cypher_server_memory_used_bytes", lbl, s.Stats.MemoryUsedBytes)
		promMetric(&b, "cypher_server_memory_total_bytes", lbl, s.Stats.MemoryTotalBytes)
		promMetric(&b, "cypher_server_disk_used_bytes", lbl, s.Stats.DiskUsedBytes)
	}

	b.WriteString("# HELP cypher_servers_total Total registered servers.\n# TYPE cypher_servers_total gauge\n")
	promMetric(&b, "cypher_servers_total", "", len(servers))
	b.WriteString("# HELP cypher_servers_online Servers currently reporting.\n# TYPE cypher_servers_online gauge\n")
	promMetric(&b, "cypher_servers_online", "", online)

	accounts, _ := h.Accounts.List(ctx, "")
	byStatus := map[string]int{}
	for _, a := range accounts {
		byStatus[a.Status]++
	}
	b.WriteString("# HELP cypher_accounts_total Hosting accounts by status.\n# TYPE cypher_accounts_total gauge\n")
	for status, n := range byStatus {
		promMetric(&b, "cypher_accounts_total", promLabel("status", status), n)
	}

	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
}

// Scoped serves the JSON Metrics API for a scope (server | account | domain).
// Resource-scoped: resellers see only their own accounts.
//
//	@Summary  Metrics for a scope (server, account, domain)
//	@Tags     admin
//	@Produce  json
//	@Param    scope path string true "server | account | domain"
//	@Success  200 {object} map[string]any
//	@Failure  400 {object} map[string]string
//	@Security BearerAuth
//	@Router   /admin/metrics/{scope} [get]
func (h *MetricsHandler) Scoped(c *gin.Context) {
	ctx := c.Request.Context()
	switch c.Param("scope") {
	case "server":
		servers, err := h.Servers.List(ctx, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		out := make([]gin.H, 0, len(servers))
		for _, s := range servers {
			out = append(out, gin.H{
				"server": s.Name, "online": s.AgentStatus == "online",
				"load1": s.Stats.Load1m,
				"memory_used_bytes": s.Stats.MemoryUsedBytes, "memory_total_bytes": s.Stats.MemoryTotalBytes,
				"disk_used_bytes": s.Stats.DiskUsedBytes, "disk_total_bytes": s.Stats.DiskTotalBytes,
			})
		}
		c.JSON(http.StatusOK, gin.H{"scope": "server", "metrics": out})

	case "account":
		// Reseller-scoped: only the caller's accounts.
		accounts, err := h.Accounts.List(ctx, auth.OwnerFilter(auth.ClaimsFrom(c)))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		byStatus := map[string]int{}
		for _, a := range accounts {
			byStatus[a.Status]++
		}
		c.JSON(http.StatusOK, gin.H{"scope": "account", "total": len(accounts), "by_status": byStatus})

	case "domain":
		// Per-domain historical metrics come from the TSDB (scrape /metrics);
		// current-state per domain is exposed via the account resources.
		c.JSON(http.StatusOK, gin.H{
			"scope": "domain",
			"note":  "per-domain time-series are served from the Prometheus/VictoriaMetrics store scraping /metrics",
		})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "scope must be server, account or domain"})
	}
}
