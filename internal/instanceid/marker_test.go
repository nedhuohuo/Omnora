package instanceid

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const (
	testInstanceID      = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherTestInstanceID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func TestValidateMarkersReturnsSharedInstanceID(t *testing.T) {
	paths := newValidMarkerPaths(t)
	if err := os.WriteFile(paths.Data, []byte(testInstanceID), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ValidateMarkers(paths)
	if err != nil {
		t.Fatalf("ValidateMarkers() error = %v", err)
	}
	if got != testInstanceID {
		t.Fatalf("ValidateMarkers() = %q, want %q", got, testInstanceID)
	}
}

func TestOpenMarkerRejectsSymbolicLinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "marker")
	writeMarker(t, target, testInstanceID+"\n")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	file, err := openMarker(link)
	if err == nil {
		file.Close()
		t.Fatal("openMarker() followed a symbolic link")
	}
}

func TestValidateMarkersRejectsInvalidMarkers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, paths MarkerPaths)
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				if err := os.Remove(paths.Managed); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "empty",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				writeMarker(t, paths.Managed, "")
			},
		},
		{
			name: "not 64 characters",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				writeMarker(t, paths.Managed, strings.Repeat("a", 63)+"\n")
			},
		},
		{
			name: "uppercase hex",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				writeMarker(t, paths.Managed, strings.ToUpper(testInstanceID)+"\n")
			},
		},
		{
			name: "two trailing newlines",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				writeMarker(t, paths.Managed, testInstanceID+"\n\n")
			},
		},
		{
			name: "mismatch",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				writeMarker(t, paths.Managed, otherTestInstanceID+"\n")
			},
		},
		{
			name: "directory",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				if err := os.Remove(paths.Managed); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(paths.Managed, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fifo",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				if err := os.Remove(paths.Managed); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(paths.Managed, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symbolic link",
			mutate: func(t *testing.T, paths MarkerPaths) {
				t.Helper()
				if err := os.Remove(paths.Managed); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(paths.Data, paths.Managed); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := newValidMarkerPaths(t)
			tt.mutate(t, paths)

			got, err := ValidateMarkers(paths)
			if err == nil {
				t.Fatalf("ValidateMarkers() = %q, want error", got)
			}
			for _, forbidden := range []string{testInstanceID, otherTestInstanceID, filepath.Dir(paths.Config)} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("ValidateMarkers() error leaked marker content or path: %v", err)
				}
			}
		})
	}
}

func newValidMarkerPaths(t *testing.T) MarkerPaths {
	t.Helper()
	root := t.TempDir()
	paths := MarkerPaths{
		Config:  filepath.Join(root, "config.marker"),
		Data:    filepath.Join(root, "data.marker"),
		Managed: filepath.Join(root, "managed.marker"),
	}
	for _, path := range []string{paths.Config, paths.Data, paths.Managed} {
		writeMarker(t, path, testInstanceID+"\n")
	}
	return paths
}

func writeMarker(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
