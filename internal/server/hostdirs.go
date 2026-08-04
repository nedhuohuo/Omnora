package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type hostDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type hostDirectorySuggestions struct {
	Roots   []string             `json:"roots"`
	Path    string               `json:"path"`
	Entries []hostDirectoryEntry `json:"entries"`
}

func (s *Server) storageRoots() []string {
	roots := make([]string, 0, 2)
	for _, root := range []string{s.cfg.Storage.ManagedDir, s.cfg.Storage.PredeclaredMountRoot} {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." || root == string(filepath.Separator) {
			continue
		}
		if !filepath.IsAbs(root) {
			continue
		}
		roots = append(roots, root)
	}
	return uniqueSorted(roots)
}

func (s *Server) suggestHostDirectories(rawPath string) (hostDirectorySuggestions, error) {
	roots := s.storageRoots()
	path := filepath.Clean(strings.TrimSpace(rawPath))
	if path == "." || path == "" {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		path = "/" + strings.TrimPrefix(path, "/")
		path = filepath.Clean(path)
	}

	payload := hostDirectorySuggestions{
		Roots:   append([]string(nil), roots...),
		Path:    path,
		Entries: []hostDirectoryEntry{},
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
		payload.Entries = append(payload.Entries, hostDirectoryEntry{
			Name: filepath.Base(entryPath),
			Path: entryPath,
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
		add(candidate)
	}

	sort.Slice(payload.Entries, func(i, j int) bool { return payload.Entries[i].Path < payload.Entries[j].Path })
	return payload, nil
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
