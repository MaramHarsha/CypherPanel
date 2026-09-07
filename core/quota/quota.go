// Package quota is admission control on the control plane (resource-quotas.md;
// permitted by ADR-012).
//
// THE FAILURE THIS EXISTS TO STOP. The panel can already cap one CONTAINER.
// What no number in this codebase could express is the AGGREGATE: an agency
// running eleven clients on four boxes can cap every container individually and
// still watch one project open forty preview environments, keep six revisions
// of images per application, and fill the disk a paying client's database
// writes to. Every individual limit was respected, and the outage happened
// anyway.
//
// A QUOTA IS A GUARDRAIL, NOT A METER. No price, no rate, no currency, no plan,
// no tier — in the schema, the API or the UI. The panel already refuses work
// for governance reasons (a freeze window, an approval gate, a registry still
// in use) and this is that mechanism with a resource dimension.
//
// NOTHING NEW RUNS ON A NODE. Nothing new rides the wire. No agent learns that
// quotas exist: the meter is arithmetic over desired state, and the enforcement
// point is where a Deployment is born, which is where deploy protection already
// asks its question.
package quota

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store is the persistence and the meter (consumer-defined).
type Store interface {
	UpsertProjectQuota(ctx context.Context, id, projectID string, q domain.ResourceQuota) (domain.ResourceQuota, error)
	UpsertTeamQuota(ctx context.Context, id, teamID string, q domain.ResourceQuota) (domain.ResourceQuota, error)
	GetProjectQuota(ctx context.Context, projectID string) (domain.ResourceQuota, error)
	GetTeamQuota(ctx context.Context, teamID string) (domain.ResourceQuota, error)
	ListResourceQuotas(ctx context.Context) ([]domain.ResourceQuota, error)
	DeleteProjectQuota(ctx context.Context, projectID string) error
	DeleteTeamQuota(ctx context.Context, teamID string) error

	ProjectDeclaredMemory(ctx context.Context, projectID string) (int64, error)
	ProjectUnlimitedResources(ctx context.Context, projectID string) ([]string, error)
	ProjectComposeStackCount(ctx context.Context, projectID string) (int, error)
	ProjectObservedDisk(ctx context.Context, projectID string) (int64, error)
	ProjectLivePreviews(ctx context.Context, projectID string) (int, error)
	ListProjectsInTeam(ctx context.Context, teamID string) ([]string, error)
	GetProject(ctx context.Context, id string) (domain.Project, error)

	GetQuotaState(ctx context.Context, kind, id, dimension string) (string, time.Time, error)
	SetQuotaState(ctx context.Context, kind, id, dimension, state string) error
}

// Announcer is told when a dimension crosses into warn or exceeded. Once, on
// the TRANSITION — a warning repeated on every deploy is a warning nobody reads
// by the second week.
type Announcer interface {
	AnnounceQuota(ctx context.Context, scopeKind, scopeID, dimension, state string, usage domain.QuotaUsage) error
}

// ValidationError marks bad input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// UnlimitedError is the 409 that NAMES the resources with no declared memory
// limit. A capability is checked when it is ATTACHED, not when it is spent, so
// the operator learns at the moment they can act rather than at 3am when
// somebody's deploy is refused for a reason they cannot see.
type UnlimitedError struct {
	Resources []string
}

func (e *UnlimitedError) Error() string {
	return "these resources have no memory limit, so a memory quota could not be enforced: " +
		joinList(e.Resources)
}

// Delta is what an admission asks for on top of what is already there.
type Delta struct {
	MemoryBytes int64
	Previews    int
}

// Service is the CRUD half and the gate.
type Service struct {
	store    Store
	announce Announcer
	log      *slog.Logger
}

func New(store Store, announce Announcer, log *slog.Logger) *Service {
	return &Service{store: store, announce: announce, log: log}
}

// Set writes a quota after checking it can be enforced.
//
// A cap of 0 is refused: removing a quota is DELETE, and zero means "nothing
// may be deployed here", which is deploy protection's job and says so much
// more clearly.
func (s *Service) Set(ctx context.Context, scopeKind, scopeID string, q domain.ResourceQuota, actor string) (domain.ResourceQuota, error) {
	for name, v := range map[string]*int64{"memory": q.MemoryLimitBytes, "disk": q.DiskLimitBytes} {
		if v != nil && *v <= 0 {
			return domain.ResourceQuota{}, invalid("a " + name + " cap of zero means nothing may be deployed here — remove the quota instead, or use a freeze window if that is what you meant")
		}
	}
	if q.PreviewLimit != nil && *q.PreviewLimit <= 0 {
		return domain.ResourceQuota{}, invalid("a preview cap of zero means no previews — turn previews off on the applications instead, which says so where somebody will read it")
	}

	// A memory cap over a scope containing an unlimited resource is a fiction,
	// so it is refused NAMING them rather than written and quietly ineffective.
	if q.MemoryLimitBytes != nil {
		unlimited, err := s.unlimitedIn(ctx, scopeKind, scopeID)
		if err != nil {
			return domain.ResourceQuota{}, err
		}
		if len(unlimited) > 0 {
			return domain.ResourceQuota{}, &UnlimitedError{Resources: unlimited}
		}
	}

	q.UpdatedBy = actor
	if scopeKind == domain.QuotaScopeTeam {
		return s.store.UpsertTeamQuota(ctx, ids.New(ids.PrefixQuota), scopeID, q)
	}
	return s.store.UpsertProjectQuota(ctx, ids.New(ids.PrefixQuota), scopeID, q)
}

func (s *Service) Delete(ctx context.Context, scopeKind, scopeID string) error {
	if scopeKind == domain.QuotaScopeTeam {
		return s.store.DeleteTeamQuota(ctx, scopeID)
	}
	return s.store.DeleteProjectQuota(ctx, scopeID)
}

func (s *Service) List(ctx context.Context) ([]domain.ResourceQuota, error) {
	return s.store.ListResourceQuotas(ctx)
}

func (s *Service) unlimitedIn(ctx context.Context, scopeKind, scopeID string) ([]string, error) {
	projects, err := s.projectsIn(ctx, scopeKind, scopeID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range projects {
		names, err := s.store.ProjectUnlimitedResources(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, names...)
	}
	return out, nil
}

func (s *Service) projectsIn(ctx context.Context, scopeKind, scopeID string) ([]string, error) {
	if scopeKind == domain.QuotaScopeTeam {
		return s.store.ListProjectsInTeam(ctx, scopeID)
	}
	return []string{scopeID}, nil
}

// Report is the whole meter for one scope, for the screen.
func (s *Service) Report(ctx context.Context, scopeKind, scopeID string) (domain.QuotaReport, error) {
	q, err := s.quotaFor(ctx, scopeKind, scopeID)
	if err != nil {
		// No quota is not an error: an uncapped scope still has a meter worth
		// showing, and it reads as "no cap" rather than as a failure.
		q = domain.ResourceQuota{}
	}
	memory, disk, previews, stacks, unlimited, err := s.meter(ctx, scopeKind, scopeID)
	if err != nil {
		return domain.QuotaReport{}, err
	}
	previewLimit := int64Ptr(q.PreviewLimit)
	return domain.QuotaReport{
		ScopeKind: scopeKind, ScopeID: scopeID,
		Usage: []domain.QuotaUsage{
			{Dimension: domain.QuotaMemory, Used: memory, Limit: q.MemoryLimitBytes,
				State: domain.QuotaState(memory, q.MemoryLimitBytes)},
			{Dimension: domain.QuotaDisk, Used: disk, Limit: q.DiskLimitBytes,
				State: domain.QuotaState(disk, q.DiskLimitBytes)},
			{Dimension: domain.QuotaPreviews, Used: int64(previews), Limit: previewLimit,
				State: domain.QuotaState(int64(previews), previewLimit)},
		},
		UncountedComposeStacks: stacks,
		Unlimited:              unlimited,
	}, nil
}

func int64Ptr(v *int) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

func (s *Service) quotaFor(ctx context.Context, scopeKind, scopeID string) (domain.ResourceQuota, error) {
	if scopeKind == domain.QuotaScopeTeam {
		return s.store.GetTeamQuota(ctx, scopeID)
	}
	return s.store.GetProjectQuota(ctx, scopeID)
}

func (s *Service) meter(ctx context.Context, scopeKind, scopeID string) (memory, disk int64, previews, stacks int, unlimited []string, err error) {
	projects, err := s.projectsIn(ctx, scopeKind, scopeID)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	for _, p := range projects {
		m, err := s.store.ProjectDeclaredMemory(ctx, p)
		if err != nil {
			return 0, 0, 0, 0, nil, err
		}
		d, err := s.store.ProjectObservedDisk(ctx, p)
		if err != nil {
			return 0, 0, 0, 0, nil, err
		}
		pv, err := s.store.ProjectLivePreviews(ctx, p)
		if err != nil {
			return 0, 0, 0, 0, nil, err
		}
		cs, err := s.store.ProjectComposeStackCount(ctx, p)
		if err != nil {
			return 0, 0, 0, 0, nil, err
		}
		names, err := s.store.ProjectUnlimitedResources(ctx, p)
		if err != nil {
			return 0, 0, 0, 0, nil, err
		}
		memory += m
		disk += d
		previews += pv
		stacks += cs
		unlimited = append(unlimited, names...)
	}
	return memory, disk, previews, stacks, unlimited, nil
}

// Admit is the gate. It answers for the PROJECT's own quota and for its team's,
// and a refusal by either is a refusal — a team cap that a project could
// exceed by having its own is not a cap.
func (s *Service) Admit(ctx context.Context, projectID string, delta Delta) (domain.QuotaAdmission, error) {
	scopes := []struct{ kind, id string }{{domain.QuotaScopeProject, projectID}}
	if proj, err := s.store.GetProject(ctx, projectID); err == nil && proj.TeamID != "" {
		scopes = append(scopes, struct{ kind, id string }{domain.QuotaScopeTeam, proj.TeamID})
	}

	for _, sc := range scopes {
		q, err := s.quotaFor(ctx, sc.kind, sc.id)
		if err != nil {
			continue // no quota on this scope is not a refusal
		}
		memory, disk, previews, _, _, err := s.meter(ctx, sc.kind, sc.id)
		if err != nil {
			// A meter that cannot be read must not refuse: a guardrail that
			// fails closed on its own database error becomes the outage it was
			// installed to prevent.
			s.log.Error("quota: could not meter a scope; admitting", "scope", sc.kind, "id", sc.id, "error", err)
			continue
		}

		if q.MemoryLimitBytes != nil && memory+delta.MemoryBytes > *q.MemoryLimitBytes {
			return s.refuse(ctx, sc.kind, sc.id, domain.QuotaMemory,
				fmt.Sprintf("this %s is at %s of its %s memory cap, and this would need %s more",
					sc.kind, bytes(memory), bytes(*q.MemoryLimitBytes), bytes(delta.MemoryBytes)),
				memory, q.MemoryLimitBytes)
		}
		if q.DiskLimitBytes != nil && disk >= *q.DiskLimitBytes {
			return s.refuse(ctx, sc.kind, sc.id, domain.QuotaDisk,
				fmt.Sprintf("this %s is using %s of its %s disk cap", sc.kind, bytes(disk), bytes(*q.DiskLimitBytes)),
				disk, q.DiskLimitBytes)
		}
		if delta.Previews > 0 && q.PreviewLimit != nil && previews+delta.Previews > *q.PreviewLimit {
			limit := int64(*q.PreviewLimit)
			return s.refuse(ctx, sc.kind, sc.id, domain.QuotaPreviews,
				fmt.Sprintf("this %s already has %d of its %d live previews", sc.kind, previews, *q.PreviewLimit),
				int64(previews), &limit)
		}

		// Not refused — but a crossing into `warn` is announced here, once.
		s.announceIfChanged(ctx, sc.kind, sc.id, domain.QuotaMemory, memory, q.MemoryLimitBytes)
		s.announceIfChanged(ctx, sc.kind, sc.id, domain.QuotaDisk, disk, q.DiskLimitBytes)
		s.announceIfChanged(ctx, sc.kind, sc.id, domain.QuotaPreviews, int64(previews), int64Ptr(q.PreviewLimit))
	}
	return domain.QuotaAdmission{Allowed: true}, nil
}

func (s *Service) refuse(ctx context.Context, kind, id, dimension, reason string, used int64, limit *int64) (domain.QuotaAdmission, error) {
	s.announceIfChanged(ctx, kind, id, dimension, used, limit)
	return domain.QuotaAdmission{Allowed: false, Dimension: dimension, Reason: reason}, nil
}

// announceIfChanged fires on the TRANSITION only. The stored state is what makes
// that possible: without it a scope sitting at 92% would announce on every
// deploy, and the message would stop being read by the second week.
func (s *Service) announceIfChanged(ctx context.Context, kind, id, dimension string, used int64, limit *int64) {
	state := domain.QuotaState(used, limit)
	previous, _, err := s.store.GetQuotaState(ctx, kind, id, dimension)
	if err == nil && previous == state {
		return
	}
	if err := s.store.SetQuotaState(ctx, kind, id, dimension, state); err != nil {
		s.log.Debug("quota: recording a state change", "scope", kind, "id", id, "error", err)
	}
	// Recovery back to ok is worth saying too: an operator who acted on a
	// warning should learn that it worked.
	if s.announce == nil || (state == domain.QuotaOK && previous == "") {
		return
	}
	if err := s.announce.AnnounceQuota(ctx, kind, id, dimension, state, domain.QuotaUsage{
		Dimension: dimension, Used: used, Limit: limit, State: state,
	}); err != nil {
		s.log.Error("quota: announcing a state change", "scope", kind, "id", id, "error", err)
	}
}

func bytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func joinList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	out := ""
	for i, s := range items {
		switch {
		case i == 0:
			out = s
		case i == len(items)-1:
			out += " and " + s
		default:
			out += ", " + s
		}
	}
	return out
}
