package recovery

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"omnora/internal/store"
)

// BackupPublisher creates a validated snapshot and publishes it without
// overwriting an existing backup. The database record should only be inserted
// by the caller after Publish returns successfully.
type BackupPublisher struct {
	db *store.DB
}

func NewBackupPublisher(db *store.DB) *BackupPublisher {
	return &BackupPublisher{db: db}
}

type BackupArtifact struct {
	ID   string
	Path string
}

// Publish writes a backup to a unique temporary file, validates SQLite
// integrity/foreign keys, fsyncs the file and directory, then atomically
// publishes it with a no-replace hard link. A failed publish never leaves a
// completed-looking final path.
func (p *BackupPublisher) Publish(ctx context.Context, directory string) (BackupArtifact, error) {
	if p == nil || p.db == nil {
		return BackupArtifact{}, errors.New("recovery: database is nil")
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return BackupArtifact{}, errors.New("recovery: backup directory is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return BackupArtifact{}, fmt.Errorf("create backup directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return BackupArtifact{}, fmt.Errorf("set backup directory mode: %w", err)
	}
	id, err := newBackupID()
	if err != nil {
		return BackupArtifact{}, err
	}
	tempPath := filepath.Join(directory, ".omnora-"+id+".tmp")
	finalPath := filepath.Join(directory, "omnora-"+id+".db")
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()

	if err := p.db.BackupTo(ctx, tempPath); err != nil {
		return BackupArtifact{}, fmt.Errorf("online backup: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return BackupArtifact{}, fmt.Errorf("set backup mode: %w", err)
	}
	if err := syncFile(tempPath); err != nil {
		return BackupArtifact{}, fmt.Errorf("sync backup: %w", err)
	}
	if err := ValidateSnapshot(ctx, tempPath); err != nil {
		return BackupArtifact{}, fmt.Errorf("verify backup: %w", err)
	}
	// Link is atomic and fails with EEXIST rather than replacing an existing
	// file. Both paths are in the same directory/filesystem by construction.
	if err := os.Link(tempPath, finalPath); err != nil {
		return BackupArtifact{}, fmt.Errorf("publish backup: %w", err)
	}
	if err := os.Remove(tempPath); err != nil {
		return BackupArtifact{}, fmt.Errorf("remove backup temporary: %w", err)
	}
	cleanup = false
	if err := syncDirectory(directory); err != nil {
		return BackupArtifact{}, fmt.Errorf("sync backup directory: %w", err)
	}
	return BackupArtifact{ID: id, Path: finalPath}, nil
}

// ValidateSnapshot opens a SQLite file read-only and confirms it passes an
// integrity check and a foreign key check. It is used both to accept a
// freshly published backup and, from cmd/omnora-recovery, to reject a
// corrupt snapshot before it is ever staged for restore.
func ValidateSnapshot(ctx context.Context, path string) error {
	return verifyBackup(ctx, path)
}

func verifyBackup(ctx context.Context, path string) error {
	readonly, err := store.OpenSQLiteReadonly(ctx, path)
	if err != nil {
		return err
	}
	defer readonly.Close()
	if err := readonly.IntegrityCheck(ctx); err != nil {
		return err
	}
	var result string
	if err := readonly.SQL().QueryRowContext(ctx, `PRAGMA foreign_key_check`).Scan(&result); err == nil {
		return fmt.Errorf("foreign key check failed: %s", result)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("foreign key check: %w", err)
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func newBackupID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
