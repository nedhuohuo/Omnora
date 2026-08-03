package transfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnora/internal/storage"
)

func TestUploadSessionResumesAndCompletes(t *testing.T) {
	mountRoot := t.TempDir()
	tempRoot := filepath.Join(mountRoot, storage.ReservedNamespace, "tmp")
	mkdir(t, tempRoot)

	service := newTestService(t, mountRoot, tempRoot)
	session, err := service.CreateUploadSession(CreateUploadSessionRequest{
		TargetPath:   "docs.bin",
		ExpectedSize: int64(len("hello world")),
	})
	if err != nil {
		t.Fatalf("CreateUploadSession() error = %v", err)
	}

	if _, err := service.WritePart(session.ID, 2, strings.NewReader("world")); err != nil {
		t.Fatalf("WritePart(2) error = %v", err)
	}
	if _, err := service.WritePart(session.ID, 1, strings.NewReader("hello ")); err != nil {
		t.Fatalf("WritePart(1) error = %v", err)
	}

	resumedService := newTestService(t, mountRoot, tempRoot)
	resumed, err := resumedService.ResumeUploadSession(session.ID)
	if err != nil {
		t.Fatalf("ResumeUploadSession() error = %v", err)
	}
	if resumed.ReceivedSize != int64(len("hello world")) {
		t.Fatalf("ReceivedSize = %d, want %d", resumed.ReceivedSize, len("hello world"))
	}
	if len(resumed.Parts) != 2 || resumed.Parts[0].Number != 1 || resumed.Parts[1].Number != 2 {
		t.Fatalf("Parts = %#v, want sorted parts 1 and 2", resumed.Parts)
	}

	completed, err := resumedService.CompleteUpload(session.ID)
	if err != nil {
		t.Fatalf("CompleteUpload() error = %v", err)
	}
	if completed.TargetPath != "docs.bin" || completed.Size != int64(len("hello world")) {
		t.Fatalf("completed = %#v, want docs.bin size %d", completed, len("hello world"))
	}
	assertFileContents(t, filepath.Join(mountRoot, "docs.bin"), "hello world")

	if _, err := os.Stat(filepath.Join(tempRoot, session.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory exists after completion: %v", err)
	}
}

func TestUploadCanReplacePartWhileResuming(t *testing.T) {
	mountRoot := t.TempDir()
	tempRoot := filepath.Join(mountRoot, storage.ReservedNamespace, "tmp")
	mkdir(t, tempRoot)
	service := newTestService(t, mountRoot, tempRoot)

	session, err := service.CreateUploadSession(CreateUploadSessionRequest{
		TargetPath:   "replace.txt",
		ExpectedSize: 5,
	})
	if err != nil {
		t.Fatalf("CreateUploadSession() error = %v", err)
	}
	if _, err := service.WritePart(session.ID, 1, strings.NewReader("oops")); err != nil {
		t.Fatalf("WritePart(original) error = %v", err)
	}
	if _, err := service.WritePart(session.ID, 1, strings.NewReader("hello")); err != nil {
		t.Fatalf("WritePart(replacement) error = %v", err)
	}
	if _, err := service.CompleteUpload(session.ID); err != nil {
		t.Fatalf("CompleteUpload() error = %v", err)
	}
	assertFileContents(t, filepath.Join(mountRoot, "replace.txt"), "hello")
}

func TestUploadRejectsUnsafeTargetsAndOversizedParts(t *testing.T) {
	mountRoot := t.TempDir()
	tempRoot := filepath.Join(mountRoot, storage.ReservedNamespace, "tmp")
	mkdir(t, tempRoot)
	service := newTestService(t, mountRoot, tempRoot)

	for _, targetPath := range []string{
		"../outside.txt",
		"/absolute.txt",
		storage.ReservedNamespace + "/tmp/final.txt",
		".",
		"bad\nname.txt",
	} {
		t.Run(targetPath, func(t *testing.T) {
			_, err := service.CreateUploadSession(CreateUploadSessionRequest{
				TargetPath:   targetPath,
				ExpectedSize: 1,
			})
			if err == nil {
				t.Fatalf("CreateUploadSession(%q) error = nil, want error", targetPath)
			}
		})
	}

	session, err := service.CreateUploadSession(CreateUploadSessionRequest{
		TargetPath:   "small.txt",
		ExpectedSize: 3,
	})
	if err != nil {
		t.Fatalf("CreateUploadSession() error = %v", err)
	}
	_, err = service.WritePart(session.ID, 1, strings.NewReader("toolarge"))
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("WritePart() error = %v, want ErrUploadTooLarge", err)
	}
}

func TestUploadCancelCleansSession(t *testing.T) {
	mountRoot := t.TempDir()
	tempRoot := filepath.Join(mountRoot, storage.ReservedNamespace, "tmp")
	mkdir(t, tempRoot)
	service := newTestService(t, mountRoot, tempRoot)

	session, err := service.CreateUploadSession(CreateUploadSessionRequest{
		TargetPath:   "cancel.txt",
		ExpectedSize: 4,
	})
	if err != nil {
		t.Fatalf("CreateUploadSession() error = %v", err)
	}
	if _, err := service.WritePart(session.ID, 1, strings.NewReader("data")); err != nil {
		t.Fatalf("WritePart() error = %v", err)
	}
	if err := service.CancelUpload(session.ID); err != nil {
		t.Fatalf("CancelUpload() error = %v", err)
	}
	if _, err := service.ResumeUploadSession(session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ResumeUploadSession() error = %v, want ErrSessionNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(mountRoot, "cancel.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target exists after cancel: %v", err)
	}
}

func TestCompleteRejectsMissingParts(t *testing.T) {
	mountRoot := t.TempDir()
	tempRoot := filepath.Join(mountRoot, storage.ReservedNamespace, "tmp")
	mkdir(t, tempRoot)
	service := newTestService(t, mountRoot, tempRoot)

	session, err := service.CreateUploadSession(CreateUploadSessionRequest{
		TargetPath:   "gap.txt",
		ExpectedSize: 6,
	})
	if err != nil {
		t.Fatalf("CreateUploadSession() error = %v", err)
	}
	if _, err := service.WritePart(session.ID, 2, strings.NewReader("second")); err != nil {
		t.Fatalf("WritePart() error = %v", err)
	}
	if _, err := service.CompleteUpload(session.ID); !errors.Is(err, ErrIncompleteUpload) {
		t.Fatalf("CompleteUpload() error = %v, want ErrIncompleteUpload", err)
	}
}

func TestParseByteRange(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		size    int64
		want    ByteRange
		wantErr error
	}{
		{"explicit range", "bytes=2-5", 10, ByteRange{Start: 2, End: 5}, nil},
		{"open ended range", "bytes=6-", 10, ByteRange{Start: 6, End: 9}, nil},
		{"suffix range", "bytes=-4", 10, ByteRange{Start: 6, End: 9}, nil},
		{"clips end", "bytes=7-20", 10, ByteRange{Start: 7, End: 9}, nil},
		{"rejects multiple ranges", "bytes=0-1,3-4", 10, ByteRange{}, ErrInvalidRange},
		{"rejects inverted range", "bytes=5-2", 10, ByteRange{}, ErrInvalidRange},
		{"rejects unsatisfied range", "bytes=10-", 10, ByteRange{}, ErrUnsatisfiableRange},
		{"rejects zero sized file", "bytes=0-0", 0, ByteRange{}, ErrUnsatisfiableRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseByteRange(tt.header, tt.size)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseByteRange() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ParseByteRange() = %#v, want %#v", got, tt.want)
			}
			if err == nil && got.Length() != got.End-got.Start+1 {
				t.Fatalf("Length() = %d, want %d", got.Length(), got.End-got.Start+1)
			}
		})
	}
}

func TestDownloadMetadataETagUsesSizeAndMTime(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "download.bin")
	writeFile(t, filePath, "download")
	modTime := time.Date(2026, 8, 3, 10, 11, 12, 13, time.UTC)
	if err := os.Chtimes(filePath, modTime, modTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	metadata, err := StatDownloadMetadata(filePath)
	if err != nil {
		t.Fatalf("StatDownloadMetadata() error = %v", err)
	}
	if metadata.Size != int64(len("download")) {
		t.Fatalf("Size = %d, want %d", metadata.Size, len("download"))
	}
	if !metadata.ModTime.Equal(modTime) {
		t.Fatalf("ModTime = %s, want %s", metadata.ModTime, modTime)
	}
	wantETag := fmt.Sprintf("\"%x-%x\"", len("download"), modTime.UnixNano())
	if metadata.ETag != wantETag {
		t.Fatalf("ETag = %q, want %q", metadata.ETag, wantETag)
	}

	updatedPath := filepath.Join(root, "download-updated.bin")
	writeFile(t, updatedPath, "download!")
	updatedMetadata, err := StatDownloadMetadata(updatedPath)
	if err != nil {
		t.Fatalf("StatDownloadMetadata(updated) error = %v", err)
	}
	if metadata.ETag == updatedMetadata.ETag {
		t.Fatalf("ETag did not change when size changed: %q", metadata.ETag)
	}
}

func newTestService(t *testing.T, mountRoot, tempRoot string) *Service {
	t.Helper()
	service, err := NewService(Options{
		MountRoot:  mountRoot,
		TempRoot:   tempRoot,
		BufferSize: 7,
		Clock: func() time.Time {
			return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func writeFile(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
}

func assertFileContents(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", name, err)
	}
	if string(got) != want {
		t.Fatalf("ReadFile(%q) = %q, want %q", name, string(got), want)
	}
}

func mkdir(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(name, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", name, err)
	}
}
