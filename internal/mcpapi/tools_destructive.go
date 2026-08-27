package mcpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/confirmation"
	"omnora/internal/files"
	"omnora/internal/memberfiles"
	"omnora/internal/membershare"
	"omnora/internal/storage"
)

// RegisterHighRiskTools installs only tools guarded by the one-time MRTR
// confirmation service.
func RegisterHighRiskTools(server *mcp.Server, deps ToolDependencies) {
	if server == nil {
		return
	}
	specs := HighRiskToolSpecs()
	addTool(server, specs[0], func(ctx context.Context, req *mcp.CallToolRequest, in MoveInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if err := requireHighRiskDeps(deps, false); err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		preview := func() (confirmation.Preview, error) {
			sourcePath, destinationPath := safeRelativePath(in.Source.Path), safeRelativePath(in.Destination.Path)
			if sourcePath == "<invalid>" || destinationPath == "<invalid>" || (in.Source.Source == in.Destination.Source && in.Source.MountID == in.Destination.MountID && (sourcePath == destinationPath || strings.HasPrefix(destinationPath, sourcePath+"/"))) {
				return confirmation.Preview{}, memberfiles.ErrMutationInvalidPath
			}
			entry, err := deps.MemberFiles.Preview(ctx, ps.Subject, in.Source.locator(), aitoken.ScopeFilesWrite)
			if err != nil {
				return confirmation.Preview{}, err
			}
			if _, err := deps.MemberFiles.Preview(ctx, ps.Subject, in.Destination.locator(), aitoken.ScopeFilesWrite); err != nil {
				return confirmation.Preview{}, err
			}
			return impactFor(fmt.Sprintf("Move %s to %s", sourcePath, destinationPath), 1, entry.Size, map[string]any{"source": sourcePath, "destination": destinationPath}, fingerprintFor(entry.ObjectFingerprint, in.Destination.locator())), nil
		}
		proceed, pending, err := deps.requireConfirmation(ctx, req, ps.Principal, specs[0].Name, preview)
		if pending != nil {
			return pending, ToolOutput[memberfiles.MutationResult]{}, nil
		}
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if !proceed {
			return toolFailure[memberfiles.MutationResult](req, ErrConfirmationInvalid)
		}
		return mutate(ctx, deps, req, specs[0].Name, ps.Principal, in.Source.locator(), func() (memberfiles.MutationResult, error) {
			return deps.MemberFiles.MoveSecure(ctx, ps.Subject, in.Source.locator(), in.Destination.locator(), nil)
		})
	})
	addTool(server, specs[1], func(ctx context.Context, req *mcp.CallToolRequest, in TrashInput) (*mcp.CallToolResult, ToolOutput[memberfiles.TrashResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.TrashResult](req, err)
		}
		if err := requireHighRiskDeps(deps, false); err != nil {
			return toolFailure[memberfiles.TrashResult](req, err)
		}
		preview := func() (confirmation.Preview, error) {
			entry, err := deps.MemberFiles.Preview(ctx, ps.Subject, in.locator(), aitoken.ScopeFilesTrash)
			if err != nil {
				return confirmation.Preview{}, err
			}
			cleanPath := safeRelativePath(in.Path)
			return impactFor(fmt.Sprintf("Move %s to trash", cleanPath), 1, entry.Size, map[string]any{"path": cleanPath}, entry.ObjectFingerprint), nil
		}
		proceed, pending, err := deps.requireConfirmation(ctx, req, ps.Principal, specs[1].Name, preview)
		if pending != nil {
			return pending, ToolOutput[memberfiles.TrashResult]{}, nil
		}
		if err != nil {
			return toolFailure[memberfiles.TrashResult](req, err)
		}
		if !proceed {
			return toolFailure[memberfiles.TrashResult](req, ErrConfirmationInvalid)
		}
		return mutateAny(ctx, deps, req, specs[1].Name, ps.Principal, in.locator(), func() (memberfiles.TrashResult, error) {
			return deps.MemberFiles.TrashSecure(ctx, ps.Subject, in.locator(), nil)
		})
	})
	addTool(server, specs[2], func(ctx context.Context, req *mcp.CallToolRequest, in TrashPurgeInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if err := requireHighRiskDeps(deps, false); err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		preview := func() (confirmation.Preview, error) {
			list, err := deps.MemberFiles.PreviewTrash(ctx, ps.Subject, in.locator(), aitoken.ScopeFilesPurge)
			if err != nil {
				return confirmation.Preview{}, err
			}
			item, ok := findTrash(list, in.TrashID)
			if !ok {
				return confirmation.Preview{}, errors.New("trash item not found")
			}
			return impactFor(fmt.Sprintf("Permanently delete trash item %s", safeRelativePath(item.Name)), 1, item.Size, map[string]any{"trashId": in.TrashID}, fingerprintBytes(list)), nil
		}
		proceed, pending, err := deps.requireConfirmation(ctx, req, ps.Principal, specs[2].Name, preview)
		if pending != nil {
			return pending, ToolOutput[memberfiles.MutationResult]{}, nil
		}
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if !proceed {
			return toolFailure[memberfiles.MutationResult](req, ErrConfirmationInvalid)
		}
		return mutate(ctx, deps, req, specs[2].Name, ps.Principal, in.locator(), func() (memberfiles.MutationResult, error) {
			return deps.MemberFiles.PurgeTrashSecure(ctx, ps.Subject, in.locator(), in.TrashID, nil)
		})
	})
	addTool(server, specs[3], func(ctx context.Context, req *mcp.CallToolRequest, in LocatorInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if err := requireHighRiskDeps(deps, false); err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		preview := func() (confirmation.Preview, error) {
			list, err := deps.MemberFiles.PreviewEmptyTrash(ctx, ps.Subject, in.locator())
			if err != nil {
				return confirmation.Preview{}, err
			}
			cleanPath := safeRelativePath(in.Path)
			return impactFor(fmt.Sprintf("Empty trash for %s", cleanPath), list.TotalCount, list.TotalBytes, map[string]any{"path": cleanPath}, fingerprintBytes(list)), nil
		}
		proceed, pending, err := deps.requireConfirmation(ctx, req, ps.Principal, specs[3].Name, preview)
		if pending != nil {
			return pending, ToolOutput[memberfiles.MutationResult]{}, nil
		}
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if !proceed {
			return toolFailure[memberfiles.MutationResult](req, ErrConfirmationInvalid)
		}
		return mutate(ctx, deps, req, specs[3].Name, ps.Principal, in.locator(), func() (memberfiles.MutationResult, error) {
			return deps.MemberFiles.EmptyTrashSecure(ctx, ps.Subject, in.locator(), nil)
		})
	})
	addTool(server, specs[4], func(ctx context.Context, req *mcp.CallToolRequest, in DeletePermanentInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if err := requireHighRiskDeps(deps, false); err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		preview := func() (confirmation.Preview, error) {
			entry, err := deps.MemberFiles.Preview(ctx, ps.Subject, in.locator(), aitoken.ScopeFilesPurge)
			if err != nil {
				return confirmation.Preview{}, err
			}
			cleanPath := safeRelativePath(in.Path)
			return impactFor(fmt.Sprintf("Permanently delete %s", cleanPath), 1, entry.Size, map[string]any{"path": cleanPath}, entry.ObjectFingerprint), nil
		}
		proceed, pending, err := deps.requireConfirmation(ctx, req, ps.Principal, specs[4].Name, preview)
		if pending != nil {
			return pending, ToolOutput[memberfiles.MutationResult]{}, nil
		}
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if !proceed {
			return toolFailure[memberfiles.MutationResult](req, ErrConfirmationInvalid)
		}
		return mutate(ctx, deps, req, specs[4].Name, ps.Principal, in.locator(), func() (memberfiles.MutationResult, error) {
			return deps.MemberFiles.DeletePermanentlySecure(ctx, ps.Subject, in.locator(), nil)
		})
	})
	addTool(server, specs[5], func(ctx context.Context, req *mcp.CallToolRequest, in ShareCreateInput) (*mcp.CallToolResult, ToolOutput[membershare.IssuedShare], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[membershare.IssuedShare](req, err)
		}
		if err := requireHighRiskDeps(deps, true); err != nil {
			return toolFailure[membershare.IssuedShare](req, err)
		}
		preview := func() (confirmation.Preview, error) {
			entry, err := deps.MemberFiles.Preview(ctx, ps.Subject, in.locator(), aitoken.ScopeSharesCreate)
			if err != nil {
				return confirmation.Preview{}, err
			}
			cleanPath := safeRelativePath(in.Path)
			return impactFor(fmt.Sprintf("Create public share for %s", cleanPath), 1, entry.Size, map[string]any{"path": cleanPath, "allowPreview": boolValue(in.AllowPreview), "allowDownload": boolValue(in.AllowDownload)}, entry.ObjectFingerprint), nil
		}
		proceed, pending, err := deps.requireConfirmation(ctx, req, ps.Principal, specs[5].Name, preview)
		if pending != nil {
			return pending, ToolOutput[membershare.IssuedShare]{}, nil
		}
		if err != nil {
			return toolFailure[membershare.IssuedShare](req, err)
		}
		if !proceed {
			return toolFailure[membershare.IssuedShare](req, ErrConfirmationInvalid)
		}
		return mutateAny(ctx, deps, req, specs[5].Name, ps.Principal, in.locator(), func() (membershare.IssuedShare, error) {
			return deps.MemberShares.Create(ctx, ps.Subject, membershare.CreateRequest{Locator: in.locator(), Password: in.Password, AllowPreview: in.AllowPreview, AllowDownload: in.AllowDownload, MaxVisits: in.MaxVisits, MaxDownloads: in.MaxDownloads, ExpiresAt: in.ExpiresAt})
		})
	})
	addTool(server, specs[6], func(ctx context.Context, req *mcp.CallToolRequest, in ShareRevokeInput) (*mcp.CallToolResult, ToolOutput[map[string]any], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[map[string]any](req, err)
		}
		if err := requireHighRiskDeps(deps, true); err != nil {
			return toolFailure[map[string]any](req, err)
		}
		preview := func() (confirmation.Preview, error) {
			share, err := deps.MemberShares.PreviewRevoke(ctx, ps.Subject, in.ShareID)
			if err != nil {
				return confirmation.Preview{}, err
			}
			return impactFor("Revoke public share", 1, 0, map[string]any{"shareId": share.PublicID, "path": safeRelativePath(share.RelativePath)}, fingerprintBytes(share)), nil
		}
		proceed, pending, err := deps.requireConfirmation(ctx, req, ps.Principal, specs[6].Name, preview)
		if pending != nil {
			return pending, ToolOutput[map[string]any]{}, nil
		}
		if err != nil {
			return toolFailure[map[string]any](req, err)
		}
		if !proceed {
			return toolFailure[map[string]any](req, ErrConfirmationInvalid)
		}
		return mutateAny(ctx, deps, req, specs[6].Name, ps.Principal, access.Locator{Path: in.ShareID}, func() (map[string]any, error) {
			if err := deps.MemberShares.Revoke(ctx, ps.Subject, in.ShareID); err != nil {
				return nil, err
			}
			return map[string]any{"shareId": in.ShareID, "revoked": true}, nil
		})
	})
	addTool(server, specs[7], func(ctx context.Context, req *mcp.CallToolRequest, in FileUpdateInput) (*mcp.CallToolResult, ToolOutput[UploadTicketOutput], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[UploadTicketOutput](req, err)
		}
		if err := requireHighRiskDeps(deps, false); err != nil || deps.TransferTickets == nil {
			if err == nil {
				err = errors.New("upload services are unavailable")
			}
			return toolFailure[UploadTicketOutput](req, err)
		}
		var expectedFingerprint string
		preview := func() (confirmation.Preview, error) {
			entry, previewErr := deps.MemberFiles.Preview(ctx, ps.Subject, in.locator(), aitoken.ScopeUploadsCreate)
			if previewErr != nil {
				return confirmation.Preview{}, previewErr
			}
			if entry.Kind != files.EntryKindFile {
				return confirmation.Preview{}, files.ErrNotFile
			}
			expectedFingerprint = entry.ObjectFingerprint
			cleanPath := safeRelativePath(in.Path)
			return impactFor(fmt.Sprintf("Replace content of %s", cleanPath), 1, entry.Size,
				map[string]any{"path": cleanPath, "expectedSize": in.ExpectedSize}, entry.ObjectFingerprint), nil
		}
		proceed, pending, err := deps.requireConfirmation(ctx, req, ps.Principal, specs[7].Name, preview)
		if pending != nil {
			return pending, ToolOutput[UploadTicketOutput]{}, nil
		}
		if err != nil {
			return toolFailure[UploadTicketOutput](req, err)
		}
		if !proceed {
			return toolFailure[UploadTicketOutput](req, ErrConfirmationInvalid)
		}
		intent, err := beginAudit(ctx, deps, req, specs[7].Name, ps.Principal, in.locator())
		if err != nil {
			return toolFailure[UploadTicketOutput](req, err)
		}
		upload, err := deps.MemberFiles.PrepareUpload(ctx, ps.Subject, memberfiles.UploadRequest{
			Locator: in.locator(), ExpectedSize: in.ExpectedSize, Checksum: in.Checksum,
			Overwrite: true, ExpectedObjectFingerprint: expectedFingerprint,
		})
		if err != nil {
			_ = finishAudit(ctx, deps, req, intent, "failed", ps.Principal, in.locator(), err)
			return toolFailure[UploadTicketOutput](req, err)
		}
		issued, err := deps.TransferTickets.IssueUpload(ctx, ps.Principal, upload.ID, in.locator(), upload.ExpectedSize)
		if err != nil {
			_ = deps.MemberFiles.CancelUpload(ctx, ps.Subject, upload.ID)
			_ = finishAudit(ctx, deps, req, intent, "failed", ps.Principal, in.locator(), err)
			return toolFailure[UploadTicketOutput](req, err)
		}
		out := UploadTicketOutput{ID: upload.ID, TargetPath: upload.TargetPath, ExpectedSize: upload.ExpectedSize,
			Checksum: upload.Checksum, PartSize: upload.PartSize, URLTemplate: issued.URL + "/parts/{partNumber}",
			Headers: map[string]string{"Authorization": "Bearer " + issued.BearerToken}, ExpiresAt: issued.ExpiresAt}
		if err := finishAudit(ctx, deps, req, intent, "succeeded", ps.Principal, in.locator(), nil); err != nil {
			return toolFailure[UploadTicketOutput](req, err)
		}
		return nil, ToolOutput[UploadTicketOutput]{Data: out}, nil
	})
}

func requireHighRiskDeps(deps ToolDependencies, shares bool) error {
	if deps.MemberFiles == nil {
		return errors.New("member files service is unavailable")
	}
	if shares && deps.MemberShares == nil {
		return errors.New("member share service is unavailable")
	}
	return nil
}

func mutateAny[T any](ctx context.Context, deps ToolDependencies, req *mcp.CallToolRequest, name string, principal aitoken.Principal, locator access.Locator, fn func() (T, error)) (*mcp.CallToolResult, ToolOutput[T], error) {
	intent, err := beginAudit(ctx, deps, req, name, principal, locator)
	if err != nil {
		return toolFailure[T](req, err)
	}
	item, err := fn()
	if err != nil {
		finishAudit(ctx, deps, req, intent, "failed", principal, locator, err)
		return toolFailure[T](req, err)
	}
	if err := finishAudit(ctx, deps, req, intent, "succeeded", principal, locator, nil); err != nil {
		return toolFailure[T](req, err)
	}
	return nil, ToolOutput[T]{Data: item}, nil
}

func findTrash(list memberfiles.TrashListResult, id string) (memberfiles.TrashResult, bool) {
	for _, item := range list.Items {
		if item.ID == id {
			return memberfiles.TrashResult{TrashID: item.ID, OriginalPath: item.OriginalPath, Name: item.Name, Size: item.Size, DeletedAt: item.DeletedAt}, true
		}
	}
	return memberfiles.TrashResult{}, false
}

func fingerprintFor(base string, destination access.Locator) string {
	return fingerprintBytes(struct {
		Base        string
		Destination access.Locator
	}{base, destination})
}
func fingerprintBytes(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func boolValue(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func safeRelativePath(value string) string {
	cleaned, err := storage.CleanRelativePath(value)
	if err != nil {
		return "<invalid>"
	}
	return cleaned
}
