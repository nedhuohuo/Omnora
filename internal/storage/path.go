package storage

import (
	"fmt"
	"path"
	"strings"
)

const ReservedNamespace = ".omnora"

func CleanRelativePath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return ".", nil
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("absolute paths are not allowed")
	}

	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	if cleaned == ReservedNamespace || strings.HasPrefix(cleaned, ReservedNamespace+"/") {
		return "", fmt.Errorf("reserved namespace is not allowed")
	}
	return cleaned, nil
}
