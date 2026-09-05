package protection

// The gate and the decisions (deploy-protection.md §4, §5). Everything here
// runs on an injected clock, because every rule under test is a statement about
// a moment: what is frozen now, what a grant still covers, what a decision
// records.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

const (
	envID     = "env_prod"
	projectID = "prj_atlas"
	appID     = "app_web"
	depID     = "dep_9f2"
	revID     = "rev_1"
)

var (
	requester = domain.User{ID: "usr_alex", Email: "alex@acme.com"}
	approver  = domain.User{ID: "usr_sam", Email: "sam@acme.com"}
)

// newService builds the service over the fakes, frozen at a chosen instant.
func newService(t *testing.T, at time.Time) (*Service, *fakeStore, *fakePipeline, *fakeAnnouncer) {
	t.Helper()
	st := newFakeStore()
	st.envs[envID] = domain.Environment{ID: envID, ProjectID: projectID, Name: "production", Kind: domain.EnvProduction}
	st.apps[appID] = domain.Application{ID: appID, EnvironmentID: envID, Name: "web"}
	st.revisions[revID] = domain.Revision{ID: revID, ApplicationID: appID, SourceCommit: "c99d2e1a1b2c3d4e5f60718293a4b5c6d7e8f900"}
	st.deployments[depID] = domain.Deployment{ID: depID, ApplicationID: appID, RevisionID: revID, Status: domain.DeployAwaitingApproval}
	st.users[requester.ID] = requester
	st.users[approver.ID] = approver

	pipe := &fakePipeline{}
	ann := &fakeAnnouncer{}
	s := New(st, pipe, ann, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetClock(func() time.Time { return at })
	// Announcements are detached in production so they never hold the
	// scheduler's pipeline lock; run them inline here so the assertions are
	// deterministic rather than timing-dependent.
	s.detach = func(ctx context.Context, fn func(context.Context)) { fn(ctx) }
	return s, st, pipe, ann
}

func berlinAt(t *testing.T, y int, m time.Month, d, hh, mm int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	return time.Date(y, m, d, hh, mm, 0, 0, loc)
}

func freezeDoc() Document {
	return Document{
		MinApproverRole: domain.RoleOwner,
		FreezeEnabled:   true,
		Windows: []WindowInput{{
			StartDOW: time.Friday, StartMinute: 18 * 60,
			EndDOW: time.Monday, EndMinute: 8 * 60, Timezone: "Europe/Berlin",
		}},
	}
}

// ─── The gate ───────────────────────────────────────────────────────────────

// Acceptance 10: an environment that was never protected admits everything.
func TestAdmitIsClearWhenUnprotected(t *testing.T) {
	s, _, _, _ := newService(t, time.Now())
	adm, err := s.Admit(context.Background(), envID)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !adm.Clear() {
		t.Fatalf("unprotected environment = %+v, want clear", adm)
	}
	// And the read surface answers with the default document, not a 404.
	p, err := s.Get(context.Background(), envID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.RequireApproval || len(p.Windows) != 0 || p.MinApproverRole != domain.RoleOwner {
		t.Fatalf("default document = %+v", p)
	}
}

// Acceptance 8: inside the window a deploy is refused, and the refusal names
// the window and when it lifts; at the lift instant it runs.
func TestAdmitFreezesInsideTheWindow(t *testing.T) {
	ctx := context.Background()
	saturday := berlinAt(t, 2026, 8, 22, 12, 0)
	s, _, _, _ := newService(t, saturday)
	if _, err := s.Set(ctx, envID, freezeDoc()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	adm, err := s.Admit(ctx, envID)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !adm.Frozen {
		t.Fatalf("Saturday noon inside Fri→Mon = %+v, want frozen", adm)
	}
	if want := "production is frozen until Mon 08:00 Europe/Berlin"; adm.FreezeDetail != want {
		t.Fatalf("detail = %q, want %q", adm.FreezeDetail, want)
	}

	// The same environment at Mon 08:00 Berlin is clear.
	s.SetClock(func() time.Time { return berlinAt(t, 2026, 8, 24, 8, 0) })
	if adm, err = s.Admit(ctx, envID); err != nil || adm.Frozen {
		t.Fatalf("Monday 08:00 = %+v, %v; want clear", adm, err)
	}
}

// The master switch is real: windows declared but freeze disabled is not a
// freeze, and neither is freeze enabled with an empty calendar.
func TestAdmitRespectsTheFreezeSwitch(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newService(t, berlinAt(t, 2026, 8, 22, 12, 0))

	off := freezeDoc()
	off.FreezeEnabled = false
	if _, err := s.Set(ctx, envID, off); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if adm, err := s.Admit(ctx, envID); err != nil || adm.Frozen {
		t.Fatalf("switch off = %+v, %v; want clear", adm, err)
	}

	empty := Document{MinApproverRole: domain.RoleOwner, FreezeEnabled: true}
	if _, err := s.Set(ctx, envID, empty); err != nil {
		t.Fatalf("Set(empty): %v", err)
	}
	if adm, err := s.Admit(ctx, envID); err != nil || adm.Frozen {
		t.Fatalf("no windows = %+v, %v; want clear", adm, err)
	}
}

// When windows overlap, the refusal names the one that lifts LAST: telling an
// operator to come back at 08:00 when a second window runs to 10:00 would be a
// lie the next attempt exposes.
func TestAdmitNamesTheLatestLift(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newService(t, berlinAt(t, 2026, 8, 22, 12, 0))
	doc := freezeDoc()
	doc.Windows = append(doc.Windows, WindowInput{
		StartDOW: time.Saturday, StartMinute: 0,
		EndDOW: time.Monday, EndMinute: 10 * 60, Timezone: "Europe/Berlin",
	})
	if _, err := s.Set(ctx, envID, doc); err != nil {
		t.Fatalf("Set: %v", err)
	}
	adm, err := s.Admit(ctx, envID)
	if err != nil || !adm.Frozen {
		t.Fatalf("Admit = %+v, %v", adm, err)
	}
	if want := "production is frozen until Mon 10:00 Europe/Berlin"; adm.FreezeDetail != want {
		t.Fatalf("detail = %q, want %q — the later window must win", adm.FreezeDetail, want)
	}
}

// Fail closed: an unreadable policy refuses the deploy rather than passing it.
func TestAdmitFailsClosed(t *testing.T) {
	s, st, _, _ := newService(t, time.Now())
	st.failProtection = true
	if _, err := s.Admit(context.Background(), envID); err == nil {
		t.Fatal("an unreadable protection row admitted the deploy")
	}
}

// Acceptance 9: an open grant suspends the freeze, and only the freeze — the
// approval gate is untouched by it.
func TestBreakGlassSuspendsOnlyTheFreeze(t *testing.T) {
	ctx := context.Background()
	saturday := berlinAt(t, 2026, 8, 22, 12, 0)
	s, _, _, _ := newService(t, saturday)
	doc := freezeDoc()
	doc.RequireApproval = true
	if _, err := s.Set(ctx, envID, doc); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if adm, _ := s.Admit(ctx, envID); !adm.Frozen {
		t.Fatal("precondition: expected the environment to be frozen")
	}

	g, err := s.OpenBreakGlass(ctx, envID, approver, "checkout returning 500s")
	if err != nil {
		t.Fatalf("OpenBreakGlass: %v", err)
	}
	if g.OpenedBy != approver.ID || g.Reason != "checkout returning 500s" {
		t.Fatalf("grant = %+v", g)
	}
	if want := saturday.Add(domain.BreakGlassTTL); !g.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", g.ExpiresAt, want)
	}

	adm, err := s.Admit(ctx, envID)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if adm.Frozen {
		t.Fatal("an open grant did not suspend the freeze")
	}
	if !adm.NeedsApproval || adm.RequiredRole != domain.RoleOwner {
		t.Fatalf("break glass leaked into the approval gate: %+v", adm)
	}

	// It lapses on its own.
	s.SetClock(func() time.Time { return saturday.Add(domain.BreakGlassTTL + time.Second) })
	if adm, err = s.Admit(ctx, envID); err != nil || !adm.Frozen {
		t.Fatalf("after the grant lapsed = %+v, %v; want frozen again", adm, err)
	}
}

// A reason is the gate on the dialog, so it is required and single-line.
func TestOpenBreakGlassRequiresAReason(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newService(t, time.Now())
	for _, tc := range []struct{ name, reason string }{
		{"empty", ""},
		{"blank", "   "},
		{"multi-line", "incident\nsecond line"},
		{"too long", string(make([]rune, domain.MaxBreakGlassReason+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ve *ValidationError
			if _, err := s.OpenBreakGlass(ctx, envID, approver, tc.reason); !errors.As(err, &ve) {
				t.Fatalf("OpenBreakGlass(%q) = %v, want a validation error", tc.reason, err)
			}
		})
	}
}

// Preview environments stay unprotected: they cannot be given a policy, and the
// gate ignores their kind entirely (§9).
func TestPreviewEnvironmentsAreUnprotected(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := newService(t, berlinAt(t, 2026, 8, 22, 12, 0))
	st.envs["env_preview"] = domain.Environment{
		ID: "env_preview", ProjectID: projectID, Name: "pr-42", Kind: domain.EnvPreview,
	}
	if _, err := s.Set(ctx, "env_preview", freezeDoc()); !errors.Is(err, ErrPreviewProtection) {
		t.Fatalf("protecting a preview = %v, want ErrPreviewProtection", err)
	}
	// Even a row written behind the service's back is ignored by the gate.
	st.protection["env_preview"] = domain.EnvironmentProtection{
		EnvironmentID: "env_preview", RequireApproval: true, MinApproverRole: domain.RoleOwner,
	}
	adm, err := s.Admit(ctx, "env_preview")
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !adm.Clear() {
		t.Fatalf("preview admission = %+v, want clear", adm)
	}
}

// ─── The document ───────────────────────────────────────────────────────────

func TestSetValidatesTheDocument(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newService(t, time.Now())
	base := func() Document { return Document{MinApproverRole: domain.RoleOwner} }

	for _, tc := range []struct {
		name string
		doc  Document
	}{
		{"unknown role", Document{MinApproverRole: "root"}},
		{"day out of range", func() Document {
			d := base()
			d.Windows = []WindowInput{{StartDOW: 9, EndDOW: time.Monday, EndMinute: 60, Timezone: "UTC"}}
			return d
		}()},
		{"minute out of range", func() Document {
			d := base()
			d.Windows = []WindowInput{{StartMinute: 1440, EndDOW: time.Monday, EndMinute: 60, Timezone: "UTC"}}
			return d
		}()},
		{"zero-length window", func() Document {
			d := base()
			d.Windows = []WindowInput{{StartDOW: time.Friday, StartMinute: 60, EndDOW: time.Friday, EndMinute: 60, Timezone: "UTC"}}
			return d
		}()},
		{"missing timezone", func() Document {
			d := base()
			d.Windows = []WindowInput{{StartDOW: time.Friday, EndDOW: time.Monday, EndMinute: 60}}
			return d
		}()},
		{"unloadable timezone", func() Document {
			d := base()
			d.Windows = []WindowInput{{StartDOW: time.Friday, EndDOW: time.Monday, EndMinute: 60, Timezone: "Mars/Olympus"}}
			return d
		}()},
		{"too many windows", func() Document {
			d := base()
			for i := 0; i <= domain.MaxFreezeWindows; i++ {
				d.Windows = append(d.Windows, WindowInput{
					StartDOW: time.Friday, StartMinute: i, EndDOW: time.Monday, EndMinute: 60, Timezone: "UTC",
				})
			}
			return d
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ve *ValidationError
			if _, err := s.Set(ctx, envID, tc.doc); !errors.As(err, &ve) {
				t.Fatalf("Set = %v, want a validation error", err)
			}
		})
	}

	// An omitted role defaults to the narrowest one rather than to nothing.
	p, err := s.Set(ctx, envID, Document{RequireApproval: true})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if p.MinApproverRole != domain.RoleOwner {
		t.Fatalf("default min_approver_role = %q, want owner", p.MinApproverRole)
	}
}

// ─── Decisions ──────────────────────────────────────────────────────────────

// park + approve: the gate records the request, the pipeline is asked to
// release it, and the people who can act are told.
func TestParkThenApprove(t *testing.T) {
	ctx := context.Background()
	s, st, pipe, ann := newService(t, time.Now())
	st.approvers[projectID+"/"+domain.RoleOwner] = 2
	st.qualifies[approver.ID+"/"+domain.RoleOwner] = true

	dep := st.deployments[depID]
	if err := s.Park(ctx, dep, envID, requester.ID, domain.RoleOwner); err != nil {
		t.Fatalf("Park: %v", err)
	}
	ap, err := s.ApprovalFor(ctx, depID)
	if err != nil {
		t.Fatalf("ApprovalFor: %v", err)
	}
	if !ap.Pending() || ap.RequestedBy != requester.ID || ap.RequiredRole != domain.RoleOwner {
		t.Fatalf("parked approval = %+v", ap)
	}
	if len(ann.awaiting) != 1 {
		t.Fatalf("awaiting notices = %d, want 1", len(ann.awaiting))
	}
	n := ann.awaiting[0]
	if n.ProjectID != projectID || n.ApplicationName != "web" || n.RequiredRole != domain.RoleOwner ||
		n.RequesterEmail != requester.Email || n.DeploymentID != depID {
		t.Fatalf("awaiting notice = %+v", n)
	}

	got, decided, err := s.Approve(ctx, depID, approver)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if decided.State != domain.ApprovalApproved || decided.DecidedBy != approver.ID {
		t.Fatalf("decision = %+v", decided)
	}
	if got.Status != domain.DeployQueued {
		t.Fatalf("approved deployment = %+v", got)
	}
	if len(pipe.approved) != 1 || pipe.approved[0] != depID {
		t.Fatalf("pipeline approvals = %v", pipe.approved)
	}
	if len(ann.approved) != 1 || ann.approved[0].RequestedBy != requester.ID {
		t.Fatalf("approval notices = %+v", ann.approved)
	}

	// Decisions are once-only.
	if _, _, err := s.Approve(ctx, depID, approver); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second approve = %v, want ErrAlreadyDecided", err)
	}
}

// Acceptance 3: rejection carries a reason and names the rejecter, and the
// pipeline is asked to end the deployment with that sentence.
func TestRejectNamesTheRejecter(t *testing.T) {
	ctx := context.Background()
	s, st, pipe, ann := newService(t, time.Now())
	if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); err != nil {
		t.Fatalf("Park: %v", err)
	}

	dep, decided, err := s.Reject(ctx, depID, "shipping Monday", approver)
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if decided.State != domain.ApprovalRejected || decided.Reason != "shipping Monday" ||
		decided.DecidedBy != approver.ID || decided.DecidedAt == nil {
		t.Fatalf("decision = %+v", decided)
	}
	want := "rejected by sam@acme.com: shipping Monday"
	if dep.Status != domain.DeployFailed || dep.Detail != want {
		t.Fatalf("rejected deployment = %+v, want detail %q", dep, want)
	}
	if len(pipe.rejected) != 1 || pipe.details[0] != want {
		t.Fatalf("pipeline rejections = %v / %v", pipe.rejected, pipe.details)
	}
	if len(ann.rejected) != 1 || ann.rejected[0].Reason != "shipping Monday" ||
		ann.rejected[0].ActorEmail != approver.Email {
		t.Fatalf("rejection notices = %+v", ann.rejected)
	}
	if _, _, err := s.Reject(ctx, depID, "again", approver); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second reject = %v, want ErrAlreadyDecided", err)
	}
}

func TestRejectRequiresAReason(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := newService(t, time.Now())
	if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); err != nil {
		t.Fatalf("Park: %v", err)
	}
	var ve *ValidationError
	if _, _, err := s.Reject(ctx, depID, "   ", approver); !errors.As(err, &ve) {
		t.Fatalf("Reject with no reason = %v, want a validation error", err)
	}
	// And the gate is still open — a refused request decides nothing.
	if ap, _ := s.ApprovalFor(ctx, depID); !ap.Pending() {
		t.Fatalf("a rejected-with-no-reason call decided the gate: %+v", ap)
	}
}

// Acceptance 6: "two-person rule where two people exist".
func TestSelfApprovalIsRefusedOnlyWhenSomeoneElseCould(t *testing.T) {
	ctx := context.Background()

	t.Run("another qualifying approver exists", func(t *testing.T) {
		s, st, _, _ := newService(t, time.Now())
		st.approvers[projectID+"/"+domain.RoleOwner] = 2
		st.qualifies[requester.ID+"/"+domain.RoleOwner] = true
		if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); err != nil {
			t.Fatalf("Park: %v", err)
		}
		if _, _, err := s.Approve(ctx, depID, requester); !errors.Is(err, ErrSelfApproval) {
			t.Fatalf("self-approve = %v, want ErrSelfApproval", err)
		}
	})

	t.Run("sole qualifying approver", func(t *testing.T) {
		s, st, pipe, _ := newService(t, time.Now())
		st.approvers[projectID+"/"+domain.RoleOwner] = 1
		st.qualifies[requester.ID+"/"+domain.RoleOwner] = true
		if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); err != nil {
			t.Fatalf("Park: %v", err)
		}
		if _, _, err := s.Approve(ctx, depID, requester); err != nil {
			t.Fatalf("the only owner could not approve their own deploy: %v", err)
		}
		if len(pipe.approved) != 1 {
			t.Fatalf("pipeline approvals = %v", pipe.approved)
		}
	})

	// Withdrawing your own request grants nothing, so self-REJECTION is
	// allowed where self-approval is not.
	t.Run("self-rejection is allowed", func(t *testing.T) {
		s, st, _, _ := newService(t, time.Now())
		st.approvers[projectID+"/"+domain.RoleOwner] = 2
		st.qualifies[requester.ID+"/"+domain.RoleOwner] = true
		if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); err != nil {
			t.Fatalf("Park: %v", err)
		}
		if _, _, err := s.Reject(ctx, depID, "changed my mind", requester); err != nil {
			t.Fatalf("self-reject: %v", err)
		}
	})
}

// A webhook deploy parks with no requester, so there is nobody to tell about
// the decision — and the awaiting item still reaches the approvers.
func TestWebhookParkHasNoRequester(t *testing.T) {
	ctx := context.Background()
	s, st, _, ann := newService(t, time.Now())
	if err := s.Park(ctx, st.deployments[depID], envID, "", domain.RoleOwner); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if len(ann.awaiting) != 1 || ann.awaiting[0].RequestedBy != "" {
		t.Fatalf("awaiting notice = %+v", ann.awaiting)
	}
	if got := ann.awaiting[0].RequestedByEmailOrPush(); got != "pushed via webhook" {
		t.Fatalf("requester line = %q", got)
	}
	// Nobody asked for it, so nobody is told it was approved.
	if _, _, err := s.Approve(ctx, depID, approver); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if len(ann.approved) != 0 {
		t.Fatalf("a requester-less deploy produced decision notices: %+v", ann.approved)
	}
}

// An approval row that cannot be written fails Park, because a deployment
// parked without one could never be decided.
func TestParkFailsWhenTheApprovalCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := newService(t, time.Now())
	if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); err != nil {
		t.Fatalf("Park: %v", err)
	}
	// A second park on the same deployment conflicts on the natural key.
	if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second park = %v, want a conflict", err)
	}
}

// An announcer that fails does not fail the decision: the gate is already
// recorded, and the approval is still reachable from the queue.
func TestAnnouncementFailureDoesNotFailTheDecision(t *testing.T) {
	ctx := context.Background()
	s, st, _, ann := newService(t, time.Now())
	ann.err = errors.New("inbox down")
	if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if _, _, err := s.Approve(ctx, depID, approver); err != nil {
		t.Fatalf("Approve: %v", err)
	}
}

// New documents a nil announcer as supported — an inbox-less panel is a real
// configuration — so every path that announces has to survive one. The trap
// here is not the guard but WHERE the record function is chosen: a method value
// read off a nil interface at the call site dereferences it before the guard
// can run, so this covers Park, Approve and Reject together.
func TestNilAnnouncerAnnouncesNothingAndPanicsNowhere(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	st.envs[envID] = domain.Environment{ID: envID, ProjectID: projectID, Name: "production"}
	st.apps[appID] = domain.Application{ID: appID, EnvironmentID: envID, Name: "web"}
	st.revisions[revID] = domain.Revision{ID: revID, ApplicationID: appID}
	st.users[requester.ID] = requester

	s := New(st, &fakePipeline{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.detach = func(ctx context.Context, fn func(context.Context)) { fn(ctx) }

	for _, tc := range []struct {
		name   string
		depID  string
		decide func(*Service, string) error
	}{
		{"approve", "dep_nil_a", func(s *Service, id string) error {
			_, _, err := s.Approve(ctx, id, approver)
			return err
		}},
		{"reject", "dep_nil_r", func(s *Service, id string) error {
			_, _, err := s.Reject(ctx, id, "shipping Monday", approver)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st.deployments[tc.depID] = domain.Deployment{ID: tc.depID, ApplicationID: appID, RevisionID: revID}
			if err := s.Park(ctx, st.deployments[tc.depID], envID, requester.ID, domain.RoleOwner); err != nil {
				t.Fatalf("Park with no announcer: %v", err)
			}
			if err := tc.decide(s, tc.depID); err != nil {
				t.Fatalf("%s with no announcer: %v", tc.name, err)
			}
			ap, err := s.ApprovalFor(ctx, tc.depID)
			if err != nil || ap.State == domain.ApprovalPending {
				t.Fatalf("decision was not recorded: %+v, %v", ap, err)
			}
		})
	}
}

// The announcement really is detached: the default constructor runs it on its
// own goroutine, so Park returns without waiting for the inbox — and the write
// still lands.
func TestAnnouncementIsDetached(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	st.envs[envID] = domain.Environment{ID: envID, ProjectID: projectID, Name: "production"}
	st.apps[appID] = domain.Application{ID: appID, EnvironmentID: envID, Name: "web"}
	st.revisions[revID] = domain.Revision{ID: revID, ApplicationID: appID}
	st.deployments[depID] = domain.Deployment{ID: depID, ApplicationID: appID, RevisionID: revID}
	st.users[requester.ID] = requester

	// Blocks until the test releases it: if Park waited for the announcement,
	// Park itself would not return.
	release := make(chan struct{})
	landed := make(chan struct{})
	ann := &blockingAnnouncer{release: release, landed: landed}

	// The DEFAULT detach — no inline override — is what is under test.
	s := New(st, &fakePipeline{}, ann, slog.New(slog.NewTextHandler(io.Discard, nil)))

	done := make(chan error, 1)
	go func() { done <- s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Park: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Park blocked on the announcement; it must not hold the scheduler's pipeline lock")
	}
	// The approval is already durable before anyone is told.
	if ap, err := s.ApprovalFor(ctx, depID); err != nil || !ap.Pending() {
		t.Fatalf("approval after Park = %+v, %v", ap, err)
	}
	close(release)
	select {
	case <-landed:
	case <-time.After(2 * time.Second):
		t.Fatal("the detached announcement never ran")
	}
}

// blockingAnnouncer holds the awaiting-approval write until released.
type blockingAnnouncer struct {
	release <-chan struct{}
	landed  chan<- struct{}
}

func (b *blockingAnnouncer) RecordDeployAwaitingApproval(_ context.Context, _ inbox.DeployNotice) error {
	<-b.release
	close(b.landed)
	return nil
}
func (b *blockingAnnouncer) RecordDeployApproved(context.Context, inbox.DeployNotice) error {
	return nil
}
func (b *blockingAnnouncer) RecordDeployRejected(context.Context, inbox.DeployNotice) error {
	return nil
}

// The approval queue filters by state and refuses an unknown one.
func TestApprovalsFilter(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := newService(t, time.Now())
	if err := s.Park(ctx, st.deployments[depID], envID, requester.ID, domain.RoleOwner); err != nil {
		t.Fatalf("Park: %v", err)
	}
	pending, err := s.Approvals(ctx, envID, domain.ApprovalPending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if got, err := s.Approvals(ctx, envID, domain.ApprovalApproved); err != nil || len(got) != 0 {
		t.Fatalf("approved = %+v, %v", got, err)
	}
	var ve *ValidationError
	if _, err := s.Approvals(ctx, envID, "maybe"); !errors.As(err, &ve) {
		t.Fatalf("unknown state = %v, want a validation error", err)
	}
}
