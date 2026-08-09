package contentref

import (
	"errors"
	"path"
	"strings"

	"omnora/internal/storage"
)

type Source string

const (
	SourcePersonal      Source = "personal"
	SourceCommonMount   Source = "common_mount"
	SourceCollaboration Source = "collaboration"
)

type Locator struct {
	Source          Source `json:"source"`
	MountID         string `json:"mountId,omitempty"`
	CollaborationID string `json:"collaborationId,omitempty"`
	Path            string `json:"path"`
}

var (
	ErrInvalidLocator          = errors.New("invalid locator")
	ErrUnsupportedSource       = errors.New("unsupported source")
	ErrCollaborationNotAllowed = errors.New("collaboration not allowed")
)

func NormalizeForMemberSession(locator Locator) (Locator, error) {
	return normalize(locator, true)
}

func NormalizeForAutomation(locator Locator) (Locator, error) {
	return normalize(locator, false)
}

func normalize(locator Locator, allowCollaboration bool) (Locator, error) {
	locator.MountID = strings.TrimSpace(locator.MountID)
	locator.CollaborationID = strings.TrimSpace(locator.CollaborationID)

	valid := false
	switch locator.Source {
	case SourcePersonal:
		valid = locator.MountID == "" && locator.CollaborationID == ""
	case SourceCommonMount:
		valid = locator.MountID != "" && locator.CollaborationID == ""
	case SourceCollaboration:
		if !allowCollaboration {
			return Locator{}, ErrCollaborationNotAllowed
		}
		valid = locator.MountID == "" && locator.CollaborationID != ""
	default:
		return Locator{}, ErrUnsupportedSource
	}
	if !valid {
		return Locator{}, ErrInvalidLocator
	}

	if path.IsAbs(locator.Path) || strings.ContainsRune(locator.Path, '\\') || hasParentComponent(locator.Path) {
		return Locator{}, ErrInvalidLocator
	}
	cleanedPath, err := storage.CleanRelativePath(locator.Path)
	if err != nil {
		return Locator{}, ErrInvalidLocator
	}
	locator.Path = cleanedPath
	return locator, nil
}

func hasParentComponent(value string) bool {
	for component := range strings.SplitSeq(value, "/") {
		if component == ".." {
			return true
		}
	}
	return false
}
