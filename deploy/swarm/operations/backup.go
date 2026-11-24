package operations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CopyOfflineDirectory is deliberately not a live backup mechanism. The caller
// must first prove that the sole production writer has fully terminated.
func CopyOfflineDirectory(source, target string) error {
	left, leftErr := os.Lstat(source)
	right, rightErr := os.Lstat(target)
	if leftErr != nil || rightErr != nil || !left.IsDir() || !right.IsDir() || left.Mode()&os.ModeSymlink != 0 || right.Mode()&os.ModeSymlink != 0 {
		return errors.New("restore_requires_real_directories")
	}
	sourceReal, leftErr := filepath.EvalSymlinks(source)
	targetReal, rightErr := filepath.EvalSymlinks(target)
	if leftErr != nil || rightErr != nil || os.SameFile(left, right) || sourceReal == targetReal {
		return errors.New("restore_same_directory")
	}
	for _, pair := range [][2]string{{sourceReal, targetReal}, {targetReal, sourceReal}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("restore_directories_overlap")
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		return errors.New("restore_target_not_empty")
	}
	root, err := os.Lstat(source)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid_backup_source")
	}
	entries, err = os.ReadDir(source)
	if err != nil {
		return errors.New("backup_read")
	}
	for _, entry := range entries {
		path := filepath.Join(source, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("unsafe_backup_entry")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return errors.New("unsafe_backup_entry")
		}
		input, err := os.Open(path)
		if err != nil {
			return errors.New("backup_read")
		}
		output, err := os.OpenFile(filepath.Join(target, entry.Name()), os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			input.Close()
			return errors.New("backup_write")
		}
		_, copyErr := io.Copy(output, input)
		input.Close()
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil {
			return errors.New("backup_write")
		}
		if err := os.Chmod(filepath.Join(target, entry.Name()), info.Mode().Perm()); err != nil {
			return errors.New("backup_mode")
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(filepath.Join(target, entry.Name()), int(stat.Uid), int(stat.Gid)); err != nil {
				return errors.New("backup_owner")
			}
		}
	}
	if err := os.Chmod(target, root.Mode().Perm()); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		stat := root.Sys().(*syscall.Stat_t)
		return os.Chown(target, int(stat.Uid), int(stat.Gid))
	}
	return nil
}
func DirectoryHashes(path string) (map[string][32]byte, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string][32]byte)
	for _, e := range entries {
		info, err := os.Lstat(filepath.Join(path, e.Name()))
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("unsafe_backup_entry")
		}
		data, err := os.ReadFile(filepath.Join(path, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = sha256.Sum256(data)
	}
	return out, nil
}
func CheckOfflineDatabase(path string, wantVersion int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return errors.New("database_open")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("database_integrity")
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return errors.New("database_foreign_keys")
	}
	has := rows.Next()
	rowErr := rows.Err()
	rows.Close()
	if has || rowErr != nil {
		return errors.New("database_foreign_keys")
	}
	var version int64
	var dirty bool
	if err := db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || dirty || version != wantVersion {
		return errors.New("database_migration")
	}
	return nil
}
