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

// AdminToggleableRouteGroups are the groups operators may expose from the
// admin console. Member Web and Admin Web stay deployment/env controlled and
// must not appear as runtime switches in the route-groups UI.
var AdminToggleableRouteGroups = []RouteGroup{
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

func (g RouteGroup) AdminToggleable() bool {
	for _, known := range AdminToggleableRouteGroups {
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

type ContentPermission string

const (
	ContentPermissionNone   ContentPermission = ""
	ContentPermissionViewer ContentPermission = "viewer"
	ContentPermissionEditor ContentPermission = "editor"
)

func (p ContentPermission) Valid() bool {
	return p == ContentPermissionViewer || p == ContentPermissionEditor
}

func (p ContentPermission) Allows(required ContentPermission) bool {
	if !p.Valid() || !required.Valid() {
		return false
	}
	return p == ContentPermissionEditor || required == ContentPermissionViewer
}

type MountMode string

const (
	MountModeReadOnly  MountMode = "read_only"
	MountModeReadWrite MountMode = "read_write"
)

type MountPurpose string

const (
	MountPurposePersonalDefault MountPurpose = "personal_default"
	MountPurposeCommon          MountPurpose = "common"
)

type StorageKind string

const (
	StorageKindManaged  StorageKind = "managed"
	StorageKindExternal StorageKind = "external"
)

type MountGovernance string

const (
	MountGovernanceSystem     MountGovernance = "system"
	MountGovernanceNormal     MountGovernance = "normal"
	MountGovernanceRestricted MountGovernance = "restricted"
)

func ValidMountClassification(purpose MountPurpose, storageKind StorageKind, governance MountGovernance) bool {
	return purpose == MountPurposePersonalDefault &&
		storageKind == StorageKindManaged &&
		governance == MountGovernanceSystem ||
		purpose == MountPurposeCommon &&
			storageKind == StorageKindExternal &&
			(governance == MountGovernanceNormal || governance == MountGovernanceRestricted)
}
