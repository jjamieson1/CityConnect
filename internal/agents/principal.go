package agents

import (
	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
)

// Permission names the capabilities the API checks. Roles map onto sets of
// these so a handler asks "can this caller do X?" rather than reimplementing
// role arithmetic.
type Permission string

// Permissions.
const (
	PermRequestRead     Permission = "request:read"
	PermRequestWrite    Permission = "request:write"
	PermRequestAssign   Permission = "request:assign"
	PermRequestTransfer Permission = "request:transfer"
	PermContactRead     Permission = "contact:read"
	PermContactWrite    Permission = "contact:write"
	PermContactMerge    Permission = "contact:merge"
	PermNotifySend      Permission = "notification:send"
	PermReportRead      Permission = "report:read"
	PermConfigRead      Permission = "config:read"
	PermConfigWrite     Permission = "config:write"
	PermUserManage      Permission = "user:manage"
	PermAuditRead       Permission = "audit:read"
	PermSystemManage    Permission = "system:manage"
)

// rolePermissions is the authority table. Higher roles inherit everything
// below them, so each entry lists only what that role adds.
var rolePermissions = map[domain.Role][]Permission{
	domain.RoleReadOnly: {
		PermRequestRead, PermContactRead, PermReportRead, PermConfigRead,
	},
	domain.RoleAgent: {
		PermRequestWrite, PermRequestAssign, PermContactWrite, PermNotifySend,
	},
	domain.RoleSupervisor: {
		PermRequestTransfer, PermContactMerge, PermAuditRead,
	},
	domain.RoleAdmin: {
		PermConfigWrite, PermUserManage, PermSystemManage,
	},
}

// roleOrder is the inheritance chain.
var roleOrder = []domain.Role{
	domain.RoleReadOnly, domain.RoleAgent, domain.RoleSupervisor, domain.RoleAdmin,
}

// permissionsFor expands a role into its full permission set.
func permissionsFor(role domain.Role) map[Permission]bool {
	out := map[Permission]bool{}
	for _, r := range roleOrder {
		for _, p := range rolePermissions[r] {
			out[p] = true
		}
		if r == role {
			break
		}
	}
	return out
}

// Principal is the authenticated caller: a staff user with a session, or a
// machine client holding a personal access token.
type Principal struct {
	User   *domain.User
	System *domain.ConnectedSystem
	Token  *domain.ApiToken

	perms map[Permission]bool
	IP    string
}

// NewUserPrincipal builds a principal for a session-authenticated staff user.
func NewUserPrincipal(u *domain.User, ip string) *Principal {
	return &Principal{User: u, perms: permissionsFor(u.Role), IP: ip}
}

// NewTokenPrincipal builds a principal for a personal access token.
//
// A token can only narrow its owner's authority, never widen it: the
// permission set starts from the owner's role, then gets clipped by the
// token's scopes and read-only flag. A token issued to a connected system with
// no owner gets an explicit, minimal set.
func NewTokenPrincipal(tp *TokenPrincipal, ip string) *Principal {
	p := &Principal{User: tp.User, System: tp.System, Token: tp.Token, IP: ip}

	switch {
	case tp.User != nil:
		p.perms = permissionsFor(tp.User.Role)
	default:
		p.perms = map[Permission]bool{
			PermRequestRead: true, PermContactRead: true,
		}
	}

	// Scopes clip the set. An empty scope list means the token was issued
	// without any, which grants nothing rather than everything.
	scoped := map[Permission]bool{}
	for perm := range p.perms {
		if tokenGrants(tp, perm) {
			scoped[perm] = true
		}
	}
	p.perms = scoped

	if tp.Token != nil && tp.Token.ReadOnly {
		for perm := range p.perms {
			if isWrite(perm) {
				delete(p.perms, perm)
			}
		}
	}
	return p
}

func tokenGrants(tp *TokenPrincipal, perm Permission) bool {
	if tp.HasScope(ScopeAll) {
		return true
	}
	switch perm {
	case PermRequestRead:
		return tp.HasScope(ScopeRequestsRead) || tp.HasScope(ScopeRequestsWrite)
	case PermRequestWrite, PermRequestAssign, PermRequestTransfer:
		return tp.HasScope(ScopeRequestsWrite)
	case PermContactRead:
		return tp.HasScope(ScopeContactsRead) || tp.HasScope(ScopeContactsWrite)
	case PermContactWrite, PermContactMerge:
		return tp.HasScope(ScopeContactsWrite)
	case PermNotifySend:
		return tp.HasScope(ScopeNotifySend)
	case PermReportRead:
		return tp.HasScope(ScopeReportsRead)
	}
	return false
}

func isWrite(p Permission) bool {
	switch p {
	case PermRequestWrite, PermRequestAssign, PermRequestTransfer,
		PermContactWrite, PermContactMerge, PermNotifySend,
		PermConfigWrite, PermUserManage, PermSystemManage:
		return true
	}
	return false
}

// Can reports whether the principal holds a permission.
func (p *Principal) Can(perm Permission) bool {
	return p != nil && p.perms[perm]
}

// ID returns a stable identifier for the caller.
func (p *Principal) ID() string {
	switch {
	case p == nil:
		return ""
	case p.User != nil:
		return p.User.ID
	case p.System != nil:
		return p.System.ID
	}
	return ""
}

// Label returns a human-readable caller name for audit entries.
func (p *Principal) Label() string {
	switch {
	case p == nil:
		return "anonymous"
	case p.User != nil && p.User.Name != "":
		return p.User.Name
	case p.User != nil:
		return p.User.Email
	case p.System != nil:
		return p.System.Name
	}
	return "unknown"
}

// Actor converts the principal into an audit actor.
func (p *Principal) Actor() audit.Actor {
	if p == nil {
		return audit.Actor{Type: audit.ActorSystem, Label: "anonymous"}
	}
	if p.System != nil && p.User == nil {
		return audit.SystemActor(p.System.ID, p.System.Name, p.IP)
	}
	return audit.UserActor(p.ID(), p.Label(), p.IP)
}

// DepartmentID is the caller's home department, or "" for a caller with none.
func (p *Principal) DepartmentID() string {
	switch {
	case p == nil:
		return ""
	case p.User != nil:
		return p.User.DepartmentID
	case p.System != nil:
		return p.System.DepartmentID
	}
	return ""
}

// CanCrossDepartment reports whether the caller may act outside their own
// department.
//
// Departments are a soft boundary, not a tenant: read access is transparent
// across the city so an agent taking a call can see that Public Works already
// has the pothole logged. Write access is what the boundary constrains.
func (p *Principal) CanCrossDepartment() bool {
	if p == nil {
		return false
	}
	if p.User != nil {
		return p.User.CanCrossDepartment()
	}
	// A connected system acts only within the department it belongs to.
	return p.System != nil && p.System.DepartmentID == ""
}

// CanWriteDepartment reports whether the caller may modify records owned by a
// department.
func (p *Principal) CanWriteDepartment(departmentID string) bool {
	switch {
	case p == nil:
		return false
	case p.CanCrossDepartment():
		return true
	case departmentID == "":
		return true
	}
	return p.DepartmentID() == departmentID
}

// IsSystem reports whether the caller is a connected system rather than a
// person.
func (p *Principal) IsSystem() bool {
	return p != nil && p.System != nil && p.User == nil
}
