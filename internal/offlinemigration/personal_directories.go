package offlinemigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func provisionTargetPersonalDirectories(ctx context.Context, dbPath, managedDir string) (func(), error) {
	location := &url.URL{Scheme: "file", Path: dbPath}
	db, err := sql.Open("sqlite", location.String()+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var accounts, directories int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&accounts); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM personal_directories`).Scan(&directories); err != nil {
		return nil, err
	}
	if accounts != directories {
		return nil, errors.New("personal-directory bindings are incomplete")
	}

	rows, err := db.QueryContext(ctx, `
SELECT account.id, account.status, directory.relative_path, directory.state
FROM accounts AS account
JOIN personal_directories AS directory ON directory.account_id = account.id
ORDER BY account.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type binding struct{ accountID, status, relativePath, state string }
	bindings := make([]binding, 0, directories)
	for rows.Next() {
		var item binding
		if err := rows.Scan(&item.accountID, &item.status, &item.relativePath, &item.state); err != nil {
			return nil, err
		}
		wantState := "ready"
		if item.status == "deleted" {
			wantState = "retained"
		}
		if item.relativePath != item.accountID || item.state != wantState || !safePersonalDirectoryComponent(item.accountID) {
			return nil, errors.New("personal-directory binding is invalid")
		}
		bindings = append(bindings, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	managedDir = filepath.Clean(strings.TrimSpace(managedDir))
	if !filepath.IsAbs(managedDir) {
		return nil, errors.New("managed directory must be absolute")
	}
	personalRoot := filepath.Join(managedDir, "personal")
	created := make([]string, 0, len(bindings)+1)
	rollback := func() {
		for index := len(created) - 1; index >= 0; index-- {
			_ = os.Remove(created[index])
		}
	}
	if err := ensureRealDirectoryOrCreate(personalRoot, &created); err != nil {
		return nil, err
	}
	for _, item := range bindings {
		path := filepath.Join(personalRoot, item.accountID)
		if filepath.Dir(path) != personalRoot {
			rollback()
			return nil, errors.New("personal-directory path escaped its root")
		}
		if err := ensureRealDirectoryOrCreate(path, &created); err != nil {
			rollback()
			return nil, err
		}
	}
	return rollback, nil
}

func ensureRealDirectoryOrCreate(path string, created *[]string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("personal storage path is not a real directory")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("create personal directory: %w", err)
	}
	*created = append(*created, path)
	return nil
}

func safePersonalDirectoryComponent(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 200 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
