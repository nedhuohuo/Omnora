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
