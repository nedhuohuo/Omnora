package offlinemigration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"omnora/internal/recovery"
)

const bundleSpaceReserve = uint64(1024 * 1024)

var instanceIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type bundleTestHooks struct {
	availableBytes func(string) (uint64, error)
	afterCopy      func(string) error
	syncFile       func(string) error
	syncDir        func(string) error
	rename         func(string, string) error
}

type sourceEntry struct {
	rel    string
	info   fs.FileInfo
	digest string
}

type sourceTree struct {
	label   string
	root    string
	entries map[string]sourceEntry
	bytes   uint64
}

// BuildBundle creates a complete, independently verifiable rollback bundle.
// A directory is usable only when ROLLBACK_READY exists; every failure path
// deliberately leaves that marker absent.
func BuildBundle(ctx context.Context, request BundleRequest) (BundleResult, error) {
	request = trimBundleRequest(request)
	if err := validateBundleRequest(request); err != nil {
		return BundleResult{}, err
	}
	hooks := defaultBundleHooks()
	if request.testHooks != nil {
		hooks = mergeBundleHooks(hooks, *request.testHooks)
	}

	sources, err := captureSources(request)
	if err != nil {
		return BundleResult{}, err
	}
	var required uint64
	for _, source := range sources {
		required += source.bytes
	}
	if required > ^uint64(0)-bundleSpaceReserve {
		return BundleResult{}, errors.New("rollback bundle size overflow")
	}
	if err := os.MkdirAll(request.RollbackRoot, 0o700); err != nil {
		return BundleResult{}, fmt.Errorf("create rollback root: %w", err)
	}
	if err := requireRealDirectory(request.RollbackRoot); err != nil {
		return BundleResult{}, fmt.Errorf("validate rollback root: %w", err)
	}
	if err := os.Chmod(request.RollbackRoot, 0o700); err != nil {
		return BundleResult{}, fmt.Errorf("secure rollback root: %w", err)
	}
	available, err := hooks.availableBytes(request.RollbackRoot)
	if err != nil {
		return BundleResult{}, fmt.Errorf("measure rollback free space: %w", err)
	}
	if available < required+bundleSpaceReserve {
		return BundleResult{}, fmt.Errorf("insufficient rollback space: need at least %d bytes, have %d", required+bundleSpaceReserve, available)
	}

	id, err := newBundleID()
	if err != nil {
		return BundleResult{}, err
	}
	tempPath := filepath.Join(request.RollbackRoot, ".bundle-"+id+".incomplete")
	finalPath := filepath.Join(request.RollbackRoot, "bundle-"+id)
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return BundleResult{}, fmt.Errorf("rollback bundle destination already exists: %s", finalPath)
		}
		return BundleResult{}, fmt.Errorf("inspect rollback bundle destination: %w", err)
	}
	if err := os.Mkdir(tempPath, 0o700); err != nil {
		return BundleResult{}, fmt.Errorf("create incomplete rollback bundle: %w", err)
	}

	manifest := BundleManifest{
		FormatVersion:      1,
		BundleID:           id,
		CreatedAt:          time.Now().UTC(),
		InstanceID:         request.InstanceID,
		SourceMigration:    request.SourceMigration,
		TargetMigration:    request.TargetMigration,
		ExternalIdentities: append([]ExternalIdentity(nil), request.ExternalIdentities...),
	}
	addDirectoryEntry(&manifest, "database")
	if err := os.Mkdir(filepath.Join(tempPath, "database"), 0o700); err != nil {
		return BundleResult{}, fmt.Errorf("create database bundle directory: %w", err)
	}
	snapshotRel := filepath.ToSlash(filepath.Join("database", "omnora.db"))
	snapshotPath := filepath.Join(tempPath, filepath.FromSlash(snapshotRel))
	if err := request.DB.BackupTo(ctx, snapshotPath); err != nil {
		return BundleResult{}, fmt.Errorf("create SQLite rollback snapshot: %w", err)
	}
	if err := os.Chmod(snapshotPath, 0o600); err != nil {
		return BundleResult{}, fmt.Errorf("secure SQLite rollback snapshot: %w", err)
	}
	if err := hooks.syncFile(snapshotPath); err != nil {
		return BundleResult{}, fmt.Errorf("sync SQLite rollback snapshot: %w", err)
	}
	if err := recovery.ValidateSnapshot(ctx, snapshotPath); err != nil {
		return BundleResult{}, fmt.Errorf("validate SQLite rollback snapshot: %w", err)
	}
	for _, sidecar := range []string{snapshotPath + "-wal", snapshotPath + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return BundleResult{}, fmt.Errorf("remove SQLite validation sidecar: %w", err)
		}
	}
	if err := addFileEntry(&manifest, tempPath, snapshotRel); err != nil {
		return BundleResult{}, err
	}

	for _, source := range sources {
		if err := copySourceTree(source, request, tempPath, &manifest, hooks); err != nil {
			return BundleResult{}, err
		}
	}
	externalBytes, err := json.MarshalIndent(request.ExternalIdentities, "", "  ")
	if err != nil {
		return BundleResult{}, fmt.Errorf("encode external identity manifest: %w", err)
	}
	if err := writeBundleFile(tempPath, "external-identities.json", append(externalBytes, '\n'), &manifest, hooks); err != nil {
		return BundleResult{}, err
	}
	const instructions = "Omnora offline migration rollback bundle. Restore only with the explicit recovery command after validating manifest.json and all SHA-256 entries. External mount content is not included.\n"
	if err := writeBundleFile(tempPath, "RESTORE.txt", []byte(instructions), &manifest, hooks); err != nil {
		return BundleResult{}, err
	}

	postSources, err := captureSources(request)
	if err != nil {
		return BundleResult{}, fmt.Errorf("recapture persistent sources: %w", err)
	}
	if err := compareSources(sources, postSources, request); err != nil {
		return BundleResult{}, err
	}
	if hooks.afterCopy != nil {
		if err := hooks.afterCopy(tempPath); err != nil {
			return BundleResult{}, fmt.Errorf("rollback bundle copy hook: %w", err)
		}
	}

	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	for _, entry := range manifest.Files {
		if entry.Type == "file" {
			manifest.FileCount++
			manifest.TotalBytes += uint64(entry.Size)
		}
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return BundleResult{}, fmt.Errorf("encode rollback manifest: %w", err)
	}
	manifestRel := "manifest.json"
	manifestPath := filepath.Join(tempPath, manifestRel)
	if err := writePlainFile(manifestPath, append(manifestBytes, '\n'), hooks); err != nil {
		return BundleResult{}, fmt.Errorf("write rollback manifest: %w", err)
	}
	if err := validateBundleContents(tempPath, manifest); err != nil {
		return BundleResult{}, fmt.Errorf("validate rollback bundle: %w", err)
	}
	if err := syncTreeDirectories(tempPath, hooks.syncDir); err != nil {
		return BundleResult{}, fmt.Errorf("sync incomplete rollback bundle: %w", err)
	}
	if err := hooks.rename(tempPath, finalPath); err != nil {
		return BundleResult{}, fmt.Errorf("publish rollback bundle: %w", err)
	}
	if err := hooks.syncDir(request.RollbackRoot); err != nil {
		return BundleResult{}, fmt.Errorf("sync rollback root after publish: %w", err)
	}
	readyPath := filepath.Join(finalPath, "ROLLBACK_READY")
	if err := writeReadyMarker(readyPath, hooks); err != nil {
		_ = os.Remove(readyPath)
		_ = hooks.syncDir(finalPath)
		return BundleResult{}, err
	}
	if err := hooks.syncDir(request.RollbackRoot); err != nil {
		_ = os.Remove(readyPath)
		_ = hooks.syncDir(finalPath)
		return BundleResult{}, fmt.Errorf("sync rollback root after ready marker: %w", err)
	}

	return BundleResult{
		ID:            id,
		Path:          finalPath,
		ManifestPath:  filepath.Join(finalPath, manifestRel),
		SnapshotPath:  filepath.Join(finalPath, filepath.FromSlash(snapshotRel)),
		RequiredBytes: required + bundleSpaceReserve,
	}, nil
}

// testHooks is intentionally package-private: production callers cannot
// weaken durability, while tests can inject precise fsync/rename failures.
func (r *BundleRequest) setTestHooks(hooks bundleTestHooks) { r.testHooks = &hooks }

// Kept outside the public request contract.
func trimBundleRequest(request BundleRequest) BundleRequest {
	request.DBPath = strings.TrimSpace(request.DBPath)
	request.ConfigDir = strings.TrimSpace(request.ConfigDir)
	request.DataDir = strings.TrimSpace(request.DataDir)
	request.ManagedDir = strings.TrimSpace(request.ManagedDir)
	request.RollbackRoot = strings.TrimSpace(request.RollbackRoot)
	request.InstanceID = strings.TrimSpace(request.InstanceID)
	request.SourceMigration = strings.TrimSpace(request.SourceMigration)
	request.TargetMigration = strings.TrimSpace(request.TargetMigration)
	return request
}

func validateBundleRequest(request BundleRequest) error {
	if request.DB == nil {
		return errors.New("rollback bundle database is required")
	}
	fields := map[string]string{
		"database path": request.DBPath, "config directory": request.ConfigDir,
		"data directory": request.DataDir, "managed directory": request.ManagedDir,
		"rollback root": request.RollbackRoot, "source migration": request.SourceMigration,
		"target migration": request.TargetMigration,
	}
	for name, value := range fields {
		if value == "" {
			return fmt.Errorf("rollback bundle %s is required", name)
		}
	}
	if !instanceIDPattern.MatchString(request.InstanceID) {
		return errors.New("rollback bundle instance ID must be 64 lowercase hexadecimal characters")
	}
	if !isWithinPath(request.DBPath, request.DataDir) {
		return errors.New("rollback bundle database must be inside the data directory")
	}
	if info, err := os.Lstat(request.DBPath); err != nil {
		return fmt.Errorf("inspect rollback bundle database: %w", err)
	} else if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("rollback bundle database must be a real regular file")
	}
	roots := []string{request.ConfigDir, request.DataDir, request.ManagedDir}
	for _, identity := range request.ExternalIdentities {
		if strings.TrimSpace(identity.Root) == "" || strings.TrimSpace(identity.RegistrationID) == "" {
			return errors.New("external identity registration ID and root are required")
		}
		roots = append(roots, identity.Root)
	}
	for _, root := range roots {
		if pathsOverlap(request.RollbackRoot, root) {
			return fmt.Errorf("rollback root overlaps persistent source: %s", root)
		}
	}
	for i := 0; i < 3; i++ {
		for j := i + 1; j < 3; j++ {
			if pathsOverlap(roots[i], roots[j]) {
				return fmt.Errorf("persistent source roots overlap: %s and %s", roots[i], roots[j])
			}
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return true
	}
	return isWithin(leftAbs, rightAbs) || isWithin(rightAbs, leftAbs)
}

func isWithinPath(path, root string) bool {
	pathAbs, pathErr := filepath.Abs(filepath.Clean(path))
	rootAbs, rootErr := filepath.Abs(filepath.Clean(root))
	return pathErr == nil && rootErr == nil && isWithin(pathAbs, rootAbs)
}

func isWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func captureSources(request BundleRequest) ([]sourceTree, error) {
	specs := []struct{ label, root string }{{"config", request.ConfigDir}, {"data", request.DataDir}, {"managed", request.ManagedDir}}
	result := make([]sourceTree, 0, len(specs))
	for _, spec := range specs {
		tree, err := captureTree(spec.label, spec.root)
		if err != nil {
			return nil, fmt.Errorf("capture %s source: %w", spec.label, err)
		}
		result = append(result, tree)
	}
	return result, nil
}

func captureTree(label, root string) (sourceTree, error) {
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return sourceTree{}, err
	}
	rootInfo, err := os.Lstat(absRoot)
	if err != nil {
		return sourceTree{}, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return sourceTree{}, errors.New("persistent root must be a real directory")
	}
	tree := sourceTree{label: label, root: absRoot, entries: make(map[string]sourceEntry)}
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed: %s", rel)
		}
		item := sourceEntry{rel: rel, info: info}
		switch {
		case info.IsDir():
		case info.Mode().IsRegular():
			item.digest, err = hashFile(path)
			if err != nil {
				return err
			}
			tree.bytes += uint64(info.Size())
		default:
			return fmt.Errorf("non-regular persistent entry is not allowed: %s", rel)
		}
		tree.entries[rel] = item
		return nil
	})
	return tree, err
}

func copySourceTree(source sourceTree, request BundleRequest, bundleRoot string, manifest *BundleManifest, hooks bundleTestHooks) error {
	destinationRoot := filepath.Join(bundleRoot, source.label)
	if err := os.Mkdir(destinationRoot, 0o700); err != nil {
		return fmt.Errorf("create %s bundle root: %w", source.label, err)
	}
	addDirectoryEntry(manifest, source.label)
	rels := make([]string, 0, len(source.entries))
	for rel := range source.entries {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		item := source.entries[rel]
		if source.label == "data" && excludedDataPath(filepath.Join(source.root, rel), request.DBPath) {
			continue
		}
		destinationRel := filepath.ToSlash(filepath.Join(source.label, rel))
		destination := filepath.Join(bundleRoot, filepath.FromSlash(destinationRel))
		if item.info.IsDir() {
			if err := os.Mkdir(destination, 0o700); err != nil {
				return fmt.Errorf("create rollback directory %s: %w", destinationRel, err)
			}
			addDirectoryEntry(manifest, destinationRel)
			continue
		}
		digest, err := copyRegularFile(filepath.Join(source.root, rel), destination, hooks)
		if err != nil {
			return fmt.Errorf("copy persistent file %s: %w", destinationRel, err)
		}
		if digest != item.digest {
			return fmt.Errorf("persistent file changed while copying: %s", destinationRel)
		}
		if err := addFileEntry(manifest, bundleRoot, destinationRel); err != nil {
			return err
		}
	}
	return nil
}

func excludedDataPath(path, dbPath string) bool {
	absPath, pathErr := filepath.Abs(filepath.Clean(path))
	absDB, dbErr := filepath.Abs(filepath.Clean(dbPath))
	if pathErr != nil || dbErr != nil {
		return false
	}
	if absPath == absDB || absPath == absDB+"-wal" || absPath == absDB+"-shm" {
		return true
	}
	base := filepath.Base(absPath)
	return strings.Contains(base, ".restore-staging-") || strings.Contains(base, ".offline-migration-")
}

func copyRegularFile(source, destination string, hooks bundleTestHooks) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	before, err := input.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", errors.New("source stopped being a regular file")
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hasher), input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	after, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return "", errors.New("source identity changed while copying")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func compareSources(before, after []sourceTree, request BundleRequest) error {
	if len(before) != len(after) {
		return errors.New("persistent source set changed while building rollback bundle")
	}
	for index := range before {
		left, right := before[index], after[index]
		if left.label != right.label {
			return fmt.Errorf("persistent %s tree changed while building rollback bundle", left.label)
		}
		for rel, oldEntry := range left.entries {
			if left.label == "data" && excludedDataPath(filepath.Join(left.root, rel), request.DBPath) {
				continue
			}
			newEntry, ok := right.entries[rel]
			if !ok || !os.SameFile(oldEntry.info, newEntry.info) || oldEntry.info.Mode() != newEntry.info.Mode() || oldEntry.info.Size() != newEntry.info.Size() || oldEntry.info.ModTime() != newEntry.info.ModTime() || oldEntry.digest != newEntry.digest {
				return fmt.Errorf("persistent source changed while building rollback bundle: %s/%s", left.label, rel)
			}
		}
		for rel := range right.entries {
			if right.label == "data" && excludedDataPath(filepath.Join(right.root, rel), request.DBPath) {
				continue
			}
			if _, ok := left.entries[rel]; !ok {
				return fmt.Errorf("persistent source added while building rollback bundle: %s/%s", right.label, rel)
			}
		}
	}
	return nil
}

func writeBundleFile(root, rel string, content []byte, manifest *BundleManifest, hooks bundleTestHooks) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := writePlainFile(path, content, hooks); err != nil {
		return fmt.Errorf("write rollback bundle file %s: %w", rel, err)
	}
	return addFileEntry(manifest, root, rel)
}

func writePlainFile(path string, content []byte, hooks bundleTestHooks) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func addDirectoryEntry(manifest *BundleManifest, rel string) {
	manifest.Files = append(manifest.Files, ManifestEntry{Path: filepath.ToSlash(rel), Type: "directory", Mode: 0o700})
}

func addFileEntry(manifest *BundleManifest, root, rel string) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect rollback artifact %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("rollback artifact has unsafe type or mode: %s", rel)
	}
	digest, err := hashFile(path)
	if err != nil {
		return fmt.Errorf("hash rollback artifact %s: %w", rel, err)
	}
	manifest.Files = append(manifest.Files, ManifestEntry{Path: filepath.ToSlash(rel), Type: "file", Size: info.Size(), Mode: 0o600, SHA256: digest})
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func validateBundleContents(root string, manifest BundleManifest) error {
	listed := make(map[string]ManifestEntry, len(manifest.Files)+1)
	for _, entry := range manifest.Files {
		if _, exists := listed[entry.Path]; exists {
			return fmt.Errorf("duplicate manifest entry: %s", entry.Path)
		}
		listed[entry.Path] = entry
		path := filepath.Join(root, filepath.FromSlash(entry.Path))
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if entry.Type == "directory" {
			if !info.IsDir() || info.Mode().Perm() != 0o700 {
				return fmt.Errorf("unsafe rollback directory: %s", entry.Path)
			}
			continue
		}
		if entry.Type != "file" || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != entry.Size {
			return fmt.Errorf("rollback file metadata mismatch: %s", entry.Path)
		}
		digest, err := hashFile(path)
		if err != nil {
			return err
		}
		if digest != entry.SHA256 {
			return fmt.Errorf("rollback file digest mismatch: %s", entry.Path)
		}
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "manifest.json" {
			return nil
		}
		if _, ok := listed[rel]; !ok {
			return fmt.Errorf("unlisted rollback artifact: %s", rel)
		}
		return nil
	})
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path must be a real directory")
	}
	return nil
}

func syncTreeDirectories(root string, syncDir func(string) error) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		if err := syncDir(directory); err != nil {
			return err
		}
	}
	return nil
}

func writeReadyMarker(path string, hooks bundleTestHooks) error {
	if err := writePlainFile(path, []byte("ready\n"), hooks); err != nil {
		return fmt.Errorf("write rollback ready marker: %w", err)
	}
	if err := hooks.syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync rollback ready marker: %w", err)
	}
	return nil
}

func defaultBundleHooks() bundleTestHooks {
	return bundleTestHooks{
		availableBytes: availableFilesystemBytes,
		syncFile:       syncFilePath,
		syncDir:        syncDirectoryPath,
		rename:         os.Rename,
	}
}

func mergeBundleHooks(base, override bundleTestHooks) bundleTestHooks {
	if override.availableBytes != nil {
		base.availableBytes = override.availableBytes
	}
	if override.afterCopy != nil {
		base.afterCopy = override.afterCopy
	}
	if override.syncFile != nil {
		base.syncFile = override.syncFile
	}
	if override.syncDir != nil {
		base.syncDir = override.syncDir
	}
	if override.rename != nil {
		base.rename = override.rename
	}
	return base
}

func availableFilesystemBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func syncFilePath(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectoryPath(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func newBundleID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate rollback bundle ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
