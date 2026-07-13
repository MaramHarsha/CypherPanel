package auth

// Role is the MVP role model from plan.md Section 6. Post-MVP team sub-roles
// layer on top of RoleEndUser; they do not extend this enum.
type Role string

const (
	RoleRootAdmin Role = "root_admin"
	RoleReseller  Role = "reseller"
	RoleEndUser   Role = "end_user"
)

func (r Role) Valid() bool {
	switch r {
	case RoleRootAdmin, RoleReseller, RoleEndUser:
		return true
	}
	return false
}
