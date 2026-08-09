package offlinemigration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLockRejectsSameProcessCompetitorAndReleases(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omnora.db")
	first, err := AcquireLock(dbPath)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}

	if _, err := AcquireLock(dbPath); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second acquire error = %v, want ErrLockHeld", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}

	second, err := AcquireLock(dbPath)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("release second lock: %v", err)
	}
}

func TestLockRejectsCompetingProcess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omnora.db")
	lock, err := AcquireLock(dbPath)
	if err != nil {
		t.Fatalf("acquire parent lock: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), "OMNORA_LOCK_HELPER=1", "OMNORA_LOCK_DB="+dbPath)
	if err := cmd.Run(); err == nil {
		t.Fatal("child acquired a lock already held by the parent")
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("release parent lock: %v", err)
	}

	cmd = exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), "OMNORA_LOCK_HELPER=1", "OMNORA_LOCK_DB="+dbPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child acquire after release: %v: %s", err, output)
	}
}

func TestLockHelperProcess(t *testing.T) {
	if os.Getenv("OMNORA_LOCK_HELPER") != "1" {
		return
	}
	lock, err := AcquireLock(os.Getenv("OMNORA_LOCK_DB"))
	if err != nil {
		os.Exit(21)
	}
	if err := lock.Close(); err != nil {
		os.Exit(22)
	}
}

func TestLockUsesDatabaseSidecarPathAndPrivateMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omnora.db")
	if got, want := DefaultLockPath(dbPath), dbPath+".offline.lock"; got != want {
		t.Fatalf("DefaultLockPath() = %q, want %q", got, want)
	}
	lock, err := AcquireLock(dbPath)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer lock.Close()
	info, err := os.Stat(DefaultLockPath(dbPath))
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("lock mode = %04o, want 0600", got)
	}
}
