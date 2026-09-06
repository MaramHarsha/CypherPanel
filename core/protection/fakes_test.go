package protection

// In-memory doubles for the protection service's three seams. They model only
// the behaviour the tests assert on — the once-only decision, the rank-narrowed
// approver count, grant expiry — because a fake that reimplements Postgres
// proves nothing about Postgres, which is what
// core/store/deploy_protection_test.go is for (ENGINEERING rule 29).

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type fakeStore struct {
	mu sync.Mutex

	envs        map[string]domain.Environment
	apps        map[string]domain.Application
	deployments map[string]domain.Deployment
	revisions   map[string]domain.Revision
	users       map[string]domain.User

	protection map[string]domain.EnvironmentProtection
	approvals  map[string]domain.DeployApproval
	grants     map[string][]domain.BreakGlassGrant

	// approvers maps a project id to how many members rank at or above each
	// role, keyed "projectID/role"; the count query subtracts the excluded
	// user when they themselves qualify.
	approvers map[string]int64
	// qualifies records which users count toward that total.
	qualifies map[string]bool

	// failProtection makes the policy read fail, for the fail-closed path.
	failProtection bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		envs:        map[string]domain.Environment{},
		apps:        map[string]domain.Application{},
		deployments: map[string]domain.Deployment{},
		revisions:   map[string]domain.Revision{},
		users:       map[string]domain.User{},
		protection:  map[string]domain.EnvironmentProtection{},
		approvals:   map[string]domain.DeployApproval{},
		grants:      map[string][]domain.BreakGlassGrant{},
		approvers:   map[string]int64{},
		qualifies:   map[string]bool{},
	}
}

func (f *fakeStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.envs[id]
	if !ok {
		return domain.Environment{}, store.ErrNotFound
	}
	return e, nil
}

func (f *fakeStore) GetApplication(_ context.Context, id string) (domain.Application, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.apps[id]
	if !ok {
		return domain.Application{}, store.ErrNotFound
	}
	return a, nil
}

func (f *fakeStore) GetDeployment(_ context.Context, id string) (domain.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deployments[id]
	if !ok {
		return domain.Deployment{}, store.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) GetRevision(_ context.Context, id string) (domain.Revision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.revisions[id]
	if !ok {
		return domain.Revision{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) GetUserByID(_ context.Context, id string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) GetEnvironmentProtection(_ context.Context, envID string) (domain.EnvironmentProtection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failProtection {
		return domain.EnvironmentProtection{}, errors.New("boom")
	}
	p, ok := f.protection[envID]
	if !ok {
		return domain.EnvironmentProtection{}, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) SetEnvironmentProtection(_ context.Context, p domain.EnvironmentProtection) (domain.EnvironmentProtection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p.CreatedAt, p.UpdatedAt = time.Unix(0, 0).UTC(), time.Unix(0, 0).UTC()
	f.protection[p.EnvironmentID] = p
	return p, nil
}

func (f *fakeStore) CreateDeployApproval(_ context.Context, deploymentID, envID, requestedBy, requiredRole string) (domain.DeployApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.approvals[deploymentID]; exists {
		return domain.DeployApproval{}, store.ErrConflict
	}
	ap := domain.DeployApproval{
		DeploymentID:  deploymentID,
		EnvironmentID: envID,
		RequestedBy:   requestedBy,
		RequiredRole:  requiredRole,
		State:         domain.ApprovalPending,
	}
	if u, ok := f.users[requestedBy]; ok {
		ap.RequestedByEmail = u.Email
	}
	f.approvals[deploymentID] = ap
	return ap, nil
}

func (f *fakeStore) GetDeployApproval(_ context.Context, deploymentID string) (domain.DeployApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ap, ok := f.approvals[deploymentID]
	if !ok {
		return domain.DeployApproval{}, store.ErrNotFound
	}
	return ap, nil
}

// ListDeployApprovalsByEnvironment honours the bound the SQL applies, so a test
// that asks for more than the service asks for cannot pass here and fail there.
func (f *fakeStore) ListDeployApprovalsByEnvironment(_ context.Context, envID, state string, limit int32) ([]domain.DeployApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.DeployApproval{}
	for _, ap := range f.approvals {
		if ap.EnvironmentID == envID && (state == "" || ap.State == state) {
			out = append(out, ap)
		}
	}
	if limit >= 0 && len(out) > int(limit) {
		out = out[:limit]
	}
	return out, nil
}

// ListDeployApprovalsByApplication answers for the NAMED deployments only —
// a fake that ignored the id list would hide the very bound it exists to model.
func (f *fakeStore) ListDeployApprovalsByApplication(_ context.Context, appID string, deploymentIDs []string) (map[string]domain.DeployApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := make(map[string]bool, len(deploymentIDs))
	for _, id := range deploymentIDs {
		want[id] = true
	}
	out := map[string]domain.DeployApproval{}
	for id, ap := range f.approvals {
		if !want[id] {
			continue
		}
		if d, ok := f.deployments[id]; ok && d.ApplicationID == appID {
			out[id] = ap
		}
	}
	return out, nil
}

// DecideDeployApproval matches only a pending row, exactly as the SQL does.
func (f *fakeStore) DecideDeployApproval(_ context.Context, deploymentID, state, decidedBy, reason string) (domain.DeployApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ap, ok := f.approvals[deploymentID]
	if !ok {
		return domain.DeployApproval{}, store.ErrNotFound
	}
	if ap.State != domain.ApprovalPending {
		return domain.DeployApproval{}, store.ErrConflict
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	ap.State, ap.DecidedBy, ap.DecidedAt, ap.Reason = state, decidedBy, &now, reason
	if u, ok := f.users[decidedBy]; ok {
		ap.DecidedByEmail = u.Email
	}
	f.approvals[deploymentID] = ap
	return ap, nil
}

func (f *fakeStore) CountQualifiedApprovers(_ context.Context, projectID, minRole, excludeUserID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.approvers[projectID+"/"+minRole]
	if f.qualifies[excludeUserID+"/"+minRole] && n > 0 {
		n--
	}
	return n, nil
}

func (f *fakeStore) CreateBreakGlassGrant(_ context.Context, g domain.BreakGlassGrant) (domain.BreakGlassGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants[g.EnvironmentID] = append([]domain.BreakGlassGrant{g}, f.grants[g.EnvironmentID]...)
	return g, nil
}

func (f *fakeStore) BreakGlassOpen(_ context.Context, envID string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.grants[envID] {
		if g.Active(now) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) ListBreakGlassGrants(_ context.Context, envID string, limit int32) ([]domain.BreakGlassGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.grants[envID]
	if int32(len(out)) > limit {
		out = out[:limit]
	}
	return append([]domain.BreakGlassGrant{}, out...), nil
}

// fakePipeline records what the gate asked the scheduler to do.
type fakePipeline struct {
	approved []string
	rejected []string
	details  []string
	err      error
}

func (f *fakePipeline) ApproveDeployment(_ context.Context, id string) (domain.Deployment, error) {
	f.approved = append(f.approved, id)
	if f.err != nil {
		return domain.Deployment{ID: id}, f.err
	}
	return domain.Deployment{ID: id, Status: domain.DeployQueued}, nil
}

func (f *fakePipeline) RejectDeployment(_ context.Context, id, detail string) (domain.Deployment, error) {
	f.rejected = append(f.rejected, id)
	f.details = append(f.details, detail)
	if f.err != nil {
		return domain.Deployment{ID: id}, f.err
	}
	return domain.Deployment{ID: id, Status: domain.DeployFailed, Detail: detail}, nil
}

// fakeAnnouncer captures the inbox notices without writing anything.
type fakeAnnouncer struct {
	awaiting []inbox.DeployNotice
	approved []inbox.DeployNotice
	rejected []inbox.DeployNotice
	err      error
}

func (f *fakeAnnouncer) RecordDeployAwaitingApproval(_ context.Context, n inbox.DeployNotice) error {
	f.awaiting = append(f.awaiting, n)
	return f.err
}

func (f *fakeAnnouncer) RecordDeployApproved(_ context.Context, n inbox.DeployNotice) error {
	f.approved = append(f.approved, n)
	return f.err
}

func (f *fakeAnnouncer) RecordDeployRejected(_ context.Context, n inbox.DeployNotice) error {
	f.rejected = append(f.rejected, n)
	return f.err
}
