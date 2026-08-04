package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"modernc.org/sqlite"
)

// BackupTo creates a consistent online backup of the open database into destPath
// using the SQLite Online Backup API.
func (db *DB) BackupTo(ctx context.Context, destPath string) error {
	destPath = strings.TrimSpace(destPath)
	if destPath == "" {
		return fmt.Errorf("backup destination is required")
	}
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("sqlite driver does not support Online Backup API")
		}
		handle, err := backuper.NewBackup(destPath)
		if err != nil {
			return err
		}
		return runBackupSteps(handle)
	})
}

// RestoreFrom replaces the contents of the open database with the snapshot at
// sourcePath using the SQLite Online Restore API.
func (db *DB) RestoreFrom(ctx context.Context, sourcePath string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return fmt.Errorf("restore source is required")
	}
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		restorer, ok := driverConn.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("sqlite driver does not support Online Restore API")
		}
		handle, err := restorer.NewRestore(sourcePath)
		if err != nil {
			return err
		}
		return runBackupSteps(handle)
	})
}

func runBackupSteps(handle *sqlite.Backup) error {
	for {
		more, err := handle.Step(-1)
		if err != nil {
			_ = handle.Finish()
			return err
		}
		if !more {
			break
		}
	}
	return handle.Finish()
}

// IntegrityCheck runs PRAGMA integrity_check and returns an error when the
// database reports anything other than "ok".
func (db *DB) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := db.sql.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(result), "ok") {
		return fmt.Errorf("integrity check failed: %s", result)
	}
	return nil
}

// OpenSQLiteReadonly opens a temporary read-only connection used to validate a
// backup file before restore.
func OpenSQLiteReadonly(ctx context.Context, path string) (*DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	dsn := fmt.Sprintf("file:%s?mode=ro", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	wrapped := &DB{sql: sqlDB}
	if err := wrapped.Ping(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return wrapped, nil
}
