package server

import (
	"errors"
	"net/http"

	"omnora/internal/access"
	"omnora/internal/httpx"
)

func (s *Server) authorizeMount(r *http.Request, accountID, spaceID, mountID string, operation access.Operation) (access.MountDecision, error) {
	return access.NewMountService(s.sqlDB()).Authorize(r.Context(), accountID, spaceID, mountID, operation)
}

func writeMountAuthorizationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, access.ErrMountAccessDenied) {
		httpx.WriteError(w, r, http.StatusForbidden, "mount_access_denied", "mount is not approved for this account and operation")
		return
	}
	writeDBError(w, r, err)
}
