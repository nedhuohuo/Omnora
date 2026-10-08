package aitoken

import (
	"errors"
	"reflect"
	"testing"

	"omnora/internal/domain"
)

func TestNormalizeSpaceGrantsCleansAndCollapsesFolderDenies(t *testing.T) {
	grants, err := normalizeSpaceGrants([]SpaceGrant{
		{
			SpaceID:    " space-editor ",
			Permission: domain.SpacePermissionEditor,
			FolderDenylist: []FolderDeny{
				{MountID: " mount-a ", RelativePath: "private/./docs", IdentityFingerprint: "id-parent"},
				{MountID: "mount-a", RelativePath: "private/docs/nested", Status: FolderDenyStatusNeedsReview},
				{MountID: "mount-a", RelativePath: "private2", IdentityFingerprint: "id-sibling"},
				{MountID: "mount-a", RelativePath: "private/docs"},
			},
		},
		{SpaceID: "space-viewer", Permission: domain.SpacePermissionViewer},
	})
	if err != nil {
		t.Fatalf("normalizeSpaceGrants() error = %v", err)
	}
	want := []SpaceGrant{
		{
			SpaceID:    "space-editor",
			Permission: domain.SpacePermissionEditor,
			FolderDenylist: []FolderDeny{
				{MountID: "mount-a", RelativePath: "private2", IdentityFingerprint: "id-sibling", Status: FolderDenyStatusActive},
				{MountID: "mount-a", RelativePath: "private/docs", IdentityFingerprint: "id-parent", Status: FolderDenyStatusNeedsReview},
			},
		},
		{SpaceID: "space-viewer", Permission: domain.SpacePermissionViewer},
	}
	if !reflect.DeepEqual(grants, want) {
		t.Fatalf("normalizeSpaceGrants() = %#v, want %#v", grants, want)
	}
}

func TestNormalizeSpaceGrantsRejectsInvalidPermissionsRootsAndMountSpaceMismatch(t *testing.T) {
	tests := []struct {
		name   string
		grants []SpaceGrant
	}{
		{name: "no grants"},
		{name: "manager permission", grants: []SpaceGrant{{SpaceID: "space-a", Permission: domain.SpacePermissionManager}}},
		{name: "duplicate space", grants: []SpaceGrant{{SpaceID: "space-a", Permission: domain.SpacePermissionViewer}, {SpaceID: "space-a", Permission: domain.SpacePermissionEditor}}},
		{name: "empty deny path", grants: []SpaceGrant{{SpaceID: "space-a", Permission: domain.SpacePermissionViewer, FolderDenylist: []FolderDeny{{MountID: "mount-a"}}}}},
		{name: "root deny path", grants: []SpaceGrant{{SpaceID: "space-a", Permission: domain.SpacePermissionViewer, FolderDenylist: []FolderDeny{{MountID: "mount-a", RelativePath: "."}}}}},
		{name: "traversal deny path", grants: []SpaceGrant{{SpaceID: "space-a", Permission: domain.SpacePermissionViewer, FolderDenylist: []FolderDeny{{MountID: "mount-a", RelativePath: "../secret"}}}}},
		{name: "unknown status", grants: []SpaceGrant{{SpaceID: "space-a", Permission: domain.SpacePermissionViewer, FolderDenylist: []FolderDeny{{MountID: "mount-a", RelativePath: "secret", Status: FolderDenyStatus("unknown")}}}}},
		{name: "mount assigned to two spaces", grants: []SpaceGrant{
			{SpaceID: "space-a", Permission: domain.SpacePermissionViewer, FolderDenylist: []FolderDeny{{MountID: "mount-a", RelativePath: "secret"}}},
			{SpaceID: "space-b", Permission: domain.SpacePermissionViewer, FolderDenylist: []FolderDeny{{MountID: "mount-a", RelativePath: "other"}}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeSpaceGrants(tc.grants); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeSpaceGrants() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestPathWithinDenyUsesSegmentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		path string
		deny string
		want bool
	}{
		{path: "private", deny: "private", want: true},
		{path: "private/docs", deny: "private", want: true},
		{path: "private2", deny: "private", want: false},
		{path: "public/private", deny: "private", want: false},
	} {
		if got := pathWithinDeny(tc.path, tc.deny); got != tc.want {
			t.Fatalf("pathWithinDeny(%q, %q) = %v, want %v", tc.path, tc.deny, got, tc.want)
		}
	}
}
