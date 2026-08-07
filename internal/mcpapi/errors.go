package mcpapi

import "errors"

var (
	ErrMissingPrincipal = errors.New("mcp: authenticated principal is missing")
	ErrInvalidPrincipal = errors.New("mcp: authenticated principal is invalid")
)
