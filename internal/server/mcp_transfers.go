package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"omnora/internal/access"
	"omnora/internal/audit"
	"omnora/internal/httpx"
	"omnora/internal/memberfiles"
	"omnora/internal/storage"
	"omnora/internal/transfer"
	"omnora/internal/transferticket"
)

type mcpTransferAudit struct {
	intentID int64
	event    audit.MCPEvent
}

func (s *Server) beginMCPTransferAudit(ctx context.Context, ticket transferticket.VerifiedTicket, tool string, metadata map[string]any, r *http.Request) (*mcpTransferAudit, error) {
	if s == nil || s.db == nil {
		return &mcpTransferAudit{}, nil
	}
	event := audit.MCPEvent{
		AccountID: ticket.AccountID, CredentialPublicID: ticket.Principal.PublicID,
		ToolName: tool, TargetType: "mcp_transfer", TargetID: ticket.PublicID,
		RequestID: httpx.RequestID(ctx), TraceID: r.Header.Get("X-Trace-ID"),
		MetadataJSON: transferAuditMetadata(metadata),
	}
	intentID, err := s.auditRecorder.RecordMCPIntent(ctx, event)
	if err != nil {
		_ = s.auditRecorder.MarkMCPReadinessRisk(ctx, err)
		return nil, err
	}
	return &mcpTransferAudit{intentID: intentID, event: event}, nil
}

func (s *Server) finishMCPTransferAudit(ctx context.Context, auditRecord *mcpTransferAudit, result string, metadata map[string]any) error {
	if auditRecord == nil || auditRecord.intentID == 0 || s == nil {
		return nil
	}
	event := auditRecord.event
	event.Result = result
	event.MetadataJSON = transferAuditMetadata(metadata)
	if err := s.auditRecorder.RecordMCPOutcome(ctx, auditRecord.intentID, event); err != nil {
		_ = s.auditRecorder.MarkMCPReadinessRisk(ctx, err)
		return err
	}
	return nil
}

func transferAuditMetadata(metadata map[string]any) string {
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func transferAuditFailure(err error) map[string]any {
	return map[string]any{"errorCode": transferErrorCode(err)}
}

func downloadAuditFailure(ticket transferticket.VerifiedTicket, rangeLabel string, err error, bytes, start int64, status int) map[string]any {
	metadata := transferAuditFailure(err)
	metadata["operation"] = "download"
	metadata["targetLabel"] = transferTargetLabel(ticket)
	metadata["range"] = rangeLabel
	metadata["bytes"] = bytes
	metadata["start"] = start
	metadata["status"] = status
	return metadata
}

func transferTargetLabel(ticket transferticket.VerifiedTicket) string {
	relative, err := storage.CleanRelativePath(ticket.Locator.Path)
	if err != nil {
		relative = "<invalid>"
	}
	if ticket.Locator.Source == "personal" {
		return strings.Join([]string{"personal", relative}, "/")
	}
	return strings.Join([]string{string(ticket.Locator.Source), ticket.Locator.MountID, relative}, "/")
}

func safeTransferRangeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "full"
	}
	if len(value) > 128 || !strings.HasPrefix(strings.ToLower(value), "bytes=") {
		return "invalid"
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || r == '-' || r == ',' || r == '=' || r == ' ' || r == 'b' || r == 'B' || r == 'y' || r == 'Y' || r == 't' || r == 'T' || r == 'e' || r == 'E' || r == 's' || r == 'S' {
			continue
		}
		return "invalid"
	}
	return value
}

func transferErrorCode(err error) string {
	switch {
	case errors.Is(err, transferticket.ErrTicketExpired):
		return "ticket_expired"
	case errors.Is(err, transferticket.ErrTicketClosed):
		return "ticket_closed"
	case errors.Is(err, transferticket.ErrObjectDrift):
		return "object_changed"
	case errors.Is(err, transferticket.ErrByteBudgetExceeded), errors.Is(err, transfer.ErrRangeExceedsBudget):
		return "byte_budget_exceeded"
	case errors.Is(err, transfer.ErrInvalidRange), errors.Is(err, transfer.ErrUnsatisfiableRange):
		return "invalid_range"
	case errors.Is(err, transfer.ErrUploadTooLarge):
		return "upload_too_large"
	case errors.Is(err, transfer.ErrInvalidPartNumber):
		return "invalid_input"
	default:
		return "transfer_failed"
	}
}

// mcpDownload serves a ticket-bound regular file. The ticket is deliberately
// verified before opening the path so every request replays token, ACL,
// boundary, mount-identity and object-fingerprint checks.
func (s *Server) mcpDownload(w http.ResponseWriter, r *http.Request) {
	if hasSecretQuery(r) {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "transfer credentials must be sent in Authorization")
		return
	}
	bearer, ok := transferBearer(r)
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "a transfer bearer is required")
		return
	}
	if s.transferTickets == nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "transfer service is unavailable")
		return
	}
	ticket, err := s.transferTickets.Verify(r.Context(), bearer, transferticket.OperationDownload)
	if err != nil {
		writeMCPTransferError(w, r, err)
		return
	}
	rangeLabel := safeTransferRangeLabel(r.Header.Get("Range"))
	auditRecord, err := s.beginMCPTransferAudit(r.Context(), ticket, "mcp.transfer.download", map[string]any{"operation": "download", "targetLabel": transferTargetLabel(ticket), "range": rangeLabel}, r)
	if err != nil {
		writeMCPTransferError(w, r, errors.New("transfer audit unavailable"))
		return
	}
	fail := func(cause error, metadata map[string]any) {
		if metadata == nil {
			metadata = downloadAuditFailure(ticket, rangeLabel, cause, 0, 0, 0)
		} else {
			metadata["operation"] = "download"
			metadata["targetLabel"] = transferTargetLabel(ticket)
			metadata["range"] = rangeLabel
		}
		if _, ok := metadata["bytes"]; !ok {
			metadata["bytes"] = int64(0)
		}
		if _, ok := metadata["start"]; !ok {
			metadata["start"] = int64(0)
		}
		if _, ok := metadata["status"]; !ok {
			metadata["status"] = 0
		}
		if auditErr := s.finishMCPTransferAudit(r.Context(), auditRecord, "failed", metadata); auditErr != nil {
			writeMCPTransferError(w, r, errors.New("transfer audit unavailable"))
			return
		}
		writeMCPTransferError(w, r, cause)
	}
	filePath := filepath.Join(ticket.Mount.Root, filepath.FromSlash(ticket.Mount.RelativePath))
	file, err := os.Open(filePath)
	if err != nil {
		fail(err, transferAuditFailure(err))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = transferticket.ErrNotRegularFile
		}
		fail(err, transferAuditFailure(err))
		return
	}
	size := info.Size()
	rechecked, err := s.transferTickets.Verify(r.Context(), bearer, transferticket.OperationDownload)
	if err != nil {
		fail(err, transferAuditFailure(err))
		return
	}
	if rechecked.ObjectFingerprint != ticket.ObjectFingerprint {
		fail(transferticket.ErrObjectDrift, transferAuditFailure(transferticket.ErrObjectDrift))
		return
	}
	etag := `"` + ticket.ObjectFingerprint + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	if match := strings.TrimSpace(r.Header.Get("If-Match")); match != "" && match != "*" && match != etag {
		fail(transferticket.ErrObjectDrift, transferAuditFailure(transferticket.ErrObjectDrift))
		return
	}
	if none := strings.TrimSpace(r.Header.Get("If-None-Match")); none != "" && (none == etag || none == "*") {
		if err := s.finishMCPTransferAudit(r.Context(), auditRecord, "succeeded", map[string]any{"operation": "download", "targetLabel": transferTargetLabel(ticket), "range": rangeLabel, "bytes": int64(0), "status": http.StatusNotModified}); err != nil {
			writeMCPTransferError(w, r, errors.New("transfer audit unavailable"))
			return
		}
		w.WriteHeader(http.StatusNotModified)
		return
	}

	start, length, status, err := downloadRange(r.Header.Get("Range"), size, ticket.MaxBytes-ticket.ConsumedBytes)
	if err != nil {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
		fail(err, transferAuditFailure(err))
		return
	}
	if r.Method != http.MethodHead {
		if err := s.transferTickets.AddBytes(r.Context(), ticket.ID, length); err != nil {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			fail(err, transferAuditFailure(err))
			return
		}
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if status == http.StatusPartialContent {
		end := start + length - 1
		w.Header().Set("Content-Range", (&transfer.ByteRange{Start: start, End: end}).ContentRange(size))
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead || length == 0 {
		auditBytes := length
		if r.Method == http.MethodHead {
			auditBytes = 0
		}
		_ = s.finishMCPTransferAudit(r.Context(), auditRecord, "succeeded", map[string]any{"operation": "download", "targetLabel": transferTargetLabel(ticket), "range": rangeLabel, "bytes": auditBytes, "start": start, "status": status})
		return
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		_ = s.finishMCPTransferAudit(r.Context(), auditRecord, "failed", downloadAuditFailure(ticket, rangeLabel, err, 0, start, status))
		return
	}
	copyErr := error(nil)
	if _, err := io.CopyN(w, file, length); err != nil {
		copyErr = err
	}
	if copyErr != nil {
		_ = s.finishMCPTransferAudit(r.Context(), auditRecord, "failed", downloadAuditFailure(ticket, rangeLabel, copyErr, length, start, status))
	} else {
		_ = s.finishMCPTransferAudit(r.Context(), auditRecord, "succeeded", map[string]any{"operation": "download", "targetLabel": transferTargetLabel(ticket), "range": rangeLabel, "bytes": length, "start": start, "status": status})
	}
}

// mcpUploadPart accepts bytes only with an upload ticket. The request body is
// buffered after a bounded read so the budget can be reserved before the
// member-file service publishes the replacement part. Replacing a part only
// consumes the positive size delta, which keeps retries idempotent.
func (s *Server) mcpUploadPart(w http.ResponseWriter, r *http.Request) {
	if hasSecretQuery(r) {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "transfer credentials must be sent in Authorization")
		return
	}
	bearer, ok := transferBearer(r)
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "a transfer bearer is required")
		return
	}
	if s.transferTickets == nil || s.memberFiles == nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "transfer service is unavailable")
		return
	}
	ticket, err := s.transferTickets.Verify(r.Context(), bearer, transferticket.OperationUpload)
	if err != nil {
		writeMCPTransferError(w, r, err)
		return
	}
	auditRecord, err := s.beginMCPTransferAudit(r.Context(), ticket, "mcp.transfer.upload_part", map[string]any{"operation": "upload", "targetLabel": transferTargetLabel(ticket), "part": r.PathValue("partNumber")}, r)
	if err != nil {
		writeMCPTransferError(w, r, errors.New("transfer audit unavailable"))
		return
	}
	fail := func(cause error) {
		metadata := transferAuditFailure(cause)
		metadata["operation"] = "upload"
		metadata["targetLabel"] = transferTargetLabel(ticket)
		metadata["part"] = r.PathValue("partNumber")
		if auditErr := s.finishMCPTransferAudit(r.Context(), auditRecord, "failed", metadata); auditErr != nil {
			writeMCPTransferError(w, r, errors.New("transfer audit unavailable"))
			return
		}
		writeMCPTransferError(w, r, cause)
	}
	partNumber, err := strconv.Atoi(r.PathValue("partNumber"))
	if err != nil || partNumber <= 0 {
		fail(transfer.ErrInvalidPartNumber)
		return
	}
	subject := access.Subject{AccountID: ticket.AccountID, Principal: &ticket.Principal}
	status, err := s.memberFiles.UploadStatus(r.Context(), subject, ticket.UploadID)
	if err != nil {
		fail(err)
		return
	}
	if status.ExpectedSize == 0 || status.PartSize <= 0 {
		fail(transfer.ErrInvalidPartNumber)
		return
	}
	maxPartNumber := (status.ExpectedSize + int64(status.PartSize) - 1) / int64(status.PartSize)
	if int64(partNumber) > maxPartNumber {
		fail(transfer.ErrInvalidPartNumber)
		return
	}
	var oldSize int64
	for _, part := range status.Parts {
		if part.Number == partNumber {
			oldSize = part.Size
			break
		}
	}
	remaining := ticket.MaxBytes - ticket.ConsumedBytes
	if remaining < 0 {
		fail(transferticket.ErrByteBudgetExceeded)
		return
	}
	available := status.ExpectedSize - status.ReceivedSize + oldSize
	if available < 0 {
		available = 0
	}
	readLimit := available
	if budgetLimit := remaining + oldSize; budgetLimit < readLimit {
		readLimit = budgetLimit
	}
	if partLimit := int64(status.PartSize); partLimit > 0 && partLimit < readLimit {
		readLimit = partLimit
	}
	if readLimit < 0 || (r.ContentLength >= 0 && r.ContentLength > readLimit) {
		fail(transfer.ErrUploadTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, readLimit+1))
	if err != nil {
		fail(err)
		return
	}
	if int64(len(body)) > readLimit {
		fail(transfer.ErrUploadTooLarge)
		return
	}
	if len(body) == 0 {
		fail(memberfiles.ErrInvalidInput)
		return
	}
	delta := int64(len(body)) - oldSize
	if delta < 0 {
		delta = 0
	}
	if delta > 0 {
		if err := s.transferTickets.AddBytes(r.Context(), ticket.ID, delta); err != nil {
			fail(err)
			return
		}
	}
	if err := s.memberFiles.WriteUploadPart(r.Context(), subject, ticket.UploadID, partNumber, bytes.NewReader(body)); err != nil {
		fail(err)
		return
	}
	if err := s.finishMCPTransferAudit(r.Context(), auditRecord, "succeeded", map[string]any{"operation": "upload", "targetLabel": transferTargetLabel(ticket), "part": partNumber, "bytes": int64(len(body))}); err != nil {
		writeMCPTransferError(w, r, errors.New("transfer audit unavailable"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func hasSecretQuery(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	for _, name := range []string{"secret", "token", "access_token"} {
		if _, ok := r.URL.Query()[name]; ok {
			return true
		}
	}
	return false
}

func transferBearer(r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if value == "" {
		return "", false
	}
	if !strings.HasPrefix(strings.ToLower(value), "bearer ") || strings.TrimSpace(value[7:]) == "" {
		return "", false
	}
	credential := strings.TrimSpace(value[7:])
	publicID, _, ok := strings.Cut(credential, ".")
	if !ok || publicID == "" || (r.PathValue("publicId") != "" && publicID != r.PathValue("publicId")) {
		return "", false
	}
	// The ticket service accepts a raw `publicID.secret` credential. Keep the
	// HTTP scheme out of the value passed to it so adapters do not accidentally
	// double-prefix or persist the Authorization header.
	return credential, true
}

func downloadRange(header string, size, remaining int64) (start, length int64, status int, err error) {
	if header == "" {
		if size < 0 || remaining < size {
			return 0, 0, 0, transfer.ErrRangeExceedsBudget
		}
		return 0, size, http.StatusOK, nil
	}
	rangeValue, parseErr := transfer.ParseByteRange(header, size)
	if parseErr != nil {
		return 0, 0, 0, parseErr
	}
	if err := transfer.ValidateRangeBudget(rangeValue, remaining); err != nil {
		return 0, 0, 0, err
	}
	return rangeValue.Start, rangeValue.Length(), http.StatusPartialContent, nil
}

func writeMCPTransferError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, access.ErrMountIdentityUnverifiable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
	case errors.Is(err, access.ErrMountUnavailable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_unavailable", "mount is unavailable")
	case errors.Is(err, access.ErrReadonlyMount):
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
	case errors.Is(err, access.ErrForbidden), errors.Is(err, access.ErrBoundaryViolation):
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "transfer is not authorized")
	case errors.Is(err, access.ErrUnauthorized):
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "transfer credential is not valid")
	case errors.Is(err, transferticket.ErrTicketInvalid), errors.Is(err, transferticket.ErrUnauthorized):
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "transfer credential is not valid")
	case errors.Is(err, transferticket.ErrTicketNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "transfer ticket was not found")
	case errors.Is(err, transferticket.ErrTicketExpired), errors.Is(err, transferticket.ErrTicketClosed):
		httpx.WriteError(w, r, http.StatusGone, "ticket_expired", "transfer ticket is no longer active")
	case errors.Is(err, transferticket.ErrUploadInvalid):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "upload session was not found")
	case errors.Is(err, transferticket.ErrWrongOperation):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "transfer ticket operation does not match this route")
	case errors.Is(err, transferticket.ErrObjectDrift):
		httpx.WriteError(w, r, http.StatusConflict, "object_changed", "the authorized object has changed")
	case errors.Is(err, transferticket.ErrByteBudgetExceeded), errors.Is(err, transfer.ErrRangeExceedsBudget):
		httpx.WriteError(w, r, http.StatusRequestedRangeNotSatisfiable, "byte_budget_exceeded", "transfer byte budget was exceeded")
	case errors.Is(err, transfer.ErrInvalidRange), errors.Is(err, transfer.ErrUnsatisfiableRange):
		httpx.WriteError(w, r, http.StatusRequestedRangeNotSatisfiable, "invalid_range", "requested byte range is not satisfiable")
	case errors.Is(err, transfer.ErrInvalidPartNumber):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "part number is invalid")
	case errors.Is(err, memberfiles.ErrInvalidInput):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "upload part is invalid")
	case errors.Is(err, transferticket.ErrNotRegularFile), errors.Is(err, transfer.ErrNotRegularFile):
		httpx.WriteError(w, r, http.StatusConflict, "object_changed", "the authorized object is no longer a regular file")
	case errors.Is(err, os.ErrNotExist), errors.Is(err, memberfiles.ErrUploadNotFound), errors.Is(err, memberfiles.ErrUploadExpired):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "the requested transfer resource was not found")
	case errors.Is(err, memberfiles.ErrUploadConflict):
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", "upload session conflicts with the current state")
	case errors.Is(err, transfer.ErrUploadTooLarge):
		httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, "upload_too_large", "upload part exceeds the authorized size")
	default:
		httpx.WriteError(w, r, http.StatusInternalServerError, "internal_error", "transfer failed")
	}
}
