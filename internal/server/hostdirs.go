package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"omnora/internal/mountid"
)

type hostDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind,omitempty"`
}

type hostDirectoryRoot struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type hostDirectorySuggestions struct {
	// Roots is retained for clients that only need the configured paths. New
	// clients should use RootDetails so they do not have to infer a mount kind
	// from a path string.
	Roots       []string             `json:"roots"`
	RootDetails []hostDirectoryRoot  `json:"rootDetails"`
	Path        string               `json:"path"`
	Entries     []hostDirectoryEntry `json:"entries"`
}

func (s *Server) storageRootDetails() []hostDirectoryRoot {
	roots := make([]hostDirectoryRoot, 0, 2)
	for _, configured := range []struct {
		path string
		kind string
	}{
		{path: s.cfg.Storage.ManagedDir, kind: "managed"},
		{path: s.cfg.Storage.PredeclaredMountRoot, kind: "external"},
	} {
		root := configured.path
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." || root == string(filepath.Separator) {
			continue
		}
		if !filepath.IsAbs(root) {
			continue
		}
		roots = append(roots, hostDirectoryRoot{Path: root, Kind: configured.kind})
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Path == roots[j].Path {
			return roots[i].Kind < roots[j].Kind
		}
		return roots[i].Path < roots[j].Path
	})
	return uniqueRootDetails(roots)
}

func (s *Server) storageRoots() []string {
	paths := make([]string, 0, 2)
	for _, root := range s.storageRootDetails() {
		paths = append(paths, root.Path)
	}
	return uniqueSorted(paths)
}

func (s *Server) suggestHostDirectories(rawPath string) (hostDirectorySuggestions, error) {
	rootDetails := s.storageRootDetails()
	roots := make([]string, 0, len(rootDetails))
	for _, root := range rootDetails {
		roots = append(roots, root.Path)
	}
	roots = uniqueSorted(roots)
	path := filepath.Clean(strings.TrimSpace(rawPath))
	if path == "." || path == "" {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		path = "/" + strings.TrimPrefix(path, "/")
		path = filepath.Clean(path)
	}

	payload := hostDirectorySuggestions{
		Roots:       append([]string(nil), roots...),
		RootDetails: append([]hostDirectoryRoot{}, rootDetails...),
		Path:        path,
		Entries:     []hostDirectoryEntry{},
	}
	if len(roots) == 0 {
		return payload, nil
	}

	seen := map[string]struct{}{}
	add := func(entryPath string) {
		entryPath = filepath.Clean(entryPath)
		if _, ok := seen[entryPath]; ok {
			return
		}
		if !isUnderAnyRoot(entryPath, roots) {
			return
		}
		info, err := os.Lstat(entryPath)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return
		}
		seen[entryPath] = struct{}{}
		kind := rootKindForPath(entryPath, rootDetails)
		payload.Entries = append(payload.Entries, hostDirectoryEntry{
			Name: filepath.Base(entryPath),
			Path: entryPath,
			Kind: kind,
		})
	}

	for _, root := range roots {
		if strings.HasPrefix(root, path) || path == "/" {
			add(root)
		}
	}

	parent := path
	prefix := ""
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && isUnderAnyRoot(path, roots):
		parent = path
	case path != "/":
		parent = filepath.Dir(path)
		prefix = strings.ToLower(filepath.Base(path))
	default:
		sort.Slice(payload.Entries, func(i, j int) bool { return payload.Entries[i].Path < payload.Entries[j].Path })
		return payload, nil
	}

	if !isUnderAnyRoot(parent, roots) && parent != "/" {
		sort.Slice(payload.Entries, func(i, j int) bool { return payload.Entries[i].Path < payload.Entries[j].Path })
		return payload, nil
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			sort.Slice(payload.Entries, func(i, j int) bool { return payload.Entries[i].Path < payload.Entries[j].Path })
			return payload, nil
		}
		return payload, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if prefix != "" && !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		candidate := filepath.Join(parent, name)
		if !isUnderAnyRoot(candidate, roots) {
			continue
		}
		// External slots appear as suggestions only after the operator binds a
		// host directory to them, which surfaces as their own bind mount in
		// the container mount table. Unbound slots stay hidden even when an
		// empty directory exists at the path.
		if rootKindForPath(candidate, rootDetails) == "external" && s.isSlotPath(candidate) && !s.isBoundSlot(candidate) {
			continue
		}
		add(candidate)
	}

	sort.Slice(payload.Entries, func(i, j int) bool { return payload.Entries[i].Path < payload.Entries[j].Path })
	return payload, nil
}

func rootKindForPath(path string, roots []hostDirectoryRoot) string {
	path = filepath.Clean(path)
	bestLength := -1
	kind := ""
	for _, root := range roots {
		rootPath := filepath.Clean(root.Path)
		if path != rootPath && !strings.HasPrefix(path, rootPath+string(filepath.Separator)) {
			continue
		}
		if len(rootPath) > bestLength {
			bestLength = len(rootPath)
			kind = root.Kind
			continue
		}
		if len(rootPath) == bestLength && kind != root.Kind {
			// An overlapping path configured for two kinds is ambiguous. Keep
			// the path usable, but let the caller preserve its current kind.
			kind = ""
		}
	}
	return kind
}

func uniqueRootDetails(values []hostDirectoryRoot) []hostDirectoryRoot {
	seen := map[string]struct{}{}
	out := make([]hostDirectoryRoot, 0, len(values))
	for _, value := range values {
		key := value.Path + "\x00" + value.Kind
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func isUnderAnyRoot(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(root)
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isBoundSlot reports whether candidatePath is an external slot the operator
// bound to a host directory. A bound slot is its own bind mount point in the
// container mount table; a mountinfo read failure fails closed to unbound.
func (s *Server) isBoundSlot(candidatePath string) bool {
	bound, err := mountid.IsBindMount(candidatePath)
	return err == nil && bound
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
