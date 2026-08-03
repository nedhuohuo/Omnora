package domain

type RouteGroup string

const (
	RouteGroupMemberWeb RouteGroup = "member_web"
	RouteGroupAdminWeb  RouteGroup = "admin_web"
	RouteGroupShare     RouteGroup = "share"
	RouteGroupREST      RouteGroup = "rest"
	RouteGroupMCP       RouteGroup = "mcp"
	RouteGroupOpenAPI   RouteGroup = "openapi"
)

var AllRouteGroups = []RouteGroup{
	RouteGroupMemberWeb,
	RouteGroupAdminWeb,
	RouteGroupShare,
	RouteGroupREST,
	RouteGroupMCP,
	RouteGroupOpenAPI,
}

func (g RouteGroup) Valid() bool {
	for _, known := range AllRouteGroups {
		if g == known {
			return true
		}
	}
	return false
}

type AccountRole string

const (
	AccountRoleAdmin  AccountRole = "admin"
	AccountRoleMember AccountRole = "member"
)

type SpacePermission string

const (
	SpacePermissionNone    SpacePermission = ""
	SpacePermissionViewer  SpacePermission = "viewer"
	SpacePermissionEditor  SpacePermission = "editor"
	SpacePermissionManager SpacePermission = "manager"
)

type MountMode string

const (
	MountModeReadOnly  MountMode = "read_only"
	MountModeReadWrite MountMode = "read_write"
)
