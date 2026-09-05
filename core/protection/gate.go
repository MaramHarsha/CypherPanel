package protection

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// Admit is the gate the scheduler consults once, in Deploy and Rollback, at the
// moment a Deployment is born and before any work item is published (§1).
//
// It returns a plain domain.DeployAdmission, and it FAILS CLOSED: an unloadable
// time zone, an unreadable protection row, or any store error refuses the
// deploy rather than passing it. A protection control that fails open is worse
// than none, because it is trusted (§5).
//
// Freeze is evaluated first and refuses before any row is written, so a refused
// deploy leaves no orphan Revision behind: a hard "not now" is more useful than
// parking something that would have to be refused later anyway.
func (s *Service) Admit(ctx context.Context, environmentID string) (domain.DeployAdmission, error) {
	env, err := s.store.GetEnvironment(ctx, environmentID)
	if err != nil {
		return domain.DeployAdmission{}, fmt.Errorf("protection: getting environment %s: %w", environmentID, err)
	}
	// Preview environments are unprotected by design (§9): they are ordinary
	// environments with a TTL, and freezing them would strand every open PR.
	// Checked here rather than trusted to the absence of a row, because the
	// write path refuses to create one and this is the half that makes that
	// refusal meaningful.
	if env.Kind == domain.EnvPreview {
		return domain.DeployAdmission{}, nil
	}

	p, err := s.store.GetEnvironmentProtection(ctx, environmentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.DeployAdmission{}, nil // never protected: nothing to enforce
		}
		return domain.DeployAdmission{}, fmt.Errorf("protection: getting protection for %s: %w", environmentID, err)
	}

	var adm domain.DeployAdmission
	if p.RequireApproval {
		adm.NeedsApproval = true
		adm.RequiredRole = p.MinApproverRole
		if !domain.ValidRole(adm.RequiredRole) {
			// A corrupt rank must not widen who may approve; the narrowest
			// role in the set is the safe reading.
			adm.RequiredRole = domain.RoleOwner
		}
	}

	frozen, detail, err := s.freezeState(env, p)
	if err != nil {
		return domain.DeployAdmission{}, err
	}
	if !frozen {
		return adm, nil
	}
	// Break glass suspends the freeze — and only the freeze. It is deliberately
	// not consulted for the approval branch: two independent controls (§4).
	open, err := s.store.BreakGlassOpen(ctx, environmentID, s.now())
	if err != nil {
		return domain.DeployAdmission{}, fmt.Errorf("protection: checking break glass for %s: %w", environmentID, err)
	}
	if open {
		s.log.Info("deploy admitted through an open break-glass grant",
			"environment_id", environmentID, "detail", detail)
		return adm, nil
	}
	adm.Frozen = true
	adm.FreezeDetail = detail
	return adm, nil
}

// freezeState reports whether the environment is inside a freeze window right
// now, and the sentence that names the window and when it lifts.
//
// When several windows overlap, the one that lifts LAST is named: telling an
// operator to come back at 08:00 when a second window runs to 10:00 would be a
// lie the next attempt exposes.
//
// A window whose zone will not load refuses the deploy (§5). The refusal names
// the window so the fix — correct the zone — is visible from the response, and
// the zone name is echoed back verbatim: it is operator-supplied configuration,
// not a secret.
func (s *Service) freezeState(env domain.Environment, p domain.EnvironmentProtection) (bool, string, error) {
	if !p.FreezeEnabled || len(p.Windows) == 0 {
		return false, "", nil
	}
	now := s.now()
	var (
		frozen   bool
		latest   time.Time
		detail   string
		badZones []string
	)
	for _, w := range p.Windows {
		in, err := InWindow(w, now)
		if err != nil {
			// Fail closed, but keep evaluating: a second, loadable window may
			// name a later lift, and the operator should see the real one.
			s.log.Error("evaluating freeze window", "environment_id", env.ID, "window_id", w.ID,
				"timezone", w.Timezone, "error", err)
			badZones = append(badZones, w.Timezone)
			continue
		}
		if !in {
			continue
		}
		lift, err := LiftsAt(w, now)
		if err != nil {
			badZones = append(badZones, w.Timezone)
			continue
		}
		frozen = true
		if lift.After(latest) {
			latest, detail = lift, FrozenUntil(env.Name, w, lift)
		}
	}
	if frozen {
		return true, detail, nil
	}
	if len(badZones) > 0 {
		return true, fmt.Sprintf(
			"%s has a freeze window in a time zone this panel cannot load (%q), so deploys are refused until it is corrected",
			env.Name, badZones[0]), nil
	}
	return false, "", nil
}

// Park records the gate decision for a deployment the scheduler has just
// parked: one pending DeployApproval row, then the inbox items that tell the
// people who can act (§9).
//
// The row is written FIRST and its failure is returned, because a deployment
// sitting in awaiting_approval with no approval row could never be decided —
// the scheduler fails such a deployment rather than leaving it stranded. The
// announcement is best-effort by contrast: an inbox write that fails leaves the
// approval reachable from the environment's approval queue and the deployment
// drawer, so it is logged, not propagated.
func (s *Service) Park(ctx context.Context, dep domain.Deployment, environmentID, requestedBy, requiredRole string) error {
	if _, err := s.store.CreateDeployApproval(ctx, dep.ID, environmentID, requestedBy, requiredRole); err != nil {
		return fmt.Errorf("protection: creating approval for %s: %w", dep.ID, err)
	}
	s.log.Info("deploy parked awaiting approval",
		"deployment_id", dep.ID, "app_id", dep.ApplicationID,
		"environment_id", environmentID, "required_role", requiredRole,
		"requested_by", requestedBy)

	if s.announcer == nil {
		return nil
	}
	// Detached: Park runs inside the scheduler's pipeline lock, so an inbox
	// fan-out here would hold every other deploy behind a handful of writes.
	s.detach(ctx, func(c context.Context) {
		notice, err := s.noticeFor(c, dep, environmentID)
		if err != nil {
			s.log.Warn("composing awaiting-approval notice", "deployment_id", dep.ID, "error", err)
			return
		}
		notice.RequiredRole = requiredRole
		notice.RequestedBy = requestedBy
		notice.RequesterEmail = s.emailOf(c, requestedBy)
		if err := s.announcer.RecordDeployAwaitingApproval(c, notice); err != nil {
			s.log.Error("recording awaiting-approval inbox items", "deployment_id", dep.ID, "error", err)
		}
	})
	return nil
}
