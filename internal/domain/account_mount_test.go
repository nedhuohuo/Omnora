package domain

import "testing"

func TestAccountMountDomainValues(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "content permission none", got: string(ContentPermissionNone), want: ""},
		{name: "content permission viewer", got: string(ContentPermissionViewer), want: "viewer"},
		{name: "content permission editor", got: string(ContentPermissionEditor), want: "editor"},
		{name: "mount purpose personal default", got: string(MountPurposePersonalDefault), want: "personal_default"},
		{name: "mount purpose common", got: string(MountPurposeCommon), want: "common"},
		{name: "storage kind managed", got: string(StorageKindManaged), want: "managed"},
		{name: "storage kind external", got: string(StorageKindExternal), want: "external"},
		{name: "mount governance system", got: string(MountGovernanceSystem), want: "system"},
		{name: "mount governance normal", got: string(MountGovernanceNormal), want: "normal"},
		{name: "mount governance restricted", got: string(MountGovernanceRestricted), want: "restricted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("value = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestContentPermissionValid(t *testing.T) {
	tests := []struct {
		name       string
		permission ContentPermission
		want       bool
	}{
		{name: "none", permission: ContentPermissionNone, want: false},
		{name: "viewer", permission: ContentPermissionViewer, want: true},
		{name: "editor", permission: ContentPermissionEditor, want: true},
		{name: "manager", permission: ContentPermission("manager"), want: false},
		{name: "unknown", permission: ContentPermission("unknown"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.permission.Valid(); got != tt.want {
				t.Fatalf("ContentPermission(%q).Valid() = %v, want %v", tt.permission, got, tt.want)
			}
		})
	}
}

func TestContentPermissionAllows(t *testing.T) {
	permissions := []ContentPermission{
		ContentPermissionNone,
		ContentPermissionViewer,
		ContentPermissionEditor,
		ContentPermission("manager"),
		ContentPermission("unknown"),
	}
	allowed := map[[2]ContentPermission]bool{
		{ContentPermissionViewer, ContentPermissionViewer}: true,
		{ContentPermissionEditor, ContentPermissionViewer}: true,
		{ContentPermissionEditor, ContentPermissionEditor}: true,
	}

	for _, have := range permissions {
		for _, required := range permissions {
			want := allowed[[2]ContentPermission{have, required}]
			if got := have.Allows(required); got != want {
				t.Errorf("ContentPermission(%q).Allows(%q) = %v, want %v", have, required, got, want)
			}
		}
	}
}

func TestValidMountClassification(t *testing.T) {
	purposes := []MountPurpose{
		MountPurposePersonalDefault,
		MountPurposeCommon,
		MountPurpose("unknown"),
	}
	storageKinds := []StorageKind{
		StorageKindManaged,
		StorageKindExternal,
		StorageKind("unknown"),
	}
	governanceModes := []MountGovernance{
		MountGovernanceSystem,
		MountGovernanceNormal,
		MountGovernanceRestricted,
		MountGovernance("unknown"),
	}
	valid := map[[3]string]bool{
		{string(MountPurposePersonalDefault), string(StorageKindManaged), string(MountGovernanceSystem)}: true,
		{string(MountPurposeCommon), string(StorageKindExternal), string(MountGovernanceNormal)}:         true,
		{string(MountPurposeCommon), string(StorageKindExternal), string(MountGovernanceRestricted)}:     true,
	}

	for _, purpose := range purposes {
		for _, storageKind := range storageKinds {
			for _, governance := range governanceModes {
				want := valid[[3]string{string(purpose), string(storageKind), string(governance)}]
				if got := ValidMountClassification(purpose, storageKind, governance); got != want {
					t.Errorf("ValidMountClassification(%q, %q, %q) = %v, want %v", purpose, storageKind, governance, got, want)
				}
			}
		}
	}
}
