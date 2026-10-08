package aitoken

import (
	"sort"
	"strings"

	"omnora/internal/domain"
	"omnora/internal/storage"
)

func normalizeSpaceGrants(grants []SpaceGrant) ([]SpaceGrant, error) {
	if len(grants) == 0 {
		return nil, ErrInvalidInput
	}
	seenSpaces := make(map[string]struct{}, len(grants))
	mountSpaces := make(map[string]string)
	normalized := make([]SpaceGrant, 0, len(grants))
	for _, grant := range grants {
		spaceID := strings.TrimSpace(grant.SpaceID)
		if spaceID == "" || !validGrantPermission(grant.Permission) {
			return nil, ErrInvalidInput
		}
		if _, exists := seenSpaces[spaceID]; exists {
			return nil, ErrInvalidInput
		}
		seenSpaces[spaceID] = struct{}{}

		var denies []FolderDeny
		for _, deny := range grant.FolderDenylist {
			mountID := strings.TrimSpace(deny.MountID)
			rawPath := strings.TrimSpace(deny.RelativePath)
			if mountID == "" || rawPath == "" || strings.Contains(rawPath, "\x00") {
				return nil, ErrInvalidInput
			}
			if otherSpace, exists := mountSpaces[mountID]; exists && otherSpace != spaceID {
				return nil, ErrInvalidInput
			}
			mountSpaces[mountID] = spaceID
			cleaned, err := storage.CleanRelativePath(rawPath)
			if err != nil || cleaned == "." {
				return nil, ErrInvalidInput
			}
			status := deny.Status
			if status == "" {
				status = FolderDenyStatusActive
			}
			if status != FolderDenyStatusActive && status != FolderDenyStatusNeedsReview {
				return nil, ErrInvalidInput
			}
			denies = append(denies, FolderDeny{
				MountID:             mountID,
				RelativePath:        cleaned,
				IdentityFingerprint: strings.TrimSpace(deny.IdentityFingerprint),
				Status:              status,
			})
		}

		sort.SliceStable(denies, func(i, j int) bool {
			if denies[i].MountID != denies[j].MountID {
				return denies[i].MountID < denies[j].MountID
			}
			leftDepth := strings.Count(denies[i].RelativePath, "/")
			rightDepth := strings.Count(denies[j].RelativePath, "/")
			if leftDepth != rightDepth {
				return leftDepth < rightDepth
			}
			return denies[i].RelativePath < denies[j].RelativePath
		})
		var collapsed []FolderDeny
		for _, candidate := range denies {
			covered := false
			for index := range collapsed {
				parent := &collapsed[index]
				if parent.MountID != candidate.MountID || !pathWithinDeny(candidate.RelativePath, parent.RelativePath) {
					continue
				}
				if parent.RelativePath == candidate.RelativePath {
					if parent.IdentityFingerprint != "" && candidate.IdentityFingerprint != "" && parent.IdentityFingerprint != candidate.IdentityFingerprint {
						return nil, ErrInvalidInput
					}
					if parent.IdentityFingerprint == "" {
						parent.IdentityFingerprint = candidate.IdentityFingerprint
					}
				}
				if candidate.Status == FolderDenyStatusNeedsReview {
					parent.Status = FolderDenyStatusNeedsReview
				}
				covered = true
				break
			}
			if !covered {
				collapsed = append(collapsed, candidate)
			}
		}
		normalized = append(normalized, SpaceGrant{
			SpaceID:        spaceID,
			Permission:     grant.Permission,
			FolderDenylist: collapsed,
		})
	}
	return normalized, nil
}

func validGrantPermission(permission domain.SpacePermission) bool {
	return permission == domain.SpacePermissionViewer || permission == domain.SpacePermissionEditor
}

func pathWithinDeny(relativePath, deniedPath string) bool {
	return relativePath == deniedPath || strings.HasPrefix(relativePath, deniedPath+"/")
}
