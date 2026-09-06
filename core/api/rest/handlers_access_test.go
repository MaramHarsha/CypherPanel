package rest

// HTTP + authorization tests for invitations and access requests
// (invitations-and-access-requests.md §3, §7, §9).
//
// Four postures are asserted here, because they are what a reviewer must be
// able to check without reading the service:
//
//   - a non-member gets 404 on every team-scoped route (no tenancy probing);
//   - the rank gates are real: admin for invitations, member to ask, owner to
//     decide;
//   - grant and deny are unreachable with an API token, however wide its
//     abilities;
//   - the two public routes need no credential, answer one undifferentiated
//     404, and never leak a token into a listing.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/access"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

const (
	testInviteToken = "inv_test.SECRET"
	testInviteID    = "inv_test"
)

// fakeInviteService implements InviteService with just enough state to exercise
// routing, authorization and DTO shape. It keeps the wire token so a test can
// assert it appears in exactly one response and no other.
type fakeInviteService struct {
	invites map[string]domain.TeamInvite
	// acceptErr, previewErr override the happy path for the error-mapping tests.
	acceptErr  error
	previewErr error
	created    bool
	revoked    []string
	lastAccept access.AcceptInput
	lastIPs    []string
}

func newFakeInviteService() *fakeInviteService {
	inviter := "usr_sam"
	return &fakeInviteService{
		invites: map[string]domain.TeamInvite{
			testInviteID: {
				ID: testInviteID, TeamID: "tm_test", Email: "priya@meridian.dev",
				Role: domain.RoleAdmin, TokenHash: []byte("hash"),
				InvitedBy: &inviter, InvitedByLabel: "sam@meridian.dev",
				ExpiresAt: time.Now().Add(72 * time.Hour),
				CreatedAt: time.Now().Add(-time.Hour),
			},
		},
	}
}

func (f *fakeInviteService) Create(_ context.Context, teamID string, in access.CreateInput, actor domain.User, actorRole string) (access.Created, error) {
	if teamID != "tm_test" {
		return access.Created{}, store.ErrNotFound
	}
	role := in.Role
	if role == "" {
		role = domain.RoleMember
	}
	if !domain.CanGrantRole(actorRole, role) {
		return access.Created{}, access.ErrForbidden
	}
	if in.Email == "" {
		return access.Created{}, &access.ValidationError{Msg: `"" is not a valid email address`}
	}
	inv := domain.TeamInvite{
		ID: "inv_new", TeamID: teamID, Email: in.Email, Role: role,
		InvitedByLabel: actor.Email, ExpiresAt: time.Now().Add(domain.InviteTTL),
		CreatedAt: time.Now(),
	}
	f.invites[inv.ID] = inv
	return access.Created{Invite: inv, AcceptURL: "https://panel.test/invite/inv_new.FRESHSECRET", MailSent: true}, nil
}

func (f *fakeInviteService) List(_ context.Context, teamID string, includeDecided bool) ([]domain.TeamInvite, error) {
	out := []domain.TeamInvite{}
	for _, inv := range f.invites {
		if inv.TeamID == teamID && (includeDecided || inv.Acceptable(time.Now())) {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (f *fakeInviteService) Revoke(_ context.Context, teamID, id string) (domain.TeamInvite, error) {
	inv, ok := f.invites[id]
	if !ok || inv.TeamID != teamID {
		return domain.TeamInvite{}, store.ErrNotFound
	}
	f.revoked = append(f.revoked, id)
	at := time.Now()
	inv.RevokedAt = &at
	return inv, nil
}

func (f *fakeInviteService) Preview(_ context.Context, token, clientIP string) (access.Preview, error) {
	f.lastIPs = append(f.lastIPs, clientIP)
	if f.previewErr != nil {
		return access.Preview{}, f.previewErr
	}
	if token != testInviteToken {
		return access.Preview{}, access.ErrInvalidInvite
	}
	inv := f.invites[testInviteID]
	return access.Preview{
		TeamName: "meridian studio", InviterLabel: inv.InvitedByLabel,
		Email: inv.Email, Role: inv.Role, ExpiresAt: inv.ExpiresAt,
	}, nil
}

func (f *fakeInviteService) Accept(_ context.Context, token string, in access.AcceptInput, clientIP string) (access.Accepted, error) {
	f.lastAccept = in
	f.lastIPs = append(f.lastIPs, clientIP)
	if f.acceptErr != nil {
		return access.Accepted{}, f.acceptErr
	}
	if token != testInviteToken {
		return access.Accepted{}, access.ErrInvalidInvite
	}
	return access.Accepted{
		Token:    "session-token",
		User:     domain.User{ID: "usr_priya", Email: "priya@meridian.dev", Role: domain.RoleMember},
		Invite:   f.invites[testInviteID],
		TeamName: "meridian studio",
		Created:  f.created,
	}, nil
}

// fakeAccessRequestService implements AccessRequestService.
type fakeAccessRequestService struct {
	requests map[string]domain.AccessRequest
	grants   []string
	denials  map[string]string
}

func newFakeAccessRequestService() *fakeAccessRequestService {
	return &fakeAccessRequestService{
		requests: map[string]domain.AccessRequest{
			"acr_test": {
				ID: "acr_test", TeamID: "tm_test", UserID: "usr_priya",
				UserEmail: "priya@meridian.dev", CurrentRole: domain.RoleMember,
				RequestedRole: domain.RoleAdmin, Message: "Need to deploy.",
				State: domain.AccessRequestPending, CreatedAt: time.Now(),
			},
		},
		denials: map[string]string{},
	}
}

func (f *fakeAccessRequestService) Create(_ context.Context, teamID string, actor domain.User, actorRole string, in access.RequestInput) (domain.AccessRequest, error) {
	if teamID != "tm_test" {
		return domain.AccessRequest{}, store.ErrNotFound
	}
	if !domain.ValidRole(in.RequestedRole) || domain.RoleRank(in.RequestedRole) <= domain.RoleRank(actorRole) {
		return domain.AccessRequest{}, &access.ValidationError{Msg: "you already hold that role or higher on this team"}
	}
	r := domain.AccessRequest{
		ID: "acr_new", TeamID: teamID, UserID: actor.ID, UserEmail: actor.Email,
		CurrentRole: actorRole, RequestedRole: in.RequestedRole, Message: in.Message,
		State: domain.AccessRequestPending, CreatedAt: time.Now(),
	}
	f.requests[r.ID] = r
	return r, nil
}

func (f *fakeAccessRequestService) List(_ context.Context, teamID string, includeDecided bool) ([]domain.AccessRequest, error) {
	out := []domain.AccessRequest{}
	for _, r := range f.requests {
		if r.TeamID == teamID && (includeDecided || r.State == domain.AccessRequestPending) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeAccessRequestService) Get(_ context.Context, id string) (domain.AccessRequest, error) {
	r, ok := f.requests[id]
	if !ok {
		return domain.AccessRequest{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeAccessRequestService) Grant(_ context.Context, id string, actor domain.User, actorRole string) (domain.AccessRequest, error) {
	r, ok := f.requests[id]
	if !ok {
		return domain.AccessRequest{}, store.ErrNotFound
	}
	if r.State != domain.AccessRequestPending {
		return domain.AccessRequest{}, access.ErrDecided
	}
	f.grants = append(f.grants, id)
	r.State, r.DecidedByLabel = domain.AccessRequestGranted, actor.Email
	at := time.Now()
	r.DecidedAt = &at
	f.requests[id] = r
	return r, nil
}

func (f *fakeAccessRequestService) Deny(_ context.Context, id, reason string, actor domain.User) (domain.AccessRequest, error) {
	r, ok := f.requests[id]
	if !ok {
		return domain.AccessRequest{}, store.ErrNotFound
	}
	if r.State != domain.AccessRequestPending {
		return domain.AccessRequest{}, access.ErrDecided
	}
	f.denials[id] = reason
	r.State, r.DecidedByLabel, r.DecisionReason = domain.AccessRequestDenied, actor.Email, reason
	at := time.Now()
	r.DecidedAt = &at
	f.requests[id] = r
	return r, nil
}

// newAccessServer wires a server whose user holds panelRole on the panel and
// teamRole in tm_test (empty = not a member at all).
func newAccessServer(t *testing.T, panelRole, teamRole string) (*httptest.Server, *fakeInviteService, *fakeAccessRequestService) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: panelRole},
		sessions: map[string]domain.User{},
	}
	ft := newFakeTeams()
	if teamRole != "" {
		ft.teamRoles["usr_test"] = map[string]string{"tm_test": teamRole}
	}
	ft.teams = nil

	invites := newFakeInviteService()
	requests := newFakeAccessRequestService()
	api := New(Deps{
		Auth:           auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Teams:          ft,
		Invites:        invites,
		AccessRequests: requests,
		Pinger:         okPinger{},
		CACertPEM:      []byte("x"),
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, invites, requests
}

// teamScopedAccessRoutes is every route that hangs off a team, with a body
// where one is needed.
var teamScopedAccessRoutes = []struct {
	name, method, path, body, minRole string
}{
	{"create invite", "POST", "/api/v1/teams/tm_test/invites", `{"email":"new@example.test"}`, domain.RoleAdmin},
	{"list invites", "GET", "/api/v1/teams/tm_test/invites", "", domain.RoleAdmin},
	{"revoke invite", "DELETE", "/api/v1/teams/tm_test/invites/" + testInviteID, "", domain.RoleAdmin},
	{"list access requests", "GET", "/api/v1/teams/tm_test/access-requests", "", domain.RoleAdmin},
	{"request access", "POST", "/api/v1/teams/tm_test/access-requests", `{"requested_role":"owner"}`, domain.RoleMember},
}

// A team you cannot see does not exist: every team-scoped route answers 404,
// never 403, so membership is not probeable.
func TestNonMemberGets404OnEveryAccessRoute(t *testing.T) {
	ts, _, _ := newAccessServer(t, domain.RoleMember, "")
	token := login(t, ts)
	for _, r := range teamScopedAccessRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
		if status != http.StatusNotFound {
			t.Errorf("%s as a non-member = %d, want 404 (body %s)", r.name, status, body)
		}
	}
	// The two decision routes resolve their team from the request, so they
	// answer the same way.
	for _, path := range []string{"/api/v1/access-requests/acr_test/grant", "/api/v1/access-requests/acr_test/deny"} {
		if status, _, body := doJSON(t, "POST", ts.URL+path, token, ""); status != http.StatusNotFound {
			t.Errorf("%s as a non-member = %d, want 404 (body %s)", path, status, body)
		}
	}
}

// The rank gates: a member may ask for access and nothing else; an admin runs
// invitations; only an owner decides.
func TestAccessRoutesEnforceTheirRanks(t *testing.T) {
	for _, tc := range []struct {
		role      string
		forbidden []string
	}{
		{domain.RoleMember, []string{"create invite", "list invites", "revoke invite", "list access requests"}},
		{domain.RoleAdmin, nil},
	} {
		t.Run(tc.role, func(t *testing.T) {
			ts, _, _ := newAccessServer(t, domain.RoleMember, tc.role)
			token := login(t, ts)
			refused := map[string]bool{}
			for _, name := range tc.forbidden {
				refused[name] = true
			}
			for _, r := range teamScopedAccessRoutes {
				status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
				if refused[r.name] {
					if status != http.StatusForbidden {
						t.Errorf("%s as a %s = %d, want 403 (body %s)", r.name, tc.role, status, body)
					}
					continue
				}
				if status == http.StatusForbidden || status == http.StatusNotFound {
					t.Errorf("%s as a %s = %d, want it authorized (body %s)", r.name, tc.role, status, body)
				}
			}
		})
	}
}

// Deciding needs OWNER: an admin who can invite still cannot promote.
func TestOnlyAnOwnerDecidesAnAccessRequest(t *testing.T) {
	ts, _, requests := newAccessServer(t, domain.RoleMember, domain.RoleAdmin)
	token := login(t, ts)
	for _, path := range []string{"/api/v1/access-requests/acr_test/grant", "/api/v1/access-requests/acr_test/deny"} {
		if status, _, body := doJSON(t, "POST", ts.URL+path, token, ""); status != http.StatusForbidden {
			t.Errorf("%s as a team admin = %d, want 403 (body %s)", path, status, body)
		}
	}
	if len(requests.grants) != 0 {
		t.Errorf("a refused grant went through anyway: %v", requests.grants)
	}

	ts, _, _ = newAccessServer(t, domain.RoleMember, domain.RoleOwner)
	token = login(t, ts)
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/access-requests/acr_test/grant", token, "")
	if status != http.StatusOK {
		t.Fatalf("grant as owner = %d (body %s)", status, body)
	}
	var granted accessRequestDTO
	if err := json.Unmarshal(body, &granted); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if granted.State != domain.AccessRequestGranted || granted.DecidedByLabel != testEmail {
		t.Fatalf("granted = %+v, want granted with the decider's label", granted)
	}
	// One decision per request.
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/access-requests/acr_test/deny", token, `{"reason":"changed my mind"}`); status != http.StatusConflict {
		t.Fatalf("deciding twice = %d, want 409", status)
	}
}

// grant and deny are sessionOnly: an API token inherits its owner's role, so a
// leaked one must not be able to promote an account (threat-model §5.8).
func TestDecidingAnAccessRequestIsSessionOnly(t *testing.T) {
	ts, _, requests := newAccessServer(t, domain.RoleOwner, domain.RoleOwner)
	session := login(t, ts)
	full := createToken(t, ts, session, "ci", `["read","write","deploy"]`)

	for _, path := range []string{"/api/v1/access-requests/acr_test/grant", "/api/v1/access-requests/acr_test/deny"} {
		status, _, body := doJSON(t, "POST", ts.URL+path, full, "")
		if status != http.StatusForbidden {
			t.Errorf("%s with a full-ability token = %d, want 403 (body %s)", path, status, body)
		}
		if !strings.Contains(string(body), "interactive session") {
			t.Errorf("%s: body %s does not explain that a session is required", path, body)
		}
	}
	if len(requests.grants) != 0 || len(requests.denials) != 0 {
		t.Fatalf("a token decided a request: grants %v denials %v", requests.grants, requests.denials)
	}
	// Issuing an invitation IS token-reachable: it grants nothing by itself,
	// expires, and is revocable, so scripting team setup from CI is allowed.
	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/teams/tm_test/invites", full, `{"email":"ci@example.test"}`); status != http.StatusCreated {
		t.Fatalf("creating an invitation with a write token = %d, want 201 (body %s)", status, body)
	}
}

// A read-only token cannot mutate anything here, exactly as everywhere else.
func TestAccessMutationsRequireTheWriteAbility(t *testing.T) {
	ts, _, _ := newAccessServer(t, domain.RoleOwner, domain.RoleOwner)
	session := login(t, ts)
	readOnly := createToken(t, ts, session, "readonly", `["read"]`)

	for _, r := range teamScopedAccessRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, readOnly, r.body)
		if r.method == http.MethodGet {
			if status == http.StatusForbidden {
				t.Errorf("%s with a read token = 403, want it allowed (body %s)", r.name, body)
			}
			continue
		}
		if status != http.StatusForbidden {
			t.Errorf("%s with a read-only token = %d, want 403 (body %s)", r.name, status, body)
		}
	}
}

// The create response is the ONE place the accept URL appears; no listing, and
// no other response, may carry a token.
func TestAcceptURLAppearsExactlyOnce(t *testing.T) {
	ts, _, _ := newAccessServer(t, domain.RoleMember, domain.RoleOwner)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/teams/tm_test/invites", token,
		`{"email":"new@example.test","role":"admin"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d (body %s)", status, body)
	}
	var created createdInviteDTO
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if created.AcceptURL == "" || !created.MailSent {
		t.Fatalf("created = %+v, want an accept URL and the mail flag", created)
	}
	if created.Invite.State != domain.InviteStatePending {
		t.Errorf("state = %q, want pending", created.Invite.State)
	}

	secret := created.AcceptURL[strings.LastIndexByte(created.AcceptURL, '.')+1:]
	for _, probe := range []string{
		mustBody(t, ts, token, "GET", "/api/v1/teams/tm_test/invites"),
		mustBody(t, ts, token, "GET", "/api/v1/teams/tm_test/invites?state=all"),
	} {
		if strings.Contains(probe, secret) || strings.Contains(probe, "accept_url") ||
			strings.Contains(probe, "token_hash") {
			t.Fatalf("a listing carried an invitation token: %s", probe)
		}
	}
}

// An invitation the caller may not grant is a 403 from the service, surfaced
// as such rather than as a 500.
func TestInvitingAboveYourRankIsForbidden(t *testing.T) {
	ts, _, _ := newAccessServer(t, domain.RoleMember, domain.RoleAdmin)
	token := login(t, ts)
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/teams/tm_test/invites", token,
		`{"email":"new@example.test","role":"owner"}`)
	if status != http.StatusForbidden {
		t.Fatalf("admin inviting an owner = %d, want 403 (body %s)", status, body)
	}
}

func TestAccessRoutesValidateInput(t *testing.T) {
	ts, _, _ := newAccessServer(t, domain.RoleMember, domain.RoleOwner)
	token := login(t, ts)
	for _, tc := range []struct{ name, method, path, body string }{
		{"invite with no address", "POST", "/api/v1/teams/tm_test/invites", `{"email":""}`},
		{"invite with a stray field", "POST", "/api/v1/teams/tm_test/invites", `{"email":"a@b.test","nope":1}`},
		{"unknown state filter", "GET", "/api/v1/teams/tm_test/invites?state=weird", ""},
		{"unknown request state filter", "GET", "/api/v1/teams/tm_test/access-requests?state=weird", ""},
		{"asking for a role you hold", "POST", "/api/v1/teams/tm_test/access-requests", `{"requested_role":"member"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := doJSON(t, tc.method, ts.URL+tc.path, token, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", status, body)
			}
		})
	}
}

// The two public routes take no credential at all — that is the whole point of
// a link someone opens before they have an account.
func TestPublicInviteRoutesNeedNoSession(t *testing.T) {
	ts, invites, _ := newAccessServer(t, domain.RoleMember, domain.RoleOwner)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/invites/"+testInviteToken, "", "")
	if status != http.StatusOK {
		t.Fatalf("public preview = %d, want 200 (body %s)", status, body)
	}
	var preview invitePreviewDTO
	if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if preview.TeamName != "meridian studio" || preview.InviterLabel != "sam@meridian.dev" ||
		preview.Email != "priya@meridian.dev" || preview.Role != domain.RoleAdmin {
		t.Fatalf("preview = %+v, want the team, inviter, address and role", preview)
	}
	if strings.Contains(string(body), "SECRET") {
		t.Fatal("the preview echoed the invitation's secret")
	}

	invites.created = true
	status, _, body = doJSON(t, "POST", ts.URL+"/api/v1/invites/"+testInviteToken+"/accept", "",
		`{"password":"correct-horse","display_name":"Priya"}`)
	if status != http.StatusCreated {
		t.Fatalf("accept creating an account = %d, want 201 (body %s)", status, body)
	}
	var lr loginResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if lr.Token == "" || lr.User.Email != "priya@meridian.dev" {
		t.Fatalf("accept response = %+v, want a session for the invitee", lr)
	}
	if invites.lastAccept.Password != "correct-horse" || invites.lastAccept.DisplayName != "Priya" {
		t.Errorf("the form reached the service as %+v", invites.lastAccept)
	}

	// An existing account joining is a 200, not a 201: nothing was created.
	invites.created = false
	if status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/invites/"+testInviteToken+"/accept", "",
		`{"password":"correct-horse"}`); status != http.StatusOK {
		t.Fatalf("accept with an existing account = %d, want 200", status)
	}
}

// Every unusable link is the same 404, on both public routes.
func TestUnusableInviteLinkIs404(t *testing.T) {
	ts, invites, _ := newAccessServer(t, domain.RoleMember, domain.RoleOwner)
	invites.previewErr = access.ErrInvalidInvite
	invites.acceptErr = access.ErrInvalidInvite

	for _, tc := range []struct{ name, method, path, body string }{
		{"preview", "GET", "/api/v1/invites/inv_missing.NOPE", ""},
		{"accept", "POST", "/api/v1/invites/inv_missing.NOPE/accept", `{"password":"correct-horse"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := doJSON(t, tc.method, ts.URL+tc.path, "", tc.body)
			if status != http.StatusNotFound {
				t.Fatalf("status %d, want 404 (body %s)", status, body)
			}
			if strings.Contains(strings.ToLower(string(body)), "expired at") {
				t.Fatalf("the 404 distinguishes why: %s", body)
			}
		})
	}
}

// A 2FA-enabled account joining by invitation is asked for its code, in the
// same body shape the sign-in screen already reads.
func TestAcceptAsksForTheSecondFactor(t *testing.T) {
	ts, invites, _ := newAccessServer(t, domain.RoleMember, domain.RoleOwner)
	invites.acceptErr = auth.ErrTOTPRequired

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/invites/"+testInviteToken+"/accept", "",
		`{"password":"correct-horse"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 (body %s)", status, body)
	}
	var envelope errorBody
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if !envelope.TOTPRequired {
		t.Fatalf("body %s does not carry totp_required", body)
	}

	invites.acceptErr = auth.ErrInvalidCredentials
	if status, _, _ = doJSON(t, "POST", ts.URL+"/api/v1/invites/"+testInviteToken+"/accept", "",
		`{"password":"wrong"}`); status != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", status)
	}
}

// A throttled public call answers 429 with the countdown, like sign-in.
func TestThrottledInviteLookupAnswers429(t *testing.T) {
	ts, invites, _ := newAccessServer(t, domain.RoleMember, domain.RoleOwner)
	invites.previewErr = &auth.RateLimitedError{RetryAfter: 42 * time.Second}

	status, header, body := doJSON(t, "GET", ts.URL+"/api/v1/invites/"+testInviteToken, "", "")
	if status != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 (body %s)", status, body)
	}
	if header.Get("Retry-After") != "42" {
		t.Errorf("Retry-After = %q, want 42", header.Get("Retry-After"))
	}
	var envelope errorBody
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if envelope.RetryAfterSeconds != 42 {
		t.Errorf("retry_after_seconds = %d, want 42", envelope.RetryAfterSeconds)
	}
}

// A conflict from the service keeps its meaning: inviting an existing member
// and asking twice are both 409, not 500.
func TestAccessConflictsAre409(t *testing.T) {
	ts, invites, _ := newAccessServer(t, domain.RoleMember, domain.RoleOwner)
	invites.acceptErr = access.ErrAlreadyMember

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/invites/"+testInviteToken+"/accept", "",
		`{"password":"correct-horse"}`)
	if status != http.StatusConflict {
		t.Fatalf("status %d, want 409 (body %s)", status, body)
	}
}

// Denying carries the reason through, and an empty body is a valid denial.
func TestDenyAcceptsAnOptionalReason(t *testing.T) {
	ts, _, requests := newAccessServer(t, domain.RoleMember, domain.RoleOwner)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/access-requests/acr_test/deny", token,
		`{"reason":"Ask again after the audit."}`)
	if status != http.StatusOK {
		t.Fatalf("deny = %d (body %s)", status, body)
	}
	if requests.denials["acr_test"] != "Ask again after the audit." {
		t.Fatalf("reason reached the service as %q", requests.denials["acr_test"])
	}

	requests.requests["acr_2"] = domain.AccessRequest{
		ID: "acr_2", TeamID: "tm_test", UserID: "usr_x", State: domain.AccessRequestPending,
	}
	if status, _, body = doJSON(t, "POST", ts.URL+"/api/v1/access-requests/acr_2/deny", token, ""); status != http.StatusOK {
		t.Fatalf("deny with no body = %d, want 200 (body %s)", status, body)
	}
}

// Requesting access is a member's route, and the response says what was asked.
func TestMemberCanRequestAccess(t *testing.T) {
	ts, _, _ := newAccessServer(t, domain.RoleMember, domain.RoleMember)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/teams/tm_test/access-requests", token,
		`{"requested_role":"admin","message":"Need to deploy the import fix."}`)
	if status != http.StatusCreated {
		t.Fatalf("status %d, want 201 (body %s)", status, body)
	}
	var req accessRequestDTO
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if req.RequestedRole != domain.RoleAdmin || req.CurrentRole != domain.RoleMember ||
		req.State != domain.AccessRequestPending || req.Message == "" {
		t.Fatalf("request = %+v, want the ask, the current rank and the message", req)
	}
}
