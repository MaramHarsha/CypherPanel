package domain

import "time"

// Resource quotas (resource-quotas.md; permitted by ADR-012).
//
// A QUOTA IS A GUARDRAIL, NOT A METER. It is denominated in bytes and counts;
// there is no price, no rate, no currency, no plan and no tier anywhere in this
// feature, and there is no path by which one arrives without its own recorded
// decision. The panel already refuses work for governance reasons — a freeze
// window, an approval gate, a registry still in use — and this is that
// mechanism with a resource dimension where protection has a clock.

// Quota scopes.
const (
	QuotaScopeProject = "project"
	QuotaScopeTeam    = "team"
)

// Quota dimensions.
const (
	QuotaMemory   = "memory"
	QuotaDisk     = "disk"
	QuotaPreviews = "previews"
)

// Quota states. `warn` is 90%, `exceeded` is 100%.
const (
	QuotaOK       = "ok"
	QuotaWarn     = "warn"
	QuotaExceeded = "exceeded"
)

// ResourceQuota is one scope's caps. A nil limit means that dimension is
// UNCAPPED, so an operator who wants a disk cap writes one number.
type ResourceQuota struct {
	ID        string
	ProjectID string
	TeamID    string
	// MemoryLimitBytes meters DECLARED memory — the sum of memory_limit_mb ×
	// replicas — not observed consumption. That is the one place the obvious
	// design is wrong: a workload that has not started uses nothing, so an
	// observed meter is zero for the very thing being admitted and could never
	// refuse a new application.
	MemoryLimitBytes *int64
	DiskLimitBytes   *int64
	PreviewLimit     *int
	UpdatedBy        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Scope reports which kind of scope this quota is for, and its id.
func (q ResourceQuota) Scope() (kind, id string) {
	if q.TeamID != "" {
		return QuotaScopeTeam, q.TeamID
	}
	return QuotaScopeProject, q.ProjectID
}

// QuotaUsage is one dimension's meter reading.
type QuotaUsage struct {
	Dimension string `json:"dimension"`
	Used      int64  `json:"used"`
	// Limit nil means uncapped, which reads as "no cap" rather than as zero.
	Limit *int64 `json:"limit"`
	State string `json:"state"`
	// Committed is the sum of the PROJECT caps below this scope, on a TEAM
	// report only (resource-quotas.md §3, §9). Nil on a project report, which
	// has nothing below it — nil is "does not apply", never zero.
	//
	// It may exceed Limit, and that is not an error: thin provisioning is the
	// normal case and eleven clients do not peak together, so the
	// over-commitment is SHOWN rather than forbidden.
	Committed *int64 `json:"committed,omitempty"`
	// UncappedProjects counts the projects in this team with no cap of their
	// own on this dimension. It travels WITH Committed because the sum alone is
	// the more flattering half of the truth: zero committed across eleven
	// uncapped projects reads as "nothing promised" when it means "nothing
	// bounded".
	UncappedProjects *int `json:"uncapped_projects,omitempty"`
}

// QuotaState derives the state from a reading. Uncapped is always ok: a
// dimension with no cap cannot be exceeded, and drawing it amber would train
// the operator to ignore the colour.
func QuotaState(used int64, limit *int64) string {
	if limit == nil || *limit <= 0 {
		return QuotaOK
	}
	switch {
	case used >= *limit:
		return QuotaExceeded
	case float64(used) >= float64(*limit)*0.9:
		return QuotaWarn
	default:
		return QuotaOK
	}
}

// QuotaAdmission is the gate's answer.
type QuotaAdmission struct {
	Allowed bool
	// Dimension and Reason are set when it is refused, so the message names
	// which cap and by how much rather than saying "quota exceeded".
	Dimension string
	Reason    string
	// Unlimited names resources with no declared memory limit, which make a
	// memory quota a fiction. Named rather than counted as zero, because the
	// runaway project this exists to stop is precisely the one that never set
	// a limit.
	Unlimited []string
}

// QuotaReport is a scope's whole meter, for the screen.
type QuotaReport struct {
	ScopeKind string       `json:"scope_kind"`
	ScopeID   string       `json:"scope_id"`
	Usage     []QuotaUsage `json:"usage"`
	// UncountedComposeStacks is a real hole, stated rather than hidden: a
	// stack's memory is declared inside its own file, and reading it out would
	// meter a six-service stack as two services' worth and LOOK complete.
	UncountedComposeStacks int      `json:"uncounted_compose_stacks"`
	Unlimited              []string `json:"unlimited"`
}
