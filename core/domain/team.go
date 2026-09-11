package domain

import "time"

// Role vocabulary shared by team memberships (team_members.role) and the
// panel-level users.role column (teams-and-roles.md §1). Ranked: comparison is
// by RoleRank, never string ordering.
const (
	RoleMember = "member"
	RoleAdmin  = "admin"
	RoleOwner  = "owner"
)

// RoleRank orders the closed role set for minimum-rank checks. Unknown strings
// rank 0 — below member — so a corrupt or empty role never grants access.
func RoleRank(role string) int {
	switch role {
	case RoleMember:
		return 1
	case RoleAdmin:
		return 2
	case RoleOwner:
		return 3
	default:
		return 0
	}
}

// ValidRole reports whether role is one of the closed set.
func ValidRole(role string) bool { return RoleRank(role) > 0 }

// CanGrantRole reports whether an actor holding actorRole may hand out — or
// take away — subjectRole. Acting on the owner rank requires owner; anything
// else requires admin (teams-and-roles.md §5: no self-service escalation).
//
// It lives here, in the package that owns the role vocabulary, because two
// features now depend on exactly this comparison: adding or re-ranking a member
// (core/teams) and issuing an invitation that will do the same later
// (core/access, invitations-and-access-requests.md §1). One implementation, so
// the two cannot drift into different answers about who may mint an owner.
func CanGrantRole(actorRole, subjectRole string) bool {
	need := RoleAdmin
	if subjectRole == RoleOwner {
		need = RoleOwner
	}
	return RoleRank(actorRole) >= RoleRank(need)
}

// Team is the tenancy boundary (glossary): owns projects; users belong to it
// with a ranked role.
type Team struct {
	ID        string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TeamMember is one user's membership in one team.
type TeamMember struct {
	TeamID    string
	UserID    string
	Email     string // joined for listings; empty when not loaded
	Role      string
	CreatedAt time.Time
}

// TeamWithRole pairs a team with the requesting user's role in it (the GET
// /teams listing shape).
type TeamWithRole struct {
	Team
	Role string
}
