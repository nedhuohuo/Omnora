package server

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/mcpapi"
)

func newMCPAPIHandler(s *Server) http.Handler {
	return mcpapi.NewHandler(s.tokens, mcpapi.HandlerOptions{
		MaxBodyBytes: s.cfg.MCP.MaxBodyBytes,
		Configure: func(server *mcp.Server) {
			mcpapi.RegisterOrdinaryTools(server, mcpapi.ToolDependencies{
				MemberFiles: s.memberFiles, MemberShares: s.memberShares,
				TransferTickets: s.transferTickets, AuditRecorder: s.auditRecorder,
				AuditEnabled:  s.db != nil,
				Confirmations: s.confirmations,
			})
			mcpapi.RegisterHighRiskTools(server, mcpapi.ToolDependencies{
				MemberFiles: s.memberFiles, MemberShares: s.memberShares,
				TransferTickets: s.transferTickets, AuditRecorder: s.auditRecorder,
				AuditEnabled: s.db != nil, Confirmations: s.confirmations,
			})
		},
	})
}
