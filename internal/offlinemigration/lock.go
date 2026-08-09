package offlinemigration

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type Lock struct {
	mu   sync.Mutex
	file *os.File
}

func DefaultLockPath(dbPath string) string {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return ""
	}
	return dbPath + ".offline.lock"
}

// AcquireLock takes the non-blocking process-wide advisory lock shared by the
// normal server and the offline migration coordinator. The returned object
// must remain open for the entire protected operation.
func AcquireLock(dbPath string) (*Lock, error) {
	path := DefaultLockPath(dbPath)
	if path == "" {
		return nil, errors.New("offline migration database path is required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open offline migration lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect offline migration lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("acquire offline migration lock: %w", err)
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("release offline migration lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close offline migration lock: %w", closeErr)
	}
	return nil
}
