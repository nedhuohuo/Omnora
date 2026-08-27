package contentref

import (
	"errors"
	"reflect"
	"testing"
)

func TestLocatorJSONContract(t *testing.T) {
	type fieldContract struct {
		name string
		tag  string
	}

	want := []fieldContract{
		{name: "Source", tag: "source"},
		{name: "MountID", tag: "mountId,omitempty"},
		{name: "CollaborationID", tag: "collaborationId,omitempty"},
		{name: "Path", tag: "path"},
	}
	typeOfLocator := reflect.TypeOf(Locator{})
	if typeOfLocator.NumField() != len(want) {
		t.Fatalf("Locator has %d fields, want %d", typeOfLocator.NumField(), len(want))
	}
	for index, contract := range want {
		field := typeOfLocator.Field(index)
		if field.Name != contract.name || field.Tag.Get("json") != contract.tag {
			t.Fatalf("Locator field %d = %s %q, want %s %q", index, field.Name, field.Tag.Get("json"), contract.name, contract.tag)
		}
	}
}

func TestNormalizeForMemberSession(t *testing.T) {
	tests := []struct {
		name    string
		locator Locator
		want    Locator
		wantErr error
	}{
		{
			name:    "personal",
			locator: Locator{Source: SourcePersonal, Path: "docs//notes"},
			want:    Locator{Source: SourcePersonal, Path: "docs/notes"},
		},
		{
			name:    "common mount",
			locator: Locator{Source: SourceCommonMount, MountID: " mount-1 ", Path: "docs"},
			want:    Locator{Source: SourceCommonMount, MountID: "mount-1", Path: "docs"},
		},
		{
			name:    "collaboration",
			locator: Locator{Source: SourceCollaboration, CollaborationID: " collab-1 ", Path: "docs"},
			want:    Locator{Source: SourceCollaboration, CollaborationID: "collab-1", Path: "docs"},
		},
		{
			name:    "common requires mount",
			locator: Locator{Source: SourceCommonMount, Path: "docs"},
			wantErr: ErrInvalidLocator,
		},
		{
			name:    "personal rejects mount",
			locator: Locator{Source: SourcePersonal, MountID: "mount-1", Path: "."},
			wantErr: ErrInvalidLocator,
		},
		{
			name:    "collaboration rejects mount",
			locator: Locator{Source: SourceCollaboration, MountID: "mount-1", CollaborationID: "collab-1", Path: "."},
			wantErr: ErrInvalidLocator,
		},
		{
			name:    "unknown source",
			locator: Locator{Source: Source("archive"), Path: "."},
			wantErr: ErrUnsupportedSource,
		},
		{
			name:    "absolute path",
			locator: Locator{Source: SourcePersonal, Path: "/docs/notes"},
			wantErr: ErrInvalidLocator,
		},
		{
			name:    "backslash path",
			locator: Locator{Source: SourcePersonal, Path: `docs\notes`},
			wantErr: ErrInvalidLocator,
		},
		{
			name:    "internal traversal",
			locator: Locator{Source: SourcePersonal, Path: "docs/../notes"},
			wantErr: ErrInvalidLocator,
		},
		{
			name:    "empty path is root",
			locator: Locator{Source: SourcePersonal},
			want:    Locator{Source: SourcePersonal, Path: "."},
		},
		{
			name:    "reserved namespace",
			locator: Locator{Source: SourcePersonal, Path: ".omnora/trash/item"},
			wantErr: ErrInvalidLocator,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeForMemberSession(tt.locator)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NormalizeForMemberSession() error = %v, want %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("NormalizeForMemberSession() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNormalizeForAutomation(t *testing.T) {
	tests := []struct {
		name    string
		locator Locator
		want    Locator
		wantErr error
	}{
		{
			name:    "personal",
			locator: Locator{Source: SourcePersonal, Path: "docs//notes"},
			want:    Locator{Source: SourcePersonal, Path: "docs/notes"},
		},
		{
			name:    "common mount",
			locator: Locator{Source: SourceCommonMount, MountID: " mount-1 ", Path: "docs"},
			want:    Locator{Source: SourceCommonMount, MountID: "mount-1", Path: "docs"},
		},
		{
			name:    "collaboration is not allowed",
			locator: Locator{Source: SourceCollaboration, CollaborationID: "collab-1", Path: "."},
			wantErr: ErrCollaborationNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeForAutomation(tt.locator)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NormalizeForAutomation() error = %v, want %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("NormalizeForAutomation() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
