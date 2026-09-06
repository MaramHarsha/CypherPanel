package access

// Invitations (invitations-and-access-requests.md §9, acceptance 1–7).
//
// The properties under test are the ones a reviewer should be able to check by
// reading a single test name: the rank is fixed at issue time, the token is
// never stored or returned in clear, a wrong guess cannot burn a valid link,
// accepting is single-use, and an existing account's password is never reset.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
)

const inviterEmail = "sam@meridian.dev"

// newInvites wires the service with fakes and returns everything a test asserts
// on. The clock is the store's, so an invitation can be aged without sleeping.
func newInvites(t *testing.T) (*Invites, *fakeStore, *fakeSessions, *fakeMailer, *fakeAnnouncer) {
	t.Helper()
	st := newFakeStore()
	sessions := newFakeSessions()
	sessions.store = st
	mailer := &fakeMailer{}
	announcer := &fakeAnnouncer{}
	svc := NewInvites(st, sessions, mailer, announcer, auth.NewLimiter(5, time.Minute),
		"https://panel.example.test/", quietLog())
	svc.now = st.now
	return svc, st, sessions, mailer, announcer
}

func inviter(st *fakeStore) domain.User {
	u := st.addUser("usr_sam", inviterEmail, domain.RoleMember)
	st.addMember("tm_1", u.ID, domain.RoleAdmin)
	return u
}

// tokenFrom recovers the wire token from the accept URL — the only place it is
// ever readable.
func tokenFrom(t *testing.T, url string) string {
	t.Helper()
	const prefix = "https://panel.example.test/invite/"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("accept URL %q does not start with %q", url, prefix)
	}
	return strings.TrimPrefix(url, prefix)
}

func TestCreateInviteIssuesALinkAndMailsIt(t *testing.T) {
	svc, st, _, mailer, _ := newInvites(t)
	sam := inviter(st)

	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "Priya@Meridian.dev", Role: domain.RoleAdmin}, sam, domain.RoleAdmin)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The address is folded and parsed, never taken as typed: it ends up in a
	// recipient list and an email body.
	if created.Invite.Email != "priya@meridian.dev" {
		t.Errorf("stored email = %q, want the lower-cased parsed address", created.Invite.Email)
	}
	if created.Invite.Role != domain.RoleAdmin {
		t.Errorf("role = %q, want admin", created.Invite.Role)
	}
	if created.Invite.InvitedByLabel != inviterEmail {
		t.Errorf("inviter label = %q, want %q", created.Invite.InvitedByLabel, inviterEmail)
	}
	if got := created.Invite.ExpiresAt.Sub(created.Invite.CreatedAt); got < 6*24*time.Hour || got > 8*24*time.Hour {
		t.Errorf("lifetime = %v, want about 7 days", got)
	}
	if !created.MailSent || len(mailer.sent) != 1 {
		t.Fatalf("mail_sent = %v with %d messages, want one message sent", created.MailSent, len(mailer.sent))
	}
	if mailer.sent[0].To[0] != "priya@meridian.dev" {
		t.Errorf("recipient = %v, want the stored address", mailer.sent[0].To)
	}
	if !strings.Contains(mailer.sent[0].Body, created.AcceptURL) {
		t.Errorf("mail body does not carry the accept link")
	}

	// The secret is never stored: only its hash is, and the hash is not the
	// token.
	token := tokenFrom(t, created.AcceptURL)
	secret := token[strings.LastIndexByte(token, '.')+1:]
	stored, err := st.GetTeamInvite(ctx(), created.Invite.ID)
	if err != nil {
		t.Fatalf("GetTeamInvite: %v", err)
	}
	if string(stored.TokenHash) == secret {
		t.Fatal("the invitation's secret is stored in clear")
	}
	if !auth.ConstantTimeEqual(stored.TokenHash, auth.HashToken(secret)) {
		t.Fatal("the stored hash is not sha256 of the wire secret")
	}
}

// The rank is checked when the invitation is CREATED, because the person
// accepting has none of their own (spec §1).
func TestCreateInviteCannotExceedTheIssuersRank(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)

	if _, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "new@meridian.dev", Role: domain.RoleOwner}, sam, domain.RoleAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin inviting an owner: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "new@meridian.dev", Role: domain.RoleOwner}, sam, domain.RoleOwner); err != nil {
		t.Fatalf("owner inviting an owner: %v", err)
	}
}

func TestCreateInviteValidatesInput(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)

	for _, tc := range []struct{ name, email, role string }{
		{"not an address", "not-an-address", domain.RoleMember},
		{"empty address", "", domain.RoleMember},
		{"unknown role", "new@meridian.dev", "superuser"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ve *ValidationError
			_, err := svc.Create(ctx(), "tm_1", CreateInput{Email: tc.email, Role: tc.role}, sam, domain.RoleOwner)
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
		})
	}
}

// Inviting someone the team already has is a 409: re-ranking a member belongs
// to the member-role route, which has the last-owner guard (spec §8).
func TestCreateInviteRefusesAnExistingMember(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)
	priya := st.addUser("usr_priya", "priya@meridian.dev", domain.RoleMember)
	st.addMember("tm_1", priya.ID, domain.RoleMember)

	if _, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("err = %v, want ErrAlreadyMember", err)
	}
}

// Acceptance 6: re-inviting an address supersedes — the first link dies, the
// second works. That is how an operator fixes a link that went astray.
func TestReInvitingSupersedesTheOutstandingLink(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)

	first, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if _, err := svc.Preview(ctx(), tokenFrom(t, first.AcceptURL), "1.2.3.4"); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("superseded link: err = %v, want ErrInvalidInvite", err)
	}
	if _, err := svc.Preview(ctx(), tokenFrom(t, second.AcceptURL), "1.2.3.4"); err != nil {
		t.Fatalf("fresh link: %v", err)
	}
}

func TestPreviewDescribesTheInvitation(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)
	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev", Role: domain.RoleAdmin}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	p, err := svc.Preview(ctx(), tokenFrom(t, created.AcceptURL), "1.2.3.4")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if p.TeamName != "meridian studio" || p.InviterLabel != inviterEmail ||
		p.Email != "priya@meridian.dev" || p.Role != domain.RoleAdmin {
		t.Fatalf("preview = %+v, want the team, inviter, address and role", p)
	}
	if p.AccountExists {
		t.Error("account_exists = true for an address with no account")
	}

	st.addUser("usr_priya", "priya@meridian.dev", domain.RoleMember)
	p, err = svc.Preview(ctx(), tokenFrom(t, created.AcceptURL), "1.2.3.4")
	if err != nil {
		t.Fatalf("Preview after the account exists: %v", err)
	}
	if !p.AccountExists {
		t.Error("account_exists = false for an address that has an account")
	}
}

// Acceptance 5: unknown, wrong-secret, expired and revoked are ONE answer, and
// a wrong guess against a real id does not consume the valid invitation.
func TestEveryUnusableTokenIsTheSame404(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)
	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	good := tokenFrom(t, created.AcceptURL)
	id := created.Invite.ID

	for _, tc := range []struct{ name, token string }{
		{"no separator", "not-a-token"},
		{"unknown id", "inv_missing.WHATEVER"},
		{"wrong secret against a real id", id + ".WRONGSECRET"},
		{"empty secret", id + "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Preview(ctx(), tc.token, "1.2.3.4"); !errors.Is(err, ErrInvalidInvite) {
				t.Fatalf("err = %v, want ErrInvalidInvite", err)
			}
		})
	}
	// The valid link still works: a guess cannot burn it.
	if _, err := svc.Preview(ctx(), good, "5.6.7.8"); err != nil {
		t.Fatalf("valid link after four wrong guesses: %v", err)
	}

	// Expiry and revocation answer the same way.
	st.mu.Lock()
	inv := st.invites[id]
	inv.ExpiresAt = st.now().Add(-time.Minute)
	st.invites[id] = inv
	st.mu.Unlock()
	if _, err := svc.Preview(ctx(), good, "5.6.7.8"); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("expired link: err = %v, want ErrInvalidInvite", err)
	}
}

// A database that is down is the PANEL's failure, not the invitee's. It must
// not wear the "used already, revoked, or expired" 404, it must not cost the
// caller a strike against the throttle, and it must reach the handler wrapped
// so the default branch logs it and answers 500 (ENGINEERING rule 3).
func TestAStoreFailureIsNotAnInvalidInvitation(t *testing.T) {
	st := newFakeStore()
	sessions := newFakeSessions()
	sessions.store = st
	// One strike per window: if either failure were charged to the address, the
	// invitee's next attempt would be refused 429 instead of served.
	svc := NewInvites(st, sessions, nil, nil, auth.NewLimiter(1, time.Minute),
		"https://panel.example.test", quietLog())
	svc.now = st.now
	sam := inviter(st)
	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleAdmin)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := tokenFrom(t, created.AcceptURL)

	outage := errors.New("connection refused")
	st.mu.Lock()
	st.failGetInvite = outage
	st.mu.Unlock()

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"preview", func() error { _, err := svc.Preview(ctx(), token, "1.2.3.4"); return err }},
		{"accept", func() error {
			_, err := svc.Accept(ctx(), token, AcceptInput{Password: "correct-horse"}, "1.2.3.4")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if errors.Is(err, ErrInvalidInvite) {
				t.Fatalf("err = %v — an outage told a legitimate invitee their link was spent", err)
			}
			if !errors.Is(err, outage) {
				t.Fatalf("err = %v, want it to wrap the store failure", err)
			}
		})
	}

	// The database comes back; the invitation is untouched and the address was
	// never charged for our outage.
	st.mu.Lock()
	st.failGetInvite = nil
	st.mu.Unlock()
	if _, err := svc.Preview(ctx(), token, "1.2.3.4"); err != nil {
		t.Fatalf("preview once the database is back: %v — the caller paid for our failure", err)
	}
}

// Acceptance 3 and 4: accepting creates the account, adds the membership at the
// invited role, returns a session — and a replay is a 404 that grants nothing.
func TestAcceptCreatesTheAccountOnceAndOnlyOnce(t *testing.T) {
	svc, st, sessions, _, announcer := newInvites(t)
	sam := inviter(st)
	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev", Role: domain.RoleAdmin}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := tokenFrom(t, created.AcceptURL)

	accepted, err := svc.Accept(ctx(), token, AcceptInput{Password: "correct-horse", DisplayName: "Priya"}, "1.2.3.4")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !accepted.Created {
		t.Error("Created = false, want true for an address with no account")
	}
	if accepted.Token == "" {
		t.Error("no session token returned — the invitee would land at a second form")
	}
	if accepted.TeamName != "meridian studio" {
		t.Errorf("team name = %q", accepted.TeamName)
	}
	if got := st.role("tm_1", accepted.User.ID); got != domain.RoleAdmin {
		t.Errorf("membership role = %q, want admin", got)
	}
	// The account is panel-role member: an invitation grants a place in one
	// TEAM, never anything panel-wide.
	if accepted.User.Role != domain.RoleMember {
		t.Errorf("panel role = %q, want member", accepted.User.Role)
	}
	if sessions.profiles[accepted.User.ID] != "Priya" {
		t.Errorf("display name = %q, want Priya", sessions.profiles[accepted.User.ID])
	}
	if len(announcer.accepted) != 1 || announcer.accepted[0].InviterID != sam.ID {
		t.Fatalf("invite-accepted notices = %+v, want one addressed to the inviter", announcer.accepted)
	}
	if announcer.accepted[0].Role != domain.RoleAdmin || announcer.accepted[0].Email != "priya@meridian.dev" {
		t.Errorf("notice = %+v, want it to name who joined as what", announcer.accepted[0])
	}

	// Replay.
	if _, err := svc.Accept(ctx(), token, AcceptInput{Password: "correct-horse"}, "1.2.3.4"); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("replay: err = %v, want ErrInvalidInvite", err)
	}
}

func TestAcceptEnforcesThePasswordFloorForANewAccount(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)
	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := tokenFrom(t, created.AcceptURL)

	var ve *ValidationError
	if _, err := svc.Accept(ctx(), token, AcceptInput{Password: "short"}, "1.2.3.4"); !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	// A refused password must not have spent the invitation.
	if _, err := svc.Preview(ctx(), token, "1.2.3.4"); err != nil {
		t.Fatalf("link after a refused password: %v", err)
	}
}

// Acceptance 7: an address that already has an account signs in with its
// CURRENT password — the invitation never resets one — and a 2FA-enabled
// account still demands its code.
func TestAcceptForAKnownAddressGoesThroughSignIn(t *testing.T) {
	svc, st, sessions, _, _ := newInvites(t)
	sam := inviter(st)
	priya := st.addUser("usr_priya", "priya@meridian.dev", domain.RoleMember)
	sessions.users["priya@meridian.dev"] = priya

	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev", Role: domain.RoleAdmin}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := tokenFrom(t, created.AcceptURL)

	// Wrong password: refused, and the link survives.
	sessions.loginErr = auth.ErrInvalidCredentials
	if _, err := svc.Accept(ctx(), token, AcceptInput{Password: "wrong"}, "1.2.3.4"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong password: err = %v, want ErrInvalidCredentials", err)
	}
	if st.role("tm_1", priya.ID) != "" {
		t.Fatal("a refused sign-in granted the membership anyway")
	}

	// Second factor demanded: the same, with a distinct error the screen reads.
	sessions.loginErr = auth.ErrTOTPRequired
	if _, err := svc.Accept(ctx(), token, AcceptInput{Password: "right"}, "1.2.3.4"); !errors.Is(err, auth.ErrTOTPRequired) {
		t.Fatalf("2FA account: err = %v, want ErrTOTPRequired", err)
	}

	// With the right credentials it joins, and the account is untouched.
	sessions.loginErr = nil
	accepted, err := svc.Accept(ctx(), token, AcceptInput{Password: "right", TOTPCode: "123456"}, "1.2.3.4")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.Created {
		t.Error("Created = true for an address that already had an account")
	}
	if got := st.role("tm_1", priya.ID); got != domain.RoleAdmin {
		t.Errorf("membership role = %q, want admin", got)
	}
	last := sessions.logins[len(sessions.logins)-1]
	if last[0] != "priya@meridian.dev" || last[1] != "right" || last[2] != "123456" {
		t.Errorf("sign-in was asked for %v, want the invited address with the supplied password and code", last)
	}
	if len(sessions.started) != 0 {
		t.Error("StartSession was called for an existing account — the session must come from sign-in")
	}
}

// Someone added by hand between the invitation and the acceptance: refuse, and
// kill the now-moot link rather than leave it live for a week (spec §8).
func TestAcceptRefusesAnExistingMemberAndRevokesTheLink(t *testing.T) {
	svc, st, sessions, _, _ := newInvites(t)
	sam := inviter(st)
	priya := st.addUser("usr_priya", "priya@meridian.dev", domain.RoleMember)
	sessions.users["priya@meridian.dev"] = priya

	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := tokenFrom(t, created.AcceptURL)
	st.addMember("tm_1", priya.ID, domain.RoleMember)

	if _, err := svc.Accept(ctx(), token, AcceptInput{Password: "right"}, "1.2.3.4"); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("err = %v, want ErrAlreadyMember", err)
	}
	if _, err := svc.Preview(ctx(), token, "1.2.3.4"); !errors.Is(err, ErrInvalidInvite) {
		t.Fatal("the moot link is still live")
	}
}

// The public routes are throttled by client address, and the refusal carries
// the wait the sign-in screen counts down against (spec §8).
func TestPublicRoutesAreThrottledPerAddress(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)
	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for range 5 {
		if _, err := svc.Preview(ctx(), "inv_missing.NOPE", "9.9.9.9"); !errors.Is(err, ErrInvalidInvite) {
			t.Fatalf("guess: err = %v, want ErrInvalidInvite", err)
		}
	}
	var limited *auth.RateLimitedError
	_, err = svc.Preview(ctx(), "inv_missing.NOPE", "9.9.9.9")
	if !errors.As(err, &limited) || limited.RetryAfterSeconds() < 1 {
		t.Fatalf("sixth guess: err = %v, want a RateLimitedError with a positive wait", err)
	}
	// Another address is unaffected — one attacker must not lock out an
	// invitee.
	if _, err := svc.Preview(ctx(), tokenFrom(t, created.AcceptURL), "1.2.3.4"); err != nil {
		t.Fatalf("a different client address: %v", err)
	}
}

// A panel with no mail transport still issues invitations: the accept URL is
// returned, and mail_sent says so honestly (spec §6).
func TestInviteWithoutMailStillIssuesTheLink(t *testing.T) {
	st := newFakeStore()
	svc := NewInvites(st, newFakeSessions(), nil, nil, nil, "https://panel.example.test", quietLog())
	svc.now = st.now
	sam := inviter(st)

	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.MailSent {
		t.Error("mail_sent = true with no transport configured")
	}
	if created.AcceptURL == "" {
		t.Fatal("no accept URL — the operator has nothing to hand over")
	}
	if _, err := svc.Preview(ctx(), tokenFrom(t, created.AcceptURL), "1.2.3.4"); err != nil {
		t.Fatalf("the link from a mail-less panel does not work: %v", err)
	}
}

// Listing never carries the token or its hash, whatever the state filter says.
func TestListNeverCarriesTheToken(t *testing.T) {
	svc, st, _, _, _ := newInvites(t)
	sam := inviter(st)
	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token := tokenFrom(t, created.AcceptURL)

	list, err := svc.List(ctx(), "tm_1", true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d invitations, want 1", len(list))
	}
	if strings.Contains(string(list[0].TokenHash), token) {
		t.Fatal("the wire token is recoverable from a listing")
	}
	if list[0].State(st.now()) != domain.InviteStatePending {
		t.Errorf("state = %q, want pending", list[0].State(st.now()))
	}
}

// An invitation whose issuer has been deleted still reads: the label is a
// snapshot, and nobody is told when it is accepted because there is no inbox to
// tell (spec §2, §6).
func TestInviteFromADeletedIssuerStillWorks(t *testing.T) {
	svc, st, _, _, announcer := newInvites(t)
	sam := inviter(st)
	created, err := svc.Create(ctx(), "tm_1", CreateInput{Email: "priya@meridian.dev"}, sam, domain.RoleOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	st.mu.Lock()
	inv := st.invites[created.Invite.ID]
	inv.InvitedBy = nil // the account was deleted; the label survives
	st.invites[created.Invite.ID] = inv
	st.mu.Unlock()

	p, err := svc.Preview(ctx(), tokenFrom(t, created.AcceptURL), "1.2.3.4")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if p.InviterLabel != inviterEmail {
		t.Errorf("inviter label = %q, want the snapshot %q", p.InviterLabel, inviterEmail)
	}
	if _, err := svc.Accept(ctx(), tokenFrom(t, created.AcceptURL), AcceptInput{Password: "correct-horse"}, "1.2.3.4"); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(announcer.accepted) != 0 {
		t.Errorf("notices = %+v, want none — there is nobody to tell", announcer.accepted)
	}
}
