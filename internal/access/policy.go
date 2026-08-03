package access

import "omnora/internal/domain"

type RouteGroups map[domain.RouteGroup]bool

func DefaultRouteGroups() RouteGroups {
	groups := make(RouteGroups, len(domain.AllRouteGroups))
	for _, group := range domain.AllRouteGroups {
		groups[group] = false
	}
	return groups
}

func (g RouteGroups) Enabled(group domain.RouteGroup) bool {
	return g[group]
}

func (g RouteGroups) Set(group domain.RouteGroup, enabled bool) {
	g[group] = enabled
}

type Operation string

const (
	OperationRead        Operation = "read"
	OperationWrite       Operation = "write"
	OperationManageShare Operation = "manage_share"
	OperationManageACL   Operation = "manage_acl"
)

func Allows(permission domain.SpacePermission, mountMode domain.MountMode, operation Operation) bool {
	switch operation {
	case OperationRead:
		return permission == domain.SpacePermissionViewer ||
			permission == domain.SpacePermissionEditor ||
			permission == domain.SpacePermissionManager
	case OperationWrite:
		if mountMode != domain.MountModeReadWrite {
			return false
		}
		return permission == domain.SpacePermissionEditor ||
			permission == domain.SpacePermissionManager
	case OperationManageShare, OperationManageACL:
		return permission == domain.SpacePermissionManager
	default:
		return false
	}
}
