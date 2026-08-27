package mcpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/aitoken"
)

func TestTokenVerifierRejectsInvalidBearer(t *testing.T) {
	verify := TokenVerifier(nil)
	if _, err := verify(context.Background(), "not-a-token", httptest.NewRequest(http.MethodPost, "/mcp", nil)); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("TokenVerifier() error = %v, want auth.ErrInvalidToken", err)
	}
}

func TestPrincipalFromRequestRequiresServerAttachedPrincipal(t *testing.T) {
	req := &mcp.CallToolRequest{}
	if _, err := PrincipalFromRequest(req); !errors.Is(err, ErrMissingPrincipal) {
		t.Fatalf("PrincipalFromRequest() error = %v, want ErrMissingPrincipal", err)
	}

	principal := aitoken.Principal{AccountID: "acct-1", TokenID: "token-1"}
	req.Extra = &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{
		Extra: map[string]any{PrincipalExtraKey: principal},
	}}
	got, err := PrincipalFromRequest(req)
	if err != nil {
		t.Fatalf("PrincipalFromRequest() error = %v", err)
	}
	if got.AccountID != principal.AccountID || got.TokenID != principal.TokenID {
		t.Fatalf("principal = %#v, want %#v", got, principal)
	}
}

func TestNewServerUsesOmnoraImplementation(t *testing.T) {
	server := NewServer(nil)
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}
}
