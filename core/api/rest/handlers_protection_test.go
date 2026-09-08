package rest

// HTTP + authorization tests for the deploy-protection routes
// (deploy-protection.md §5, §6) and for the behaviour changes on the three
// existing deploy routes (§6). Between them these cover the acceptance list's
// route-shaped items: 1, 3, 4, 6, 7, 8, 9 and 10.
//
// The authorization table under test is: read = team member, PUT = team admin,
// decide = the rank the approval snapshotted, break glass = team owner — and
// approve, reject and break glass are additionally sessionOnly, so no API token
// reaches them however broad its abilities.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/applications"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/projects"
	"github.com/MaramHarsha/cypherpanel/core/protection"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/templates"
)

// fakeProtection implements ProtectionService with just enough state to
// exercise routing, authorization and DTO shape. It is deliberately not a
// second implementation of the policy: the rules live in core/protection's own
// tests, and what a fake proves here is that the HTTP layer asks the right
// questions and maps the answers to the right status codes.
type fakeProtection struct {
	doc       map[string]domain.EnvironmentProtection
	approvals map[string]domain.DeployApproval
	grants    map[string][]domain.BreakGlassGrant
	now       time.Time

	// selfApprovalRefused makes Approve answer with the sole-approver rule.
	selfApprovalRefused bool
	// decided makes a second decision conflict.
	decided map[string]bool
	// setErr is returned from Set, for the validation and preview paths.
	setErr error
	// approvedBy / rejectedBy record who decided what.
	approvedBy map[string]string
	rejectedBy map[string]string
	reasons    map[string]string
}

func newFakeProtection() *fakeProtection {
	return &fakeProtection{
		doc: map[string]domain.EnvironmentProtection{},
		approvals: map[string]domain.DeployApproval{
			// dep_test is the harness deployment; it is parked and needs an
			// owner, and it was requested by the harness user — which is what
			// makes the self-approval case reachable.
			"dep_test": {
				DeploymentID: "dep_test", EnvironmentID: "env_test",
				RequestedBy: "usr_test", RequestedByEmail: testEmail,
				RequiredRole: domain.RoleOwner, State: domain.ApprovalPending,
			},
		},
		grants:     map[string][]domain.BreakGlassGrant{},
		now:        time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
		decided:    map[string]bool{},
		approvedBy: map[string]string{},
		rejectedBy: map[string]string{},
		reasons:    map[string]string{},
	}
}

func (f *fakeProtection) Get(_ context.Context, envID string) (domain.EnvironmentProtection, error) {
	if envID != "env_test" {
		return domain.EnvironmentProtection{}, store.ErrNotFound
	}
	if p, ok := f.doc[envID]; ok {
		return p, nil
	}
	return domain.DefaultProtection(envID), nil
}

func (f *fakeProtection) Set(_ context.Context, envID string, in protection.Document) (domain.EnvironmentProtection, error) {
	if f.setErr != nil {
		return domain.EnvironmentProtection{}, f.setErr
	}
	if envID != "env_test" {
		return domain.EnvironmentProtection{}, store.ErrNotFound
	}
	p := domain.EnvironmentProtection{
		EnvironmentID:   envID,
		RequireApproval: in.RequireApproval,
		MinApproverRole: in.MinApproverRole,
		FreezeEnabled:   in.FreezeEnabled,
		Windows:         []domain.FreezeWindow{},
		CreatedAt:       f.now,
		UpdatedAt:       f.now,
	}
	for i, w := range in.Windows {
		p.Windows = append(p.Windows, domain.FreezeWindow{
			ID: "fzw_" + itoa(i), EnvironmentID: envID,
			StartDOW: w.StartDOW, StartMinute: w.StartMinute,
			EndDOW: w.EndDOW, EndMinute: w.EndMinute, Timezone: w.Timezone,
		})
	}
	f.doc[envID] = p
	return p, nil
}

func (f *fakeProtection) Approvals(_ context.Context, envID, state string) ([]domain.DeployApproval, error) {
	if envID != "env_test" {
		return nil, store.ErrNotFound
	}
	out := []domain.DeployApproval{}
	for _, ap := range f.approvals {
		if state == "" || ap.State == state {
			out = append(out, ap)
		}
	}
	return out, nil
}

func (f *fakeProtection) ApprovalFor(_ context.Context, deploymentID string) (domain.DeployApproval, error) {
	ap, ok := f.approvals[deploymentID]
	if !ok {
		return domain.DeployApproval{}, store.ErrNotFound
	}
	return ap, nil
}

// ApprovalsForApplication answers for the ids it is handed and no others: the
// handler decorates one page, and a fake that returned everything would hide a
// caller that forgot to pass the page.
func (f *fakeProtection) ApprovalsForApplication(_ context.Context, _ string, deploymentIDs []string) (map[string]domain.DeployApproval, error) {
	out := make(map[string]domain.DeployApproval, len(deploymentIDs))
	for _, id := range deploymentIDs {
		if ap, ok := f.approvals[id]; ok {
			out[id] = ap
		}
	}
	return out, nil
}

func (f *fakeProtection) Approve(_ context.Context, deploymentID string, actor domain.User) (domain.Deployment, domain.DeployApproval, error) {
	ap, ok := f.approvals[deploymentID]
	if !ok {
		return domain.Deployment{}, domain.DeployApproval{}, store.ErrNotFound
	}
	if f.decided[deploymentID] {
		return domain.Deployment{}, domain.DeployApproval{}, protection.ErrAlreadyDecided
	}
	if f.selfApprovalRefused && ap.RequestedBy == actor.ID {
		return domain.Deployment{}, domain.DeployApproval{}, protection.ErrSelfApproval
	}
	f.decided[deploymentID] = true
	f.approvedBy[deploymentID] = actor.ID
	ap.State, ap.DecidedBy, ap.DecidedByEmail = domain.ApprovalApproved, actor.ID, actor.Email
	f.approvals[deploymentID] = ap
	return domain.Deployment{
		ID: deploymentID, ApplicationID: "app_x", RevisionID: "rev_test",
		Status: domain.DeployQueued, Trigger: "manual", CreatedAt: f.now,
	}, ap, nil
}

func (f *fakeProtection) Reject(_ context.Context, deploymentID, reason string, actor domain.User) (domain.Deployment, domain.DeployApproval, error) {
	ap, ok := f.approvals[deploymentID]
	if !ok {
		return domain.Deployment{}, domain.DeployApproval{}, store.ErrNotFound
	}
	if f.decided[deploymentID] {
		return domain.Deployment{}, domain.DeployApproval{}, protection.ErrAlreadyDecided
	}
	if strings.TrimSpace(reason) == "" {
		return domain.Deployment{}, domain.DeployApproval{}, &protection.ValidationError{Msg: "reason is required"}
	}
	f.decided[deploymentID] = true
	f.rejectedBy[deploymentID] = actor.ID
	f.reasons[deploymentID] = reason
	ap.State, ap.DecidedBy, ap.DecidedByEmail, ap.Reason =
		domain.ApprovalRejected, actor.ID, actor.Email, reason
	f.approvals[deploymentID] = ap
	return domain.Deployment{
		ID: deploymentID, ApplicationID: "app_x", RevisionID: "rev_test",
		Status: domain.DeployFailed, Trigger: "manual", CreatedAt: f.now,
		Detail: protection.RejectionDetail(actor.Email, reason),
	}, ap, nil
}

func (f *fakeProtection) OpenBreakGlass(_ context.Context, envID string, actor domain.User, reason string) (domain.BreakGlassGrant, error) {
	if envID != "env_test" {
		return domain.BreakGlassGrant{}, store.ErrNotFound
	}
	if strings.TrimSpace(reason) == "" {
		return domain.BreakGlassGrant{}, &protection.ValidationError{Msg: "reason is required"}
	}
	g := domain.BreakGlassGrant{
		ID: "bg_1", EnvironmentID: envID, OpenedBy: actor.ID, OpenedByEmail: actor.Email,
		Reason: reason, CreatedAt: f.now, ExpiresAt: f.now.Add(domain.BreakGlassTTL),
	}
	f.grants[envID] = append([]domain.BreakGlassGrant{g}, f.grants[envID]...)
	return g, nil
}

func (f *fakeProtection) BreakGlassGrants(_ context.Context, envID string) ([]domain.BreakGlassGrant, error) {
	if envID != "env_test" {
		return nil, store.ErrNotFound
	}
	return f.grants[envID], nil
}

func (f *fakeProtection) Now() time.Time { return f.now }

// ─── harness ────────────────────────────────────────────────────────────────

// newProtectionServer builds an API whose caller holds panelRole on the panel
// and projectRole in prj_test's team (empty = not a member at all).
func newProtectionServer(t *testing.T, panelRole, projectRole string) (*httptest.Server, *fakeProtection, *fakeDeployer) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: panelRole},
		sessions: map[string]domain.User{},
		tokens:   map[string]domain.APIToken{},
		byHash:   map[string]string{},
	}
	box := testBox(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ft := newFakeTeams()
	if projectRole != "" {
		ft.projectRoles["usr_test"] = map[string]string{"prj_test": projectRole}
	}
	ft.teams = nil

	prot := newFakeProtection()
	deployer := &fakeDeployer{}
	api := New(Deps{
		Auth:         auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: applications.NewService(newFakeAppsStore(), box),
		Protection:   prot,
		Scheduler:    deployer,
		Deployments:  fakeDeploymentReader{},
		Teams:        ft,
		Opener:       box,
		Pinger:       okPinger{},
		CACertPEM:    []byte("x"),
		Log:          log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, prot, deployer
}

// protectionRoutes is every route this feature adds, with a body where one is
// needed.
var protectionRoutes = []struct {
	name   string
	method string
	path   string
	body   string
}{
	{"read protection", "GET", "/api/v1/environments/env_test/protection", ""},
	{"set protection", "PUT", "/api/v1/environments/env_test/protection", `{"require_approval":true,"min_approver_role":"owner","freeze_enabled":false,"windows":[]}`},
	{"list approvals", "GET", "/api/v1/environments/env_test/approvals", ""},
	{"list grants", "GET", "/api/v1/environments/env_test/break-glass", ""},
	{"open break glass", "POST", "/api/v1/environments/env_test/break-glass", `{"reason":"checkout returning 500s"}`},
	{"read approval", "GET", "/api/v1/deployments/dep_test/approval", ""},
	{"approve", "POST", "/api/v1/deployments/dep_test/approve", ""},
	{"reject", "POST", "/api/v1/deployments/dep_test/reject", `{"reason":"shipping Monday"}`},
}

// A project you cannot see does not exist: every route answers 404, never 403,
// so membership is not probeable.
func TestNonMemberGets404OnEveryProtectionRoute(t *testing.T) {
	ts, _, _ := newProtectionServer(t, domain.RoleMember, "")
	token := login(t, ts)
	for _, r := range protectionRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
		if status != http.StatusNotFound {
			t.Errorf("%s as a non-member = %d, want 404 (body %s)", r.name, status, body)
		}
	}
}

// Every route needs authentication.
func TestProtectionRoutesRequireAuth(t *testing.T) {
	ts, _, _ := newProtectionServer(t, domain.RoleOwner, domain.RoleOwner)
	for _, r := range protectionRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, "", r.body)
		if status != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated = %d, want 401 (body %s)", r.name, status, body)
		}
	}
}

// The rank table in §5: a member reads but cannot write the policy, decide, or
// break glass.
func TestProtectionRankTable(t *testing.T) {
	for _, tc := range []struct {
		role  string
		want  map[string]int // route name → expected status class marker
		label string
	}{
		{
			role:  domain.RoleMember,
			label: "team member",
			want: map[string]int{
				"read protection": http.StatusOK,
				"list approvals":  http.StatusOK,
				"list grants":     http.StatusOK,
				"read approval":   http.StatusOK,

				"set protection":   http.StatusForbidden,
				"open break glass": http.StatusForbidden,
				"approve":          http.StatusForbidden,
				"reject":           http.StatusForbidden,
			},
		},
		{
			role:  domain.RoleAdmin,
			label: "team admin",
			want: map[string]int{
				"read protection": http.StatusOK,
				"set protection":  http.StatusOK,
				// Acceptance 9: an admin cannot break glass, only an owner can.
				"open break glass": http.StatusForbidden,
				// The approval snapshotted `owner`, so an admin is under-ranked.
				"approve": http.StatusForbidden,
				"reject":  http.StatusForbidden,
			},
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			ts, _, _ := newProtectionServer(t, domain.RoleMember, tc.role)
			token := login(t, ts)
			for _, r := range protectionRoutes {
				want, ok := tc.want[r.name]
				if !ok {
					continue
				}
				status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
				if status != want {
					t.Errorf("%s as %s = %d, want %d (body %s)", r.name, tc.label, status, want, body)
				}
			}
		})
	}
}

// Acceptance 10: an environment that has never been protected answers with the
// default document, not a 404.
func TestGetProtectionReturnsTheDefaultDocument(t *testing.T) {
	ts, _, _ := newProtectionServer(t, domain.RoleMember, domain.RoleMember)
	token := login(t, ts)
	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/protection", token, "")
	if status != http.StatusOK {
		t.Fatalf("GET protection = %d (body %s)", status, body)
	}
	var doc struct {
		EnvironmentID   string `json:"environment_id"`
		RequireApproval bool   `json:"require_approval"`
		MinApproverRole string `json:"min_approver_role"`
		FreezeEnabled   bool   `json:"freeze_enabled"`
		Windows         []any  `json:"windows"`
		CreatedAt       *any   `json:"created_at"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if doc.EnvironmentID != "env_test" || doc.RequireApproval || doc.FreezeEnabled ||
		doc.MinApproverRole != domain.RoleOwner {
		t.Fatalf("default document = %+v", doc)
	}
	if doc.Windows == nil {
		t.Fatal("windows came back null; an empty list must serialize as []")
	}
	if doc.CreatedAt != nil {
		t.Fatal("the default document claimed a created_at; no row exists")
	}
}

// The PUT carries the WHOLE document. An omitted flag is refused rather than
// read as false, because that would turn a forgotten field into "approval is
// now off" and answer 200.
func TestSetProtectionRequiresTheWholeDocument(t *testing.T) {
	ts, _, _ := newProtectionServer(t, domain.RoleMember, domain.RoleAdmin)
	token := login(t, ts)
	for _, tc := range []struct{ name, body string }{
		{"no require_approval", `{"min_approver_role":"owner","freeze_enabled":false,"windows":[]}`},
		// Omitting the role is the same silent rewrite in the tightening
		// direction: a document that read `member` would come back `owner`.
		{"no min_approver_role", `{"require_approval":true,"freeze_enabled":false,"windows":[]}`},
		{"no freeze_enabled", `{"require_approval":true,"min_approver_role":"owner","windows":[]}`},
		{"no windows", `{"require_approval":true,"min_approver_role":"owner","freeze_enabled":false}`},
		{"not json", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/environments/env_test/protection", token, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("PUT %s = %d, want 400 (body %s)", tc.name, status, body)
			}
		})
	}

	// A complete document round-trips, and the window comes back with its
	// rendered sentence.
	status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/environments/env_test/protection", token,
		`{"require_approval":true,"min_approver_role":"owner","freeze_enabled":true,
		  "windows":[{"start_dow":5,"start_minute":1080,"end_dow":1,"end_minute":480,"timezone":"Europe/Berlin"}]}`)
	if status != http.StatusOK {
		t.Fatalf("PUT = %d (body %s)", status, body)
	}
	if !strings.Contains(string(body), "Fri 18:00 → Mon 08:00 (Europe/Berlin)") {
		t.Fatalf("window summary missing from the response: %s", body)
	}
}

// A validation failure from the service is a 400, and the preview refusal is a
// 409 — different problems, different codes.
func TestSetProtectionMapsServiceErrors(t *testing.T) {
	ts, prot, _ := newProtectionServer(t, domain.RoleMember, domain.RoleAdmin)
	token := login(t, ts)
	full := `{"require_approval":true,"min_approver_role":"owner","freeze_enabled":false,"windows":[]}`

	prot.setErr = &protection.ValidationError{Msg: "\"Mars/Olympus\" is not an IANA time zone name"}
	if status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/environments/env_test/protection", token, full); status != http.StatusBadRequest {
		t.Fatalf("invalid document = %d, want 400 (body %s)", status, body)
	}

	prot.setErr = protection.ErrPreviewProtection
	status, _, body := doJSON(t, "PUT", ts.URL+"/api/v1/environments/env_test/protection", token, full)
	if status != http.StatusConflict {
		t.Fatalf("preview environment = %d, want 409 (body %s)", status, body)
	}
	if !strings.Contains(string(body), "preview") {
		t.Fatalf("the 409 does not say why: %s", body)
	}
}

// Acceptance 1 (the route half): a parked deploy comes back 202 with
// status=awaiting_approval and its approval summary, and the environment's
// approval queue lists it as pending.
func TestDeployParksAndIsListedPending(t *testing.T) {
	ts, _, deployer := newProtectionServer(t, domain.RoleMember, domain.RoleMember)
	deployer.parked = true
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/applications/app_x/deploy", token, "")
	if status != http.StatusAccepted {
		t.Fatalf("deploy = %d (body %s)", status, body)
	}
	var dep struct {
		Status   string `json:"status"`
		Approval *struct {
			State            string `json:"state"`
			RequiredRole     string `json:"required_role"`
			RequestedBy      string `json:"requested_by"`
			RequestedByEmail string `json:"requested_by_email"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(body, &dep); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if dep.Status != string(domain.DeployAwaitingApproval) {
		t.Fatalf("status = %q, want awaiting_approval (body %s)", dep.Status, body)
	}
	if dep.Approval == nil {
		t.Fatalf("the parked deployment carries no approval summary: %s", body)
	}
	if dep.Approval.State != domain.ApprovalPending || dep.Approval.RequiredRole != domain.RoleOwner ||
		dep.Approval.RequestedByEmail != testEmail {
		t.Fatalf("approval summary = %+v", dep.Approval)
	}
	// The handler attributed the deploy to the caller.
	if len(deployer.requesters) != 1 || deployer.requesters[0] != "usr_test" {
		t.Fatalf("requesters = %v, want [usr_test]", deployer.requesters)
	}

	// And it is in the environment's pending queue.
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/approvals", token, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"state":"pending"`) {
		t.Fatalf("pending queue = %d %s", status, body)
	}
}

// Acceptance 8 (the route half): inside a freeze window the three deploy routes
// answer 409 with a body naming the window and when it lifts.
func TestFrozenDeployRoutesAnswer409(t *testing.T) {
	const detail = "production is frozen until Mon 08:00 Europe/Berlin"
	ts, _, deployer := newProtectionServer(t, domain.RoleMember, domain.RoleMember)
	deployer.frozen = detail
	token := login(t, ts)

	for _, r := range []struct{ name, method, path, body string }{
		{"deploy", "POST", "/api/v1/applications/app_x/deploy", ""},
		{"rollback", "POST", "/api/v1/deployments/dep_test/rollback", ""},
	} {
		t.Run(r.name, func(t *testing.T) {
			status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
			if status != http.StatusConflict {
				t.Fatalf("%s while frozen = %d, want 409 (body %s)", r.name, status, body)
			}
			if !strings.Contains(string(body), detail) {
				t.Fatalf("the 409 does not name the window: %s", body)
			}
		})
	}
}

// A template install deploys, so it passes the same gate: inside a freeze it is
// refused with the window named, rather than failing as a blank 500.
func TestTemplateInstallIsFrozenToo(t *testing.T) {
	const detail = "production is frozen until Mon 08:00 Europe/Berlin"
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	box := testBox(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deployer := &fakeDeployer{frozen: detail}
	appSvc := applications.NewService(newFakeAppsStore(), box)
	dbSvc := databases.NewService(newFakeDatabasesStore(), box, &fakeDbReconciler{})
	templateSvc, err := templates.New(appSvc, dbSvc, deployer, log)
	if err != nil {
		t.Fatalf("templates.New: %v", err)
	}
	api := New(Deps{
		Auth: auth.NewAuthenticator(&fakeAuthStore{
			user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleOwner},
			sessions: map[string]domain.User{},
		}, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: appSvc,
		Databases:    dbSvc,
		Templates:    templateSvc,
		Scheduler:    deployer,
		Deployments:  fakeDeploymentReader{},
		Teams:        newFakeTeams(),
		Opener:       box,
		Pinger:       okPinger{},
		CACertPEM:    []byte("x"),
		Log:          log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/templates/n8n/install", token,
		`{"environment_id":"env_test","server_id":"srv_test","domain":"n8n.example.com"}`)
	if status != http.StatusConflict {
		t.Fatalf("frozen template install = %d, want 409 (body %s)", status, body)
	}
	if !strings.Contains(string(body), detail) {
		t.Fatalf("the 409 does not name the window: %s", body)
	}
}

// Acceptance 4 and 8: a signed webhook push into a protected environment parks;
// into a frozen one it is refused with the window named, so a failed GitHub
// delivery is diagnosable from the response alone.
func TestWebhookDeployParksAndFreezes(t *testing.T) {
	t.Run("parks", func(t *testing.T) {
		ts, _, deployer := newProtectionServer(t, domain.RoleOwner, domain.RoleOwner)
		deployer.parked = true
		status, body := postWebhookPush(t, ts)
		if status != http.StatusAccepted {
			t.Fatalf("webhook push = %d (body %s)", status, body)
		}
		if !strings.Contains(body, `"status":"awaiting_approval"`) {
			t.Fatalf("webhook deploy did not park: %s", body)
		}
		// A push is not a person: the deploy carries no requester.
		if len(deployer.requesters) != 1 || deployer.requesters[0] != "" {
			t.Fatalf("webhook requesters = %v, want one empty entry", deployer.requesters)
		}
	})

	t.Run("frozen", func(t *testing.T) {
		const detail = "production is frozen until Mon 08:00 Europe/Berlin"
		ts, _, deployer := newProtectionServer(t, domain.RoleOwner, domain.RoleOwner)
		deployer.frozen = detail
		status, body := postWebhookPush(t, ts)
		if status != http.StatusConflict || !strings.Contains(body, detail) {
			t.Fatalf("frozen webhook push = %d %s, want 409 naming the window", status, body)
		}
	})
}

// Acceptance 6: the requester cannot approve their own deploy while another
// qualifying approver exists.
func TestRequesterCannotApproveTheirOwnDeploy(t *testing.T) {
	ts, prot, _ := newProtectionServer(t, domain.RoleMember, domain.RoleOwner)
	token := login(t, ts)

	prot.selfApprovalRefused = true
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_test/approve", token, "")
	if status != http.StatusForbidden {
		t.Fatalf("self-approve = %d, want 403 (body %s)", status, body)
	}
	if !strings.Contains(string(body), "someone else") {
		t.Fatalf("the 403 does not explain itself: %s", body)
	}

	// As the sole qualifying approver the identical call succeeds.
	prot.selfApprovalRefused = false
	status, _, body = doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_test/approve", token, "")
	if status != http.StatusAccepted {
		t.Fatalf("sole-approver approve = %d, want 202 (body %s)", status, body)
	}
	if prot.approvedBy["dep_test"] != "usr_test" {
		t.Fatalf("approver recorded = %q", prot.approvedBy["dep_test"])
	}
	// Decisions are once-only.
	if status, _, body = doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_test/approve", token, ""); status != http.StatusConflict {
		t.Fatalf("second approve = %d, want 409 (body %s)", status, body)
	}
}

// Acceptance 3 (the route half): rejecting needs a reason, ends the deployment
// failed with a detail naming the rejecter, and records the decision.
func TestRejectRecordsTheReason(t *testing.T) {
	ts, prot, _ := newProtectionServer(t, domain.RoleMember, domain.RoleOwner)
	token := login(t, ts)

	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_test/reject", token, `{"reason":"  "}`); status != http.StatusBadRequest {
		t.Fatalf("reject with no reason = %d, want 400 (body %s)", status, body)
	}
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/deployments/dep_test/reject", token, `{"reason":"shipping Monday"}`)
	if status != http.StatusOK {
		t.Fatalf("reject = %d (body %s)", status, body)
	}
	if !strings.Contains(string(body), `"status":"failed"`) ||
		!strings.Contains(string(body), "rejected by "+testEmail+": shipping Monday") {
		t.Fatalf("rejected deployment does not name the rejecter: %s", body)
	}
	if !strings.Contains(string(body), `"state":"rejected"`) {
		t.Fatalf("the response carries no decided approval: %s", body)
	}
	if prot.reasons["dep_test"] != "shipping Monday" {
		t.Fatalf("reason recorded = %q", prot.reasons["dep_test"])
	}
}

// Acceptance 7: a token with the `deploy` ability creates a parked deployment
// successfully, but is refused at approve, reject, break glass and the PUT that
// would switch the whole policy off — an interactive session is required,
// whatever the token's abilities.
func TestAPITokensCannotOpenOrRemoveTheGate(t *testing.T) {
	ts, _, deployer := newProtectionServer(t, domain.RoleOwner, domain.RoleOwner)
	deployer.parked = true
	session := login(t, ts)
	deployToken := createToken(t, ts, session, "ci", `["read","deploy"]`)

	// It can still deploy, and its deploy parks.
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/applications/app_x/deploy", deployToken, "")
	if status != http.StatusAccepted || !strings.Contains(string(body), `"status":"awaiting_approval"`) {
		t.Fatalf("token deploy = %d %s, want 202 awaiting_approval", status, body)
	}

	// A token holding EVERY ability is still refused, and the refusal says
	// why: the gate is about HOW you authenticated, not what the token may do.
	// (A narrower token is refused one layer earlier, by the ability check —
	// also 403, which is what the deploy-only case below asserts.)
	wide := createToken(t, ts, session, "wide", `["read","write","deploy"]`)
	for _, r := range []struct{ name, method, path, body string }{
		{"approve", "POST", "/api/v1/deployments/dep_test/approve", ""},
		{"reject", "POST", "/api/v1/deployments/dep_test/reject", `{"reason":"no"}`},
		{"break glass", "POST", "/api/v1/environments/env_test/break-glass", `{"reason":"incident"}`},
		// The PUT belongs in this list for a stronger reason than the other
		// three: it does not open the gate for one deploy, it deletes the gate.
		// A `write` token whose owner is a team admin could otherwise send
		// this exact body and then deploy freely for as long as nobody looked.
		{"set protection", "PUT", "/api/v1/environments/env_test/protection",
			`{"require_approval":false,"min_approver_role":"owner","freeze_enabled":false,"windows":[]}`},
	} {
		t.Run(r.name, func(t *testing.T) {
			status, _, body := doJSON(t, r.method, ts.URL+r.path, wide, r.body)
			if status != http.StatusForbidden {
				t.Fatalf("%s with a full-ability token = %d, want 403 (body %s)", r.name, status, body)
			}
			if !strings.Contains(string(body), "interactive session") {
				t.Fatalf("the refusal does not say why: %s", body)
			}
			// And the deploy-only token — the CI shape acceptance 7 names —
			// is refused too.
			if status, _, body := doJSON(t, r.method, ts.URL+r.path, deployToken, r.body); status != http.StatusForbidden {
				t.Fatalf("%s with a deploy token = %d, want 403 (body %s)", r.name, status, body)
			}
		})
	}
}

// Acceptance 9: an owner opens a grant with a reason; the listing shows who,
// why and when it expires, with the derived `active` flag.
func TestBreakGlassGrantIsRecordedAndListed(t *testing.T) {
	ts, _, _ := newProtectionServer(t, domain.RoleMember, domain.RoleOwner)
	token := login(t, ts)

	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/break-glass", token, `{"reason":""}`); status != http.StatusBadRequest {
		t.Fatalf("grant with no reason = %d, want 400 (body %s)", status, body)
	}

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/break-glass", token,
		`{"reason":"checkout returning 500s"}`)
	if status != http.StatusCreated {
		t.Fatalf("break glass = %d (body %s)", status, body)
	}
	var g struct {
		OpenedByEmail string    `json:"opened_by_email"`
		Reason        string    `json:"reason"`
		ExpiresAt     time.Time `json:"expires_at"`
		Active        bool      `json:"active"`
	}
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if g.OpenedByEmail != testEmail || g.Reason != "checkout returning 500s" || !g.Active {
		t.Fatalf("grant = %+v", g)
	}

	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/break-glass", token, "")
	if status != http.StatusOK || !strings.Contains(string(body), "checkout returning 500s") {
		t.Fatalf("grant listing = %d %s", status, body)
	}
}

// The approvals filter is a real filter, and an unknown state is a 400 rather
// than a silent "everything".
func TestApprovalsStateFilter(t *testing.T) {
	ts, prot, _ := newProtectionServer(t, domain.RoleMember, domain.RoleMember)
	token := login(t, ts)

	// Default is pending.
	if _, _, body := doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/approvals", token, ""); !strings.Contains(string(body), "dep_test") {
		t.Fatalf("default queue omitted the pending approval: %s", body)
	}
	// Decide it, and it leaves the pending queue but stays in `all`.
	ap := prot.approvals["dep_test"]
	ap.State = domain.ApprovalRejected
	prot.approvals["dep_test"] = ap

	if _, _, body := doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/approvals", token, ""); strings.Contains(string(body), "dep_test") {
		t.Fatalf("a decided approval is still in the pending queue: %s", body)
	}
	if _, _, body := doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/approvals?state=all", token, ""); !strings.Contains(string(body), "dep_test") {
		t.Fatalf("state=all omitted the decided approval: %s", body)
	}
}

// With the service absent (a plane built without it), reads degrade to the
// "nothing is protected" answer and writes say so — never a nil panic.
func TestProtectionRoutesWithoutTheService(t *testing.T) {
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	box := testBox(t)
	ft := newFakeTeams()
	ft.projectRoles["usr_test"] = map[string]string{"prj_test": domain.RoleOwner}
	ft.teams = nil
	api := New(Deps{
		Auth: auth.NewAuthenticator(&fakeAuthStore{
			user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleOwner},
			sessions: map[string]domain.User{},
		}, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:     projects.NewService(newFakeProjectsStore()),
		Applications: applications.NewService(newFakeAppsStore(), box),
		Scheduler:    &fakeDeployer{},
		Deployments:  fakeDeploymentReader{},
		Teams:        ft,
		Opener:       box,
		Pinger:       okPinger{},
		CACertPEM:    []byte("x"),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/protection", token, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"require_approval":false`) {
		t.Fatalf("read without the service = %d %s, want the default document", status, body)
	}
	for _, r := range []struct{ name, method, path, body, wantIn string }{
		{"approvals", "GET", "/api/v1/environments/env_test/approvals", "", "[]"},
		{"grants", "GET", "/api/v1/environments/env_test/break-glass", "", "[]"},
	} {
		if status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body); status != http.StatusOK ||
			strings.TrimSpace(string(body)) != r.wantIn {
			t.Errorf("%s without the service = %d %s, want 200 %s", r.name, status, body, r.wantIn)
		}
	}
	if status, _, _ := doJSON(t, "PUT", ts.URL+"/api/v1/environments/env_test/protection", token,
		`{"require_approval":true,"min_approver_role":"owner","freeze_enabled":false,"windows":[]}`); status != http.StatusNotImplemented {
		t.Errorf("set without the service = %d, want 501", status)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/deployments/dep_test/approval", token, ""); status != http.StatusNotFound {
		t.Errorf("approval without the service = %d, want 404", status)
	}
	// An ungated deployment carries no approval summary at all.
	if _, _, body := doJSON(t, "GET", ts.URL+"/api/v1/deployments/dep_test", token, ""); strings.Contains(string(body), "approval") {
		t.Errorf("an ungated deployment carries an approval key: %s", body)
	}
}

// postWebhookPush creates an application, then delivers a correctly signed
// push on its configured branch. It returns the status and the body, so a test
// can assert on what the plane answered a GitHub delivery.
func postWebhookPush(t *testing.T, ts *httptest.Server) (int, string) {
	t.Helper()
	token := login(t, ts)
	create := `{"name":"web-hook","source":{"kind":"github","repo":"acme/web"},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"hook.example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, create)
	if status != http.StatusCreated {
		t.Fatalf("create application: %d (body %s)", status, resp)
	}
	var created struct {
		Webhook struct {
			URL    string `json:"url"`
			Secret string `json:"secret"`
		} `json:"webhook"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("decode create: %v (%s)", err, resp)
	}
	hookPath := created.Webhook.URL[strings.Index(created.Webhook.URL, "/webhooks/"):]

	payload := `{"ref":"refs/heads/main","after":"c99d2e1","deleted":false}`
	mac := hmac.New(sha256.New, []byte(created.Webhook.Secret))
	mac.Write([]byte(payload))
	req, err := http.NewRequest("POST", ts.URL+hookPath, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook post: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading webhook body: %v", err)
	}
	return res.StatusCode, string(body)
}

// itoa keeps the fake free of an fmt import for one integer.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
