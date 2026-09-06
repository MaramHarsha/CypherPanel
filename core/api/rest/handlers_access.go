package rest

// Team invitations and access requests
// (invitations-and-access-requests.md §3, §7).
//
// Three shapes of authorization meet in this file:
//
//   - the team-scoped routes resolve the caller's rank in the team and refuse
//     with 404 for a non-member (a team you cannot see does not exist) and 403
//     for insufficient rank — the posture every other team route already has;
//   - the two decision routes are additionally sessionOnly, because promoting
//     an account is durable, panel-wide privilege and an API token inherits its
//     owner's role (threat-model §5.8);
//   - the two public routes have no principal at all. They are bearer-token
//     gated by the invitation itself, throttled by client address, and every
//     failure is the same 404, so a guess learns nothing.

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/access"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/teams"
)

// ─── DTOs ───────────────────────────────────────────────────────────────────

// teamInviteDTO is an invitation as the API may describe it. There is no field
// that could carry the token: the accept URL exists only in createdInviteDTO,
// exactly once (spec §7).
type teamInviteDTO struct {
	ID     string `json:"id"`
	TeamID string `json:"team_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	// State is derived from the timestamps, never stored.
	State          string     `json:"state"`
	InvitedByLabel string     `json:"invited_by_label"`
	ExpiresAt      time.Time  `json:"expires_at"`
	AcceptedAt     *time.Time `json:"accepted_at"`
	RevokedAt      *time.Time `json:"revoked_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func toInviteDTO(inv domain.TeamInvite, now time.Time) teamInviteDTO {
	return teamInviteDTO{
		ID: inv.ID, TeamID: inv.TeamID, Email: inv.Email, Role: inv.Role,
		State:          inv.State(now),
		InvitedByLabel: inv.InvitedByLabel,
		ExpiresAt:      inv.ExpiresAt,
		AcceptedAt:     inv.AcceptedAt,
		RevokedAt:      inv.RevokedAt,
		CreatedAt:      inv.CreatedAt,
	}
}

type createdInviteDTO struct {
	Invite teamInviteDTO `json:"invite"`
	// AcceptURL is readable here and nowhere else, ever — only a hash of its
	// secret is stored. It is returned whether or not the mail went out,
	// because a panel with no SMTP is the common self-hosted case (spec §6).
	AcceptURL string `json:"accept_url"`
	MailSent  bool   `json:"mail_sent"`
}

type invitePreviewDTO struct {
	TeamName      string    `json:"team_name"`
	InviterLabel  string    `json:"inviter_label"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	ExpiresAt     time.Time `json:"expires_at"`
	AccountExists bool      `json:"account_exists"`
}

type accessRequestDTO struct {
	ID            string `json:"id"`
	TeamID        string `json:"team_id"`
	UserID        string `json:"user_id"`
	UserEmail     string `json:"user_email"`
	CurrentRole   string `json:"current_role"`
	RequestedRole string `json:"requested_role"`
	Message       string `json:"message"`
	State         string `json:"state"`

	DecidedByLabel string     `json:"decided_by_label"`
	DecisionReason string     `json:"decision_reason"`
	DecidedAt      *time.Time `json:"decided_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func toAccessRequestDTO(r domain.AccessRequest) accessRequestDTO {
	return accessRequestDTO{
		ID: r.ID, TeamID: r.TeamID, UserID: r.UserID, UserEmail: r.UserEmail,
		CurrentRole: r.CurrentRole, RequestedRole: r.RequestedRole,
		Message: r.Message, State: r.State,
		DecidedByLabel: r.DecidedByLabel, DecisionReason: r.DecisionReason,
		DecidedAt: r.DecidedAt, CreatedAt: r.CreatedAt,
	}
}

// includeDecided reads the shared ?state= filter. Anything but the two known
// values is a 400 rather than a silent default: a typo that quietly returns the
// pending page would look exactly like an empty history.
func includeDecided(w http.ResponseWriter, r *http.Request) (bool, bool) {
	switch r.URL.Query().Get("state") {
	case "", "pending":
		return false, true
	case "all":
		return true, true
	default:
		writeError(w, http.StatusBadRequest, "state must be pending or all")
		return false, false
	}
}

// ─── Invitations (team-scoped) ──────────────────────────────────────────────

func (a *API) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if a.deps.Invites == nil {
		a.deps.Log.Error("invitations are not wired", "team_id", teamID)
		writeError(w, http.StatusInternalServerError, "invitations are not configured")
		return
	}
	actorRole, ok := a.teamRoleAtLeast(w, r, user, teamID, domain.RoleAdmin)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := a.deps.Invites.Create(r.Context(), teamID,
		access.CreateInput{Email: req.Email, Role: req.Role}, user, actorRole)
	if err != nil {
		a.writeAccessError(w, "creating invitation", err)
		return
	}
	// The address and the role, never the token: the audit log records what was
	// granted to whom, and the credential that grants it lives in one mailbox
	// (spec §6, audit-log.md §6).
	a.audit(r, audit.Entry{
		Action:   audit.ActionInviteCreated,
		Resource: audit.Resource(audit.ResourceTeamInvite, created.Invite.ID, created.Invite.Email),
		TeamID:   teamID,
		Detail: map[string]any{
			"email": created.Invite.Email, "role": created.Invite.Role,
			"mail_sent": created.MailSent,
		},
	})
	writeJSON(w, http.StatusCreated, createdInviteDTO{
		Invite:    toInviteDTO(created.Invite, time.Now()),
		AcceptURL: created.AcceptURL,
		MailSent:  created.MailSent,
	})
}

func (a *API) handleListInvites(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	// Authorization first, always: a listing that answers before the rank check
	// would tell a non-member that the team exists, even when the answer is an
	// empty array.
	if _, ok := a.teamRoleAtLeast(w, r, user, teamID, domain.RoleAdmin); !ok {
		return
	}
	if a.deps.Invites == nil {
		writeJSON(w, http.StatusOK, []teamInviteDTO{})
		return
	}
	decided, ok := includeDecided(w, r)
	if !ok {
		return
	}
	list, err := a.deps.Invites.List(r.Context(), teamID, decided)
	if err != nil {
		a.writeAccessError(w, "listing invitations", err)
		return
	}
	now := time.Now()
	out := make([]teamInviteDTO, 0, len(list))
	for _, inv := range list {
		out = append(out, toInviteDTO(inv, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if a.deps.Invites == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// Any team admin may revoke any of the team's invitations, including one
	// that would have granted owner: revoking is a REDUCTION, so it needs no
	// extra rank (spec §3).
	if _, ok := a.teamRoleAtLeast(w, r, user, teamID, domain.RoleAdmin); !ok {
		return
	}
	inv, err := a.deps.Invites.Revoke(r.Context(), teamID, r.PathValue("inv"))
	if err != nil {
		a.writeAccessError(w, "revoking invitation", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionInviteRevoked,
		Resource: audit.Resource(audit.ResourceTeamInvite, inv.ID, inv.Email),
		TeamID:   teamID,
		Detail:   map[string]any{"email": inv.Email, "role": inv.Role},
	})
	w.WriteHeader(http.StatusNoContent)
}

// ─── Invitations (public) ───────────────────────────────────────────────────

// handleGetInvite is the unauthenticated landing screen's read. Every failure of
// the token is 404 and costs the client address a strike; a failure of ours is
// logged and answered 500, and costs the caller nothing (spec §8).
func (a *API) handleGetInvite(w http.ResponseWriter, r *http.Request) {
	if a.deps.Invites == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	p, err := a.deps.Invites.Preview(r.Context(), r.PathValue("token"), a.clientIP(r))
	if err != nil {
		a.writeAccessError(w, "reading invitation", err)
		return
	}
	writeJSON(w, http.StatusOK, invitePreviewDTO{
		TeamName:      p.TeamName,
		InviterLabel:  p.InviterLabel,
		Email:         p.Email,
		Role:          p.Role,
		ExpiresAt:     p.ExpiresAt,
		AccountExists: p.AccountExists,
	})
}

// handleAcceptInvite spends an invitation and signs the invitee in. It is the
// panel's second unauthenticated MUTATING route, after the GitHub webhook, and
// like that one it is authenticated by a bearer secret rather than by a
// session.
func (a *API) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	if a.deps.Invites == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		TOTPCode    string `json:"totp_code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	accepted, err := a.deps.Invites.Accept(r.Context(), r.PathValue("token"), access.AcceptInput{
		Password:    req.Password,
		DisplayName: req.DisplayName,
		TOTPCode:    req.TOTPCode,
	}, a.clientIP(r))
	if err != nil {
		a.auditRefusedAccept(r, err)
		a.writeAccessError(w, "accepting invitation", err)
		return
	}
	// Attributed to the account that joined: there is no principal on a public
	// route, and the person who did this is the person who now exists as a
	// session (audit-log.md §4, the same shape first-run setup uses).
	a.audit(r, audit.Entry{
		Action:   audit.ActionInviteAccepted,
		Actor:    auditUserActor(accepted.User),
		Resource: audit.Resource(audit.ResourceTeamInvite, accepted.Invite.ID, accepted.Invite.Email),
		TeamID:   accepted.Invite.TeamID,
		Detail: map[string]any{
			"email": accepted.Invite.Email, "role": accepted.Invite.Role,
			"account_created": accepted.Created,
		},
	})
	status := http.StatusOK
	if accepted.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, loginResponse{
		Token: accepted.Token,
		User: userDTO{
			ID: accepted.User.ID, Email: accepted.User.Email, Role: accepted.User.Role,
			DisplayName: accepted.User.DisplayName, Timezone: accepted.User.Timezone,
		},
	})
}

// auditRefusedAccept records the refusals that took a VALID invitation to
// produce — a wrong password, a missing second factor — and nothing else.
//
// An unknown or expired token records no row on purpose: an unauthenticated
// caller must not be able to drive unbounded durable writes at their own
// request rate, which is the same reasoning that makes a throttled sign-in one
// row per episode rather than one per packet (audit-log.md §9).
func (a *API) auditRefusedAccept(r *http.Request, err error) {
	var reason string
	switch {
	case errors.Is(err, auth.ErrTOTPRequired):
		reason = "two-factor code required"
	case errors.Is(err, auth.ErrInvalidCredentials):
		reason = "invalid credentials"
	default:
		return
	}
	a.auditFailed(r, audit.Entry{
		Action:   audit.ActionInviteAccepted,
		Actor:    domain.AuditActor{Kind: domain.AuditActorAnonymous},
		Resource: audit.Resource(audit.ResourceTeamInvite, "", ""),
	}, reason)
}

// ─── Access requests ────────────────────────────────────────────────────────

func (a *API) handleCreateAccessRequest(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if a.deps.AccessRequests == nil {
		a.deps.Log.Error("access requests are not wired", "team_id", teamID)
		writeError(w, http.StatusInternalServerError, "access requests are not configured")
		return
	}
	// Member+: asking for access to a team you cannot see is not a supported
	// flow — it would make this collection a tenancy probe (spec §3).
	actorRole, ok := a.teamRoleAtLeast(w, r, user, teamID, domain.RoleMember)
	if !ok {
		return
	}
	var req struct {
		RequestedRole string `json:"requested_role"`
		Message       string `json:"message"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := a.deps.AccessRequests.Create(r.Context(), teamID, user, actorRole,
		access.RequestInput{RequestedRole: req.RequestedRole, Message: req.Message})
	if err != nil {
		a.writeAccessError(w, "creating access request", err)
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionAccessRequested,
		Resource: audit.Resource(audit.ResourceAccessRequest, created.ID, created.UserEmail),
		TeamID:   teamID,
		Detail: map[string]any{
			"requested_role": created.RequestedRole, "current_role": created.CurrentRole,
		},
	})
	writeJSON(w, http.StatusCreated, toAccessRequestDTO(created))
}

func (a *API) handleListAccessRequests(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	teamID := r.PathValue("id")
	if _, ok := a.teamRoleAtLeast(w, r, user, teamID, domain.RoleAdmin); !ok {
		return
	}
	if a.deps.AccessRequests == nil {
		writeJSON(w, http.StatusOK, []accessRequestDTO{})
		return
	}
	decided, ok := includeDecided(w, r)
	if !ok {
		return
	}
	list, err := a.deps.AccessRequests.List(r.Context(), teamID, decided)
	if err != nil {
		a.writeAccessError(w, "listing access requests", err)
		return
	}
	out := make([]accessRequestDTO, 0, len(list))
	for _, req := range list {
		out = append(out, toAccessRequestDTO(req))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGrantAccessRequest(w http.ResponseWriter, r *http.Request) {
	a.decideAccessRequest(w, r, true)
}

func (a *API) handleDenyAccessRequest(w http.ResponseWriter, r *http.Request) {
	a.decideAccessRequest(w, r, false)
}

// decideAccessRequest is the shared half of grant and deny: resolve the request
// to its team, require OWNER there, apply the verb, record it.
func (a *API) decideAccessRequest(w http.ResponseWriter, r *http.Request, grant bool) {
	user, _ := userFromContext(r.Context())
	if a.deps.AccessRequests == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	req, err := a.deps.AccessRequests.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeAccessError(w, "reading access request", err)
		return
	}
	actorRole, ok := a.teamRoleAtLeast(w, r, user, req.TeamID, domain.RoleOwner)
	if !ok {
		return
	}
	var reason string
	if !grant {
		var body struct {
			Reason string `json:"reason"`
		}
		// The body is optional: a denial with no reason is a denial. io.EOF is
		// "there was no body" — checked rather than trusting Content-Length,
		// which a chunked request does not set.
		if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		reason = body.Reason
	}

	var decided domain.AccessRequest
	action := audit.ActionAccessDenied
	if grant {
		action = audit.ActionAccessGranted
		decided, err = a.deps.AccessRequests.Grant(r.Context(), req.ID, user, actorRole)
	} else {
		decided, err = a.deps.AccessRequests.Deny(r.Context(), req.ID, reason, user)
	}
	if err != nil {
		a.writeAccessError(w, "deciding access request", err)
		return
	}
	detail := map[string]any{
		"requested_role": decided.RequestedRole,
		"requester":      decided.UserEmail,
		"member_user_id": decided.UserID,
	}
	if decided.DecisionReason != "" {
		detail["reason"] = decided.DecisionReason
	}
	a.audit(r, audit.Entry{
		Action:   action,
		Resource: audit.Resource(audit.ResourceAccessRequest, decided.ID, decided.UserEmail),
		TeamID:   decided.TeamID,
		Detail:   detail,
	})
	writeJSON(w, http.StatusOK, toAccessRequestDTO(decided))
}

// ─── error mapping ──────────────────────────────────────────────────────────

// writeAccessError maps this feature's errors — and the auth and teams errors
// it deliberately passes through — to statuses. The invitation sentinel is 404
// on purpose: unknown, wrong-secret, expired, revoked and spent must be one
// answer (spec §1).
func (a *API) writeAccessError(w http.ResponseWriter, op string, err error) {
	var (
		ve  *access.ValidationError
		tve *teams.ValidationError
		ave *auth.ValidationError
	)
	switch {
	case errors.Is(err, access.ErrInvalidInvite):
		writeError(w, http.StatusNotFound, "that invitation link is not valid — it may have been used already, revoked, or expired")
	case errors.Is(err, auth.ErrRateLimited):
		rateLimited(w, err, "too many attempts — wait before trying again")
	case errors.Is(err, auth.ErrTOTPRequired):
		// The password was correct; ask for the code, not the password again —
		// the same body the sign-in screen already knows how to read.
		writeJSON(w, http.StatusUnauthorized, errorBody{
			Error:        "two-factor authentication code required",
			TraceID:      w.Header().Get(TraceIDHeader),
			TOTPRequired: true,
		})
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "that is not the password for this account")
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.As(err, &tve):
		writeError(w, http.StatusBadRequest, tve.Msg)
	case errors.As(err, &ave):
		writeError(w, http.StatusBadRequest, ave.Error())
	case errors.Is(err, access.ErrForbidden), errors.Is(err, teams.ErrForbidden):
		writeError(w, http.StatusForbidden, "insufficient role")
	case errors.Is(err, access.ErrAlreadyMember):
		writeError(w, http.StatusConflict, "that address is already a member of this team")
	case errors.Is(err, access.ErrRequestOpen):
		writeError(w, http.StatusConflict, "a request from you is already open on this team")
	case errors.Is(err, access.ErrNotMember):
		writeError(w, http.StatusConflict, "the requester is no longer a member of this team")
	case errors.Is(err, access.ErrDecided):
		writeError(w, http.StatusConflict, "that request has already been decided")
	case errors.Is(err, teams.ErrLastOwner):
		writeError(w, http.StatusConflict, "a team must keep at least one owner")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
}
