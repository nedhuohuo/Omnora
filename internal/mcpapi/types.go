package mcpapi

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/audit"
	"omnora/internal/confirmation"
	"omnora/internal/contentref"
	"omnora/internal/memberfiles"
	"omnora/internal/membershare"
	"omnora/internal/transferticket"
)

// ToolDependencies is the service graph used by the standard MCP adapter.
// The adapter owns no storage or authorization logic; every operation is
// delegated to these live, shared services.
type ToolDependencies struct {
	MemberFiles     *memberfiles.Service
	MemberShares    *membershare.Service
	TransferTickets *transferticket.Service
	AuditRecorder   audit.Recorder
	AuditEnabled    bool
	Confirmations   *confirmation.Service
}

type ToolSpec struct {
	Name        string
	Title       string
	Description string
	Scope       aitoken.Scope
	ReadOnly    bool
	Destructive bool
	OpenWorld   bool
}

// OrdinaryToolSpecs is the stable catalogue for the non-MRTR tools. High-risk
// tools are intentionally added by a later confirmation layer.
func OrdinaryToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Name: "mounts.list", Title: "List common mounts", Description: "List common mounts currently granted to the authenticated account.", Scope: aitoken.ScopeMountsRead, ReadOnly: true},
		{Name: "files.list", Title: "List files", Description: "List a directory within an authorized mount.", Scope: aitoken.ScopeFilesList, ReadOnly: true},
		{Name: "files.metadata", Title: "Read file metadata", Description: "Read metadata for an authorized file or directory.", Scope: aitoken.ScopeFilesMetadata, ReadOnly: true},
		{Name: "files.search", Title: "Search files", Description: "Search indexed content within current authorization boundaries.", Scope: aitoken.ScopeSearchRead, ReadOnly: true},
		{Name: "files.read_text", Title: "Read text", Description: "Read a bounded UTF-8 text preview from an authorized file.", Scope: aitoken.ScopeFilesText, ReadOnly: true},
		{Name: "files.prepare_download", Title: "Prepare download", Description: "Issue a short-lived download ticket for an authorized regular file.", Scope: aitoken.ScopeFilesDownloadTicket, ReadOnly: true},
		{Name: "directories.create", Title: "Create directory", Description: "Create a directory in an authorized writable mount.", Scope: aitoken.ScopeFilesWrite},
		{Name: "files.prepare_upload", Title: "Prepare upload", Description: "Create an upload session and short-lived upload ticket.", Scope: aitoken.ScopeUploadsCreate},
		{Name: "uploads.status", Title: "Upload status", Description: "Read the status of an owned upload session.", Scope: aitoken.ScopeUploadsCreate, ReadOnly: true},
		{Name: "uploads.complete", Title: "Complete upload", Description: "Complete an owned upload session after revalidation.", Scope: aitoken.ScopeUploadsCreate},
		{Name: "uploads.cancel", Title: "Cancel upload", Description: "Cancel an owned upload session and invalidate its ticket.", Scope: aitoken.ScopeUploadsCreate, Destructive: true},
		{Name: "files.rename", Title: "Rename file", Description: "Rename an authorized file or directory without overwriting.", Scope: aitoken.ScopeFilesWrite, Destructive: true},
		{Name: "files.copy", Title: "Copy file", Description: "Copy an authorized file or directory to an authorized destination.", Scope: aitoken.ScopeFilesWrite},
		{Name: "trash.list", Title: "List trash", Description: "List the authorized managed-mount trash.", Scope: aitoken.ScopeTrashRead, ReadOnly: true},
		{Name: "trash.restore", Title: "Restore trash", Description: "Restore an authorized trash item without overwriting.", Scope: aitoken.ScopeFilesRestore},
		{Name: "shares.list", Title: "List shares", Description: "List authenticated member shares without secrets.", Scope: aitoken.ScopeSharesRead, ReadOnly: true},
	}
}

type LocatorInput struct {
	Source  contentref.Source `json:"source" jsonschema:"required"`
	MountID string            `json:"mountId,omitempty"`
	Path    string            `json:"path" jsonschema:"required"`
}

func (in LocatorInput) locator() access.Locator {
	locator, _ := ValidateLocatorInput(in)
	return locator
}

func ValidateLocatorInput(in LocatorInput) (access.Locator, error) {
	return contentref.NormalizeForAutomation(contentref.Locator{Source: in.Source, MountID: in.MountID, Path: in.Path})
}

type EmptyInput struct{}
type SearchInput struct {
	Source  contentref.Source `json:"source" jsonschema:"required"`
	MountID string            `json:"mountId,omitempty"`
	Query   string            `json:"query" jsonschema:"required"`
	Limit   int               `json:"limit,omitempty"`
	Cursor  string            `json:"cursor,omitempty"`
}
type TextInput struct {
	LocatorInput
	MaxBytes int64 `json:"maxBytes,omitempty"`
}
type DirectoryInput struct {
	LocatorInput
	Name string `json:"name" jsonschema:"required"`
}
type UploadPrepareInput struct {
	LocatorInput
	ExpectedSize int64  `json:"expectedSize" jsonschema:"required"`
	Checksum     string `json:"checksum,omitempty"`
}
type FileUpdateInput struct {
	LocatorInput
	ExpectedSize int64  `json:"expectedSize" jsonschema:"required"`
	Checksum     string `json:"checksum,omitempty"`
}
type UploadIDInput struct {
	UploadID string `json:"uploadId" jsonschema:"required"`
}
type RenameInput struct {
	LocatorInput
	Destination string `json:"destination" jsonschema:"required"`
}
type CopyInput struct {
	Source      LocatorInput `json:"source" jsonschema:"required"`
	Destination LocatorInput `json:"destination" jsonschema:"required"`
}
type RestoreInput struct {
	LocatorInput
	TrashID string `json:"trashId" jsonschema:"required"`
}
type ShareListInput struct {
	Limit int `json:"limit,omitempty"`
}

type MoveInput struct {
	Source      LocatorInput `json:"source" jsonschema:"required"`
	Destination LocatorInput `json:"destination" jsonschema:"required"`
}

type TrashInput struct{ LocatorInput }
type TrashPurgeInput struct {
	LocatorInput
	TrashID string `json:"trashId" jsonschema:"required"`
}
type DeletePermanentInput struct{ LocatorInput }
type ShareCreateInput struct {
	LocatorInput
	Password      string    `json:"password,omitempty"`
	AllowPreview  *bool     `json:"allowPreview,omitempty"`
	AllowDownload *bool     `json:"allowDownload,omitempty"`
	MaxVisits     *int64    `json:"maxVisits,omitempty"`
	MaxDownloads  *int64    `json:"maxDownloads,omitempty"`
	ExpiresAt     time.Time `json:"expiresAt,omitempty"`
}
type ShareRevokeInput struct {
	ShareID string `json:"shareId" jsonschema:"required"`
}

func HighRiskToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Name: "files.move", Title: "Move file", Description: "Move an authorized file or directory after confirmation.", Scope: aitoken.ScopeFilesWrite, Destructive: true},
		{Name: "files.trash", Title: "Trash file", Description: "Move an authorized object to managed trash after confirmation.", Scope: aitoken.ScopeFilesTrash, Destructive: true},
		{Name: "trash.purge", Title: "Purge trash", Description: "Permanently delete one trash item after confirmation.", Scope: aitoken.ScopeFilesPurge, Destructive: true},
		{Name: "trash.empty", Title: "Empty trash", Description: "Permanently delete all authorized trash after confirmation.", Scope: aitoken.ScopeFilesPurge, Destructive: true},
		{Name: "files.delete_permanently", Title: "Delete permanently", Description: "Permanently delete an authorized object after confirmation.", Scope: aitoken.ScopeFilesPurge, Destructive: true},
		{Name: "shares.create", Title: "Create share", Description: "Create a public share after confirmation.", Scope: aitoken.ScopeSharesCreate, OpenWorld: true},
		{Name: "shares.revoke", Title: "Revoke share", Description: "Revoke a public share after confirmation.", Scope: aitoken.ScopeSharesRevoke, Destructive: true, OpenWorld: true},
		{Name: "files.update", Title: "Update file content", Description: "Replace an authorized file's content through a confirmed upload.", Scope: aitoken.ScopeUploadsCreate, Destructive: true},
	}
}

type ToolError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId,omitempty"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

type ToolOutput[T any] struct {
	Data  T          `json:"data,omitempty"`
	Error *ToolError `json:"error,omitempty"`
}

type DownloadTicketOutput struct {
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Size        int64             `json:"size"`
	ETag        string            `json:"etag,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	ExpiresAt   time.Time         `json:"expiresAt"`
}

type UploadTicketOutput struct {
	ID           string            `json:"id"`
	TargetPath   string            `json:"targetPath"`
	ExpectedSize int64             `json:"expectedSize"`
	Checksum     string            `json:"checksum,omitempty"`
	PartSize     int               `json:"partSize"`
	URLTemplate  string            `json:"urlTemplate"`
	Headers      map[string]string `json:"headers"`
	ExpiresAt    time.Time         `json:"expiresAt"`
}

type UploadCancelOutput struct {
	UploadID string `json:"uploadId"`
	Canceled bool   `json:"canceled"`
}

type principalSubject struct {
	Principal aitoken.Principal
	Subject   access.Subject
}

func (d ToolDependencies) subject(req *mcp.CallToolRequest) (principalSubject, error) {
	p, err := PrincipalFromRequest(req)
	if err != nil {
		return principalSubject{}, err
	}
	return principalSubject{Principal: p, Subject: access.Subject{AccountID: p.AccountID, Principal: &p}}, nil
}
