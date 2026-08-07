package mcpapi

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/aitoken"
)

// implementationVersion is the MCP server implementation version exposed
// during protocol initialization. It remains local until product release
// versioning centralizes it.
const implementationVersion = "0.1.0"

// HandlerOptions configures the standard Streamable HTTP transport. Configure
// is called for each stateless request-created SDK server, allowing later tool
// catalog tasks to register handlers without adding another protocol adapter.
type HandlerOptions struct {
	MaxBodyBytes int64
	Configure    func(*mcp.Server)
}

// TokenVerifier adapts Omnora's AI Token service to the SDK's bearer verifier.
// VerifyBearer remains the sole credential parser and live token validator.
func TokenVerifier(tokens *aitoken.Service) auth.TokenVerifier {
	return func(ctx context.Context, bearer string, _ *http.Request) (*auth.TokenInfo, error) {
		if tokens == nil {
			return nil, auth.ErrInvalidToken
		}
		principal, err := tokens.VerifyBearer(ctx, bearer)
		if err != nil {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{
			UserID:     principal.AccountID,
			Scopes:     scopeStrings(principal.Scopes),
			Expiration: principal.ExpiresAt,
			Extra:      map[string]any{PrincipalExtraKey: principal},
		}, nil
	}
}

func scopeStrings(scopes []aitoken.Scope) []string {
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		result = append(result, string(scope))
	}
	return result
}

// NewServer creates a fresh official SDK server. Stateless HTTP requests use
// one instance per request; this keeps request state out of process globals.
func NewServer(_ *aitoken.Service, configure ...func(*mcp.Server)) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "omnora", Version: implementationVersion},
		&mcp.ServerOptions{},
	)
	for _, setup := range configure {
		if setup != nil {
			setup(server)
		}
	}
	return server
}

// NewHandler creates the standard stateless Streamable HTTP MCP endpoint.
// Large file bytes are intentionally excluded; transfer routes are owned by
// the server package and use separate short-lived transfer tickets.
func NewHandler(tokens *aitoken.Service, opts HandlerOptions) http.Handler {
	maxBodyBytes := opts.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1 << 20
	}
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			return NewServer(tokens, opts.Configure)
		},
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			MaxRequestBodyBytes:          maxBodyBytes,
			PropagateRequestCancellation: true,
		},
	)
	// Omnora tokens may never expire; the SDK middleware must accept a
	// missing Expiration. Real expiry is still enforced by VerifyBearer, which
	// rejects tokens whose stored expiration has passed.
	return auth.RequireBearerToken(TokenVerifier(tokens), &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(streamable)
}

// NewHandlerWithLimit is a compact convenience for callers that do not need
// to register tools yet.
func NewHandlerWithLimit(tokens *aitoken.Service, maxBodyBytes int64) http.Handler {
	return NewHandler(tokens, HandlerOptions{MaxBodyBytes: maxBodyBytes})
}
