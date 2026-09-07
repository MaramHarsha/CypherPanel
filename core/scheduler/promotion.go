package scheduler

// Revision promotion (revision-promotion.md): ship the artifact that was
// tested, rather than rebuilding one that should be the same.
//
// The whole idea already exists in start(): a revision that names an image does
// not build, which is the branch rollback has used since Phase 2. A promotion
// is the same trick pointed sideways instead of backwards.
//
// THE MOST IMPORTANT DECISION IN THE SLICE is that the promoted revision names
// the TARGET application's own canonical tag, not the source's. The agent
// parses ownership out of a managed tag, and garbage collection reclaims every
// managed reference whose application is absent from that server's desired set
// — so a revision carrying the source's tag would, on the target server, belong
// to an application that is not desired there, and the first reconcile after
// the rollout would try to reclaim the image production is serving from. The
// retain set cannot save it either: it is keyed by application id.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ErrNotPromotable is a source that cannot be promoted, with the reason.
type ErrNotPromotable struct{ Detail string }

func (e *ErrNotPromotable) Error() string { return e.Detail }

// PromotionPlan is exactly what would change, computed before anything is
// written. It is the screen's whole content, and it is a GET.
type PromotionPlan struct {
	SourceApplication string `json:"source_application"`
	TargetApplication string `json:"target_application"`
	SourceRevisionID  string `json:"source_revision_id"`
	SourceCommit      string `json:"source_commit"`
	// TargetRevisionID is what production is serving now, so the card can say
	// what is being replaced rather than only what is arriving.
	TargetRevisionID string `json:"target_revision_id"`
	Image            string `json:"image"`
	// SameServer says whether the artifact already exists where it is needed.
	// A same-server promotion is instant; a cross-server one relays.
	SameServer bool `json:"same_server"`

	// NOTHING ABOUT ENVIRONMENT VARIABLES IS EVER COPIED by a promotion. Not
	// optionally, not behind a checkbox, not with a confirmation: copying
	// variables across environments is the most effective way there is to point
	// production at a staging database, and a panel that offers it will
	// eventually do it. The panel's job is to make the disagreement VISIBLE at
	// the moment it matters and then get out of the way.
	OnlyInSource []string `json:"only_in_source"`
	OnlyInTarget []string `json:"only_in_target"`
	// UnresolvedInTarget is the blocker: a {{shared.KEY}} the target's scope
	// cannot resolve. Reported here so it is fixed before the deploy rather
	// than discovered by a failed one.
	UnresolvedInTarget []string `json:"unresolved_in_target"`

	// Blockers are the reasons this cannot proceed. Empty means it can.
	Blockers []string `json:"blockers"`
	// Note is the honest caveat that has no fix inside the panel.
	Note string `json:"note"`
}

// buildTimeNote is the cost of moving the tested artifact rather than
// rebuilding, stated because nothing in the panel can detect it.
const buildTimeNote = "The image that runs in production will be byte-for-byte the one that ran in staging, " +
	"including anything baked in at build time — an API URL compiled into a bundle, a config file substituted " +
	"by an ARG. If this application bakes environment configuration into its image, promoting moves staging's " +
	"configuration into production and the deploy will look perfectly successful. The remedy is a " +
	"runtime-configured image; the panel cannot detect this."

// PlanPromotion computes what a promotion would do. It writes nothing, which is
// why the route that calls it is a GET.
func (s *Scheduler) PlanPromotion(ctx context.Context, sourceRevID, targetAppID string) (PromotionPlan, error) {
	rev, err := s.store.GetRevision(ctx, sourceRevID)
	if err != nil {
		return PromotionPlan{}, fmt.Errorf("scheduler: reading the source revision: %w", err)
	}
	source, err := s.store.GetApplication(ctx, rev.ApplicationID)
	if err != nil {
		return PromotionPlan{}, fmt.Errorf("scheduler: reading the source application: %w", err)
	}
	target, err := s.store.GetApplication(ctx, targetAppID)
	if err != nil {
		return PromotionPlan{}, fmt.Errorf("scheduler: reading the target application: %w", err)
	}

	plan := PromotionPlan{
		SourceApplication: source.Name, TargetApplication: target.Name,
		SourceRevisionID: rev.ID, SourceCommit: rev.SourceCommit,
		Image:      rev.Image,
		SameServer: source.Runtime.ServerID == target.Runtime.ServerID,
		Note:       buildTimeNote,
	}
	if target.ObservedRevisionID != "" {
		plan.TargetRevisionID = target.ObservedRevisionID
	}

	// A revision with no image was never built, and there is nothing to move.
	if rev.Image == "" {
		plan.Blockers = append(plan.Blockers,
			"this revision has no image — it was never built, so there is no artifact to promote")
	}
	if source.ID == target.ID {
		plan.Blockers = append(plan.Blockers, "an application cannot be promoted to itself")
	}

	// The two must be the same application in different environments, or the
	// promotion is moving an artifact between things that are not versions of
	// each other. Same PROJECT is the check the panel can actually make.
	srcEnv, serr := s.store.GetEnvironment(ctx, source.EnvironmentID)
	tgtEnv, terr := s.store.GetEnvironment(ctx, target.EnvironmentID)
	if serr == nil && terr == nil && srcEnv.ProjectID != tgtEnv.ProjectID {
		plan.Blockers = append(plan.Blockers,
			"these applications are in different projects — a promotion moves an artifact between environments of one project")
	}

	// Env drift, from KEY NAMES ONLY. Values that differ are deliberately not
	// reported: a key present in both with a different value is not drift, it
	// is the normal, intended difference between environments, and flagging it
	// would make the card noise.
	srcKeys, err := s.envKeys(ctx, source.ID)
	if err != nil {
		return plan, err
	}
	tgtKeys, err := s.envKeys(ctx, target.ID)
	if err != nil {
		return plan, err
	}
	plan.OnlyInSource = difference(srcKeys, tgtKeys)
	plan.OnlyInTarget = difference(tgtKeys, srcKeys)

	// A {{shared.KEY}} the target's scope cannot resolve would fail the deploy
	// at start(); surfacing it here turns a failed deploy into a fixable plan.
	if _, err := s.resolveEnv(ctx, target, envStrict); err != nil {
		var unresolved *UnresolvedReferenceError
		if errors.As(err, &unresolved) {
			plan.UnresolvedInTarget = append(plan.UnresolvedInTarget, unresolved.Error())
			plan.Blockers = append(plan.Blockers, unresolved.Error())
		}
	}
	return plan, nil
}

func (s *Scheduler) envKeys(ctx context.Context, appID string) ([]string, error) {
	vars, err := s.store.ListEnvVars(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: listing environment variables: %w", err)
	}
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		out = append(out, v.Key)
	}
	sort.Strings(out)
	return out, nil
}

func difference(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, s := range b {
		in[s] = true
	}
	out := []string{}
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

// Promote ships the source revision's artifact to the target application.
//
// It creates a revision on the TARGET carrying the target's own canonical image
// tag, records the source's server as the one holding the artifact, and lets
// start() take it to distribute. No build runs and no source is fetched: the
// thing that ships is the thing that was tested.
func (s *Scheduler) Promote(ctx context.Context, sourceRevID, targetAppID, requestedBy string) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	plan, err := s.PlanPromotion(ctx, sourceRevID, targetAppID)
	if err != nil {
		return domain.Deployment{}, err
	}
	if len(plan.Blockers) > 0 {
		return domain.Deployment{}, &ErrNotPromotable{Detail: plan.Blockers[0]}
	}

	source, err := s.store.GetApplication(ctx, mustSourceApp(ctx, s, sourceRevID))
	if err != nil {
		return domain.Deployment{}, err
	}
	target, err := s.store.GetApplication(ctx, targetAppID)
	if err != nil {
		return domain.Deployment{}, err
	}

	// The gate runs BEFORE anything is written, exactly as an ordinary deploy's
	// does: a promotion is a deploy, and a frozen environment refuses it for
	// the same reason.
	admission, err := s.admit(ctx, target)
	if err != nil {
		return domain.Deployment{}, err
	}
	if admission.Frozen {
		return domain.Deployment{}, &FrozenError{Detail: admission.FreezeDetail}
	}
	if qerr := s.admitQuota(ctx, target); qerr != nil {
		return domain.Deployment{}, qerr
	}

	// The TARGET's configuration, not the source's: a promotion moves the
	// artifact and nothing else. Port, health check, route and limits are the
	// target's own, which is what makes "promote the image, keep the
	// environment" true rather than aspirational.
	snapshot, err := snapshotOf(target)
	if err != nil {
		return domain.Deployment{}, err
	}
	revID := ids.New(ids.PrefixRevision)
	rev, err := s.store.CreatePromotedRevision(ctx, revID, target.ID,
		plan.SourceCommit, snapshot, imageTag(target.ID, revID), sourceRevID)
	if err != nil {
		return domain.Deployment{}, err
	}

	dep, err := s.store.CreateDeployment(ctx, ids.New(ids.PrefixDeployment), target.ID, rev.ID, "promotion")
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: creating the promotion deployment: %w", err)
	}
	// The SOURCE's server holds the artifact, which is exactly what
	// builder_server_id already means: the server this deployment's image comes
	// from, and the one the relay authorizes a push from.
	if dep, err = s.store.SetDeploymentBuilder(ctx, dep.ID, source.Runtime.ServerID); err != nil {
		return domain.Deployment{}, fmt.Errorf("scheduler: recording the artifact's server: %w", err)
	}

	if admission.NeedsApproval {
		return s.park(ctx, dep, target, admission, requestedBy)
	}
	if serr := s.tryStart(ctx, dep); serr != nil {
		return dep, serr
	}
	return s.store.GetDeployment(ctx, dep.ID)
}

// mustSourceApp reads the source revision's application id. A helper because
// the plan already validated the revision exists, and re-handling that error
// here would be noise.
func mustSourceApp(ctx context.Context, s *Scheduler, revID string) string {
	rev, err := s.store.GetRevision(ctx, revID)
	if err != nil {
		return ""
	}
	return rev.ApplicationID
}

// promotedSourceImage resolves the tag the artifact currently lives under on
// the source's server, which is what the relay pushes.
//
// It never returns an error: the fallback IS the answer. A source revision that
// was deleted leaves the target's own name, under which the image may still be
// present from a previous rollout — which degrades a rollback-to-a-promotion to
// exactly today's behaviour rather than failing it.
func (s *Scheduler) promotedSourceImage(ctx context.Context, rev domain.Revision) string {
	if rev.PromotedFromRevisionID == "" {
		return rev.Image
	}
	src, err := s.store.GetRevision(ctx, rev.PromotedFromRevisionID)
	if err != nil || src.Image == "" {
		return rev.Image
	}
	return src.Image
}
