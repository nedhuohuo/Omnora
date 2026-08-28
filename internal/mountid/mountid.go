package mountid

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const defaultMountInfoPath = "/proc/self/mountinfo"

var (
	ErrIdentityUnverifiable = errors.New("mount_identity_unverifiable")
	ErrMountConflict        = errors.New("mount_conflict")
)

type Identity struct {
	Path string

	Device uint64
	Inode  uint64

	Mount MountInfo
	Statx StatxInfo
}

type StatxInfo struct {
	Available bool

	Mask        uint32
	DeviceMajor uint32
	DeviceMinor uint32
	Inode       uint64
	MountID     uint64
}

type MountInfo struct {
	Available bool
	ReadOnly  bool

	ID       int
	ParentID int
	Device   string
	Root     string
	Point    string
	FSType   string
	Source   string
}

type ConflictError struct {
	Reason       string
	Candidate    Identity
	Existing     Identity
	CandidateKey string
	ExistingKey  string
}

func (e *ConflictError) Error() string {
	if e.Reason == "" {
		return ErrMountConflict.Error()
	}
	return fmt.Sprintf("%s: %s", ErrMountConflict, e.Reason)
}

func (e *ConflictError) Unwrap() error {
	return ErrMountConflict
}

func VerifyCandidateRoot(root string, existing []Identity) (Identity, error) {
	return verifyCandidateRoot(root, existing, defaultMountInfoPath)
}

func verifyCandidateRoot(root string, existing []Identity, mountInfoPath string) (Identity, error) {
	identity, err := capture(root, mountInfoPath)
	if err != nil {
		return Identity{}, err
	}
	if err := CheckConflicts(identity, existing); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func Capture(root string) (Identity, error) {
	return capture(root, defaultMountInfoPath)
}

// ListMountPoints returns the mount points visible in the current process mount namespace.
func ListMountPoints() ([]MountInfo, error) {
	return listMountPoints(defaultMountInfoPath)
}

func listMountPoints(mountInfoPath string) ([]MountInfo, error) {
	file, err := os.Open(mountInfoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []MountInfo{}, nil
		}
		return nil, fmt.Errorf("%w: read mountinfo: %v", ErrIdentityUnverifiable, err)
	}
	defer file.Close()
	return parseMountInfo(file)
}

func CheckConflicts(candidate Identity, existing []Identity) error {
	if candidate.Path == "" || candidate.Device == 0 || candidate.Inode == 0 {
		return fmt.Errorf("%w: candidate identity is incomplete", ErrIdentityUnverifiable)
	}

	for _, current := range existing {
		if current.Path == "" || current.Device == 0 || current.Inode == 0 {
			return fmt.Errorf("%w: existing identity is incomplete", ErrIdentityUnverifiable)
		}
		if samePath(candidate.Path, current.Path) {
			return newConflict("same canonical path", candidate, current, candidate.Path, current.Path)
		}
		if pathContains(candidate.Path, current.Path) || pathContains(current.Path, candidate.Path) {
			return newConflict("path parent-child overlap", candidate, current, candidate.Path, current.Path)
		}
		if candidate.Device == current.Device && candidate.Inode == current.Inode {
			return newConflict("same root device and inode", candidate, current, devInodeKey(candidate), devInodeKey(current))
		}
		if bindSourceOverlaps(candidate.Mount, current.Mount) {
			return newConflict("bind source parent-child overlap", candidate, current, mountSourceKey(candidate.Mount), mountSourceKey(current.Mount))
		}
	}
	return nil
}

func capture(root, mountInfoPath string) (Identity, error) {
	cleaned, err := cleanRoot(root)
	if err != nil {
		return Identity{}, err
	}
	info, err := lstatPathComponents(cleaned)
	if err != nil {
		return Identity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return Identity{}, fmt.Errorf("%w: stat_t unavailable for %q", ErrIdentityUnverifiable, cleaned)
	}

	identity := Identity{
		Path:   cleaned,
		Device: uint64(stat.Dev),
		Inode:  uint64(stat.Ino),
	}
	if identity.Device == 0 || identity.Inode == 0 {
		return Identity{}, fmt.Errorf("%w: device or inode unavailable for %q", ErrIdentityUnverifiable, cleaned)
	}
	identity.Statx, err = statxIdentity(cleaned)
	if err != nil {
		return Identity{}, err
	}

	mount, err := readMountInfo(cleaned, mountInfoPath)
	if err != nil {
		return Identity{}, err
	}
	identity.Mount = mount
	return identity, nil
}

func cleanRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: root path is empty", ErrIdentityUnverifiable)
	}
	cleaned := filepath.Clean(root)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("%w: root path must be absolute", ErrIdentityUnverifiable)
	}
	return cleaned, nil
}

func lstatPathComponents(root string) (os.FileInfo, error) {
	volume := filepath.VolumeName(root)
	rest := strings.TrimPrefix(root, volume)
	separator := string(os.PathSeparator)
	current := volume + separator

	info, err := rejectSymlink(current)
	if err != nil {
		return nil, err
	}
	if rest == separator {
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: root path is not a directory", ErrIdentityUnverifiable)
		}
		return info, nil
	}

	parts := strings.Split(strings.Trim(rest, separator), separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err = rejectSymlink(current)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: path component %q is not a directory", ErrIdentityUnverifiable, current)
		}
	}
	return info, nil
}

func rejectSymlink(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: lstat %q: %v", ErrIdentityUnverifiable, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: symlink component %q", ErrIdentityUnverifiable, path)
	}
	return info, nil
}

func readMountInfo(root, mountInfoPath string) (MountInfo, error) {
	file, err := os.Open(mountInfoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return MountInfo{}, nil
		}
		return MountInfo{}, fmt.Errorf("%w: read mountinfo: %v", ErrIdentityUnverifiable, err)
	}
	defer file.Close()

	entries, err := parseMountInfo(file)
	if err != nil {
		return MountInfo{}, err
	}
	best := bestMountForPath(root, entries)
	if len(entries) > 0 && !best.Available {
		return MountInfo{}, fmt.Errorf("%w: mountinfo has no entry for %q", ErrIdentityUnverifiable, root)
	}
	return best, nil
}

func parseMountInfo(reader io.Reader) ([]MountInfo, error) {
	scanner := bufio.NewScanner(reader)
	var entries []MountInfo
	for scanner.Scan() {
		entry, err := parseMountInfoLine(scanner.Text())
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: scan mountinfo: %v", ErrIdentityUnverifiable, err)
	}
	return entries, nil
}

func parseMountInfoLine(line string) (MountInfo, error) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return MountInfo{}, fmt.Errorf("%w: malformed mountinfo line", ErrIdentityUnverifiable)
	}

	separator := -1
	for i, field := range fields {
		if field == "-" {
			separator = i
			break
		}
	}
	if separator < 6 || separator+3 >= len(fields) {
		return MountInfo{}, fmt.Errorf("%w: malformed mountinfo separator", ErrIdentityUnverifiable)
	}

	id, err := strconv.Atoi(fields[0])
	if err != nil {
		return MountInfo{}, fmt.Errorf("%w: malformed mount ID", ErrIdentityUnverifiable)
	}
	parentID, err := strconv.Atoi(fields[1])
	if err != nil {
		return MountInfo{}, fmt.Errorf("%w: malformed parent mount ID", ErrIdentityUnverifiable)
	}

	root, err := unescapeMountInfoPath(fields[3])
	if err != nil {
		return MountInfo{}, err
	}
	point, err := unescapeMountInfoPath(fields[4])
	if err != nil {
		return MountInfo{}, err
	}
	source, err := unescapeMountInfoPath(fields[separator+2])
	if err != nil {
		return MountInfo{}, err
	}

	return MountInfo{
		Available: true,
		ReadOnly:  mountOptionsReadOnly(fields[5]),
		ID:        id,
		ParentID:  parentID,
		Device:    fields[2],
		Root:      filepath.Clean(root),
		Point:     filepath.Clean(point),
		FSType:    fields[separator+1],
		Source:    source,
	}, nil
}

func mountOptionsReadOnly(options string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == "ro" {
			return true
		}
	}
	return false
}

func unescapeMountInfoPath(value string) (string, error) {
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			builder.WriteByte(value[i])
			continue
		}
		if i+3 >= len(value) {
			return "", fmt.Errorf("%w: malformed mountinfo escape", ErrIdentityUnverifiable)
		}
		octal := value[i+1 : i+4]
		decoded, err := strconv.ParseUint(octal, 8, 8)
		if err != nil {
			return "", fmt.Errorf("%w: malformed mountinfo escape", ErrIdentityUnverifiable)
		}
		builder.WriteByte(byte(decoded))
		i += 3
	}
	return builder.String(), nil
}

func bestMountForPath(root string, entries []MountInfo) MountInfo {
	var best MountInfo
	for _, entry := range entries {
		if !entry.Available || !pathWithin(root, entry.Point) {
			continue
		}
		if !best.Available || len(entry.Point) > len(best.Point) {
			best = entry
		}
	}
	return best
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func pathContains(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	if parent == string(os.PathSeparator) {
		return filepath.IsAbs(child)
	}
	return strings.HasPrefix(child, parent+string(os.PathSeparator))
}

func pathWithin(path, root string) bool {
	return pathContains(root, path)
}

func bindSourceOverlaps(a, b MountInfo) bool {
	if !a.Available || !b.Available {
		return false
	}
	if a.ID == b.ID || a.Device == "" || a.Device != b.Device {
		return false
	}
	if a.Root == "" || b.Root == "" || !filepath.IsAbs(a.Root) || !filepath.IsAbs(b.Root) {
		return false
	}
	return pathContains(a.Root, b.Root) || pathContains(b.Root, a.Root)
}

func newConflict(reason string, candidate, existing Identity, candidateKey, existingKey string) error {
	return &ConflictError{
		Reason:       reason,
		Candidate:    candidate,
		Existing:     existing,
		CandidateKey: candidateKey,
		ExistingKey:  existingKey,
	}
}

func devInodeKey(identity Identity) string {
	return fmt.Sprintf("%d:%d", identity.Device, identity.Inode)
}

func mountSourceKey(mount MountInfo) string {
	return fmt.Sprintf("%s:%s", mount.Device, mount.Root)
}
