package mcpapi

import (
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/aitoken"
)

// PrincipalExtraKey is the private, in-process key used to carry the full
// verified AI Token principal through the SDK request metadata. Tool handlers
// must use this value instead of trusting the SDK's scope slice alone.
const PrincipalExtraKey = "omnora.principal"

// PrincipalFromRequest returns the exact principal attached by TokenVerifier.
// It deliberately rejects a missing or malformed SDK TokenInfo rather than
// reconstructing authorization from user-controlled request arguments.
func PrincipalFromRequest(req *mcp.CallToolRequest) (aitoken.Principal, error) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.Extra == nil {
		return aitoken.Principal{}, ErrMissingPrincipal
	}
	raw, ok := req.Extra.TokenInfo.Extra[PrincipalExtraKey]
	if !ok {
		return aitoken.Principal{}, ErrMissingPrincipal
	}
	switch principal := raw.(type) {
	case aitoken.Principal:
		if principal.AccountID == "" || principal.TokenID == "" {
			return aitoken.Principal{}, ErrInvalidPrincipal
		}
		return principal, nil
	case *aitoken.Principal:
		if principal == nil || principal.AccountID == "" || principal.TokenID == "" {
			return aitoken.Principal{}, ErrInvalidPrincipal
		}
		return *principal, nil
	default:
		return aitoken.Principal{}, errors.Join(ErrInvalidPrincipal, errors.New("unexpected principal type"))
	}
}
