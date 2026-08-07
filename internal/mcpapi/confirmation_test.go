package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/aitoken"
	"omnora/internal/confirmation"
	"omnora/internal/store"
)

func TestHighRiskCatalogueExact(t *testing.T) {
	want := []string{"files.move", "files.trash", "trash.purge", "trash.empty", "files.delete_permanently", "shares.create", "shares.revoke"}
	specs := HighRiskToolSpecs()
	if len(specs) != len(want) {
		t.Fatalf("high-risk count = %d, want %d", len(specs), len(want))
	}
	for i, name := range want {
		if specs[i].Name != name || specs[i].Scope == "" || !specs[i].Destructive && name != "shares.create" {
			t.Fatalf("high-risk spec[%d] = %#v", i, specs[i])
		}
	}
	server := NewServer(nil)
	RegisterHighRiskTools(server, ToolDependencies{})
}

func TestReliableFormElicitationCapabilityRules(t *testing.T) {
	if reliableFormCapabilities(nil) {
		t.Fatal("nil capabilities should be rejected")
	}
	if reliableFormCapabilities(&mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}}) {
		t.Fatal("URL-only elicitation should be rejected")
	}
	if !reliableFormCapabilities(&mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{}}) {
		t.Fatal("legacy form default should be accepted")
	}
	if !reliableFormCapabilities(&mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}) {
		t.Fatal("explicit form should be accepted")
	}
}

func TestConfirmationGateRejectsMissingCapabilityWithoutMutation(t *testing.T) {
	called := false
	deps := ToolDependencies{}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"path":"x"}`)}}
	_, _, err := deps.requireConfirmation(context.Background(), req, testPrincipal(), "files.trash", func() (confirmation.Preview, error) { called = true; return confirmation.Preview{}, nil })
	if !errors.Is(err, ErrClientCapability) {
		t.Fatalf("requireConfirmation error = %v, want capability error", err)
	}
	if called {
		t.Fatal("preview/mutation callback ran without client capability")
	}
}

func testPrincipal() aitoken.Principal { return aitoken.Principal{AccountID: "acct", TokenID: "token"} }

func TestConfirmationGateMRTRAcceptsOnceAndDeclineDoesNotExecute(t *testing.T) {
	dbHandle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "mcp-confirm.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer dbHandle.Close()
	db := dbHandle.SQL()
	if _, err := db.Exec(`INSERT INTO accounts(id,email,display_name,role,status) VALUES ('acct-mcp','mcp@example.test','MCP','member','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO spaces(id,kind,name,owner_account_id,status) VALUES ('space-mcp','shared','MCP','acct-mcp','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mounts(id,space_id,display_name,root_path,kind,mode,status) VALUES ('mount-mcp','space-mcp','MCP','/tmp','managed','read_write','active')`); err != nil {
		t.Fatal(err)
	}
	issued, err := aitoken.NewService(db).Create(context.Background(), aitoken.CreateRequest{AccountID: "acct-mcp", Name: "mcp", Scopes: []aitoken.Scope{aitoken.ScopeFilesWrite}, Boundaries: []aitoken.DirectoryBoundary{{SpaceID: "space-mcp", MountID: "mount-mcp", RelativePath: "."}}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	principal := aitoken.Principal{AccountID: issued.Token.AccountID, TokenID: issued.Token.ID, PublicID: issued.Token.PublicID, Scopes: issued.Token.Scopes, ExpiresAt: issued.Token.ExpiresAt}
	confirmations := confirmation.NewService(db)
	var executed int
	server := mcp.NewServer(&mcp.Implementation{Name: "server", Version: "test"}, nil)
	server.AddTool(&mcp.Tool{Name: "danger", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deps := ToolDependencies{Confirmations: confirmations}
		proceed, pending, err := deps.requireConfirmation(ctx, req, principal, "danger", func() (confirmation.Preview, error) {
			return confirmation.Preview{ObjectFingerprint: "fp-1", Impact: confirmation.Impact{Summary: "Danger", ItemCount: 1}}, nil
		})
		if pending != nil {
			return pending, nil
		}
		if err != nil {
			result, _, toolErr := toolFailure[map[string]any](req, err)
			return result, toolErr
		}
		if proceed {
			executed++
		}
		return &mcp.CallToolResult{StructuredContent: map[string]any{"executed": proceed}}, nil
	})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	accepted := true
	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "test"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}, ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		if accepted {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmed": true}}, nil
		}
		return &mcp.ElicitResult{Action: "decline", Content: map[string]any{"confirmed": false}}, nil
	}})
	session, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "danger", Arguments: json.RawMessage(`{"x":1}`)})
	if err != nil || res.IsError || executed != 1 {
		t.Fatalf("accepted call err=%v result=%#v executed=%d", err, res, executed)
	}
	accepted = false
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "danger", Arguments: json.RawMessage(`{"x":2}`)})
	if err != nil || !res.IsError || executed != 1 {
		t.Fatalf("declined call err=%v result=%#v executed=%d", err, res, executed)
	}
}

func TestHighRiskConfirmationBindingMatrix(t *testing.T) {
	dbHandle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "mcp-matrix.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer dbHandle.Close()
	if _, err := dbHandle.SQL().Exec(`INSERT INTO accounts(id,email,display_name,role,status) VALUES ('acct-matrix','matrix@example.test','Matrix','member','active')`); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	if _, err := dbHandle.SQL().Exec(`INSERT INTO ai_tokens(id,public_id,secret_hash,account_id,name,scopes,created_at,expires_at,updated_at) VALUES ('aitok-matrix','ait-matrix','sha256:test','acct-matrix','matrix','["files:purge"]',CURRENT_TIMESTAMP,?,CURRENT_TIMESTAMP)`, expiresAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	principal := aitoken.Principal{AccountID: "acct-matrix", TokenID: "aitok-matrix", PublicID: "ait-matrix", Scopes: []aitoken.Scope{aitoken.ScopeFilesPurge}, ExpiresAt: expiresAt}
	service := confirmation.NewService(dbHandle.SQL())
	for _, spec := range HighRiskToolSpecs() {
		t.Run(spec.Name, func(t *testing.T) {
			preview := confirmation.Preview{ToolName: spec.Name, Args: json.RawMessage(`{"path":"docs/a.txt"}`), ObjectFingerprint: "fp-" + spec.Name}
			challenge, err := service.Begin(context.Background(), principal, preview)
			if err != nil {
				t.Fatal(err)
			}
			state := challenge.RequestState
			for name, mutate := range map[string]func(confirmation.ConsumeRequest, aitoken.Principal) (confirmation.ConsumeRequest, aitoken.Principal){
				"changed args": func(req confirmation.ConsumeRequest, p aitoken.Principal) (confirmation.ConsumeRequest, aitoken.Principal) {
					req.Args = json.RawMessage(`{"path":"docs/b.txt"}`)
					return req, p
				},
				"changed object": func(req confirmation.ConsumeRequest, p aitoken.Principal) (confirmation.ConsumeRequest, aitoken.Principal) {
					req.ObjectFingerprint = "changed"
					return req, p
				},
				"changed token": func(req confirmation.ConsumeRequest, p aitoken.Principal) (confirmation.ConsumeRequest, aitoken.Principal) {
					p.TokenID = "other-token"
					return req, p
				},
			} {
				t.Run(name, func(t *testing.T) {
					req, p := mutate(confirmation.ConsumeRequest{RequestState: state, ToolName: spec.Name, Args: preview.Args, ObjectFingerprint: preview.ObjectFingerprint, Decision: "accept"}, principal)
					if err := service.Consume(context.Background(), p, req); err == nil {
						t.Fatal("binding drift unexpectedly accepted")
					}
				})
			}
			if err := service.Consume(context.Background(), principal, confirmation.ConsumeRequest{RequestState: state, ToolName: spec.Name, Args: preview.Args, ObjectFingerprint: preview.ObjectFingerprint, Decision: "accept"}); err != nil {
				t.Fatalf("accept = %v", err)
			}
			if err := service.Consume(context.Background(), principal, confirmation.ConsumeRequest{RequestState: state, ToolName: spec.Name, Args: preview.Args, ObjectFingerprint: preview.ObjectFingerprint, Decision: "accept"}); !errors.Is(err, confirmation.ErrConsumed) {
				t.Fatalf("replay = %v, want ErrConsumed", err)
			}
		})
	}

	// A concurrent retry can execute at most once for one challenge.
	preview := confirmation.Preview{ToolName: "files.move", Args: json.RawMessage(`{"path":"docs/a.txt"}`), ObjectFingerprint: "fp-race"}
	challenge, err := service.Begin(context.Background(), principal, preview)
	if err != nil {
		t.Fatal(err)
	}
	dbHandle.SQL().SetMaxOpenConns(8)
	req := confirmation.ConsumeRequest{RequestState: challenge.RequestState, ToolName: preview.ToolName, Args: preview.Args, ObjectFingerprint: preview.ObjectFingerprint, Decision: "accept"}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- service.Consume(context.Background(), principal, req) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successes = %d, want 1", successes)
	}

	// Expiry is checked before acceptance and never executes the operation.
	clock := time.Now().UTC()
	expiring := confirmation.NewService(dbHandle.SQL(), confirmation.WithClock(func() time.Time { return clock }))
	expired, err := expiring.Begin(context.Background(), principal, confirmation.Preview{ToolName: "files.trash", Args: []byte(`{}`), ObjectFingerprint: "fp-expired"})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(3 * time.Minute)
	if err := expiring.Consume(context.Background(), principal, confirmation.ConsumeRequest{RequestState: expired.RequestState, ToolName: "files.trash", Args: []byte(`{}`), ObjectFingerprint: "fp-expired", Decision: "accept"}); !errors.Is(err, confirmation.ErrExpired) {
		t.Fatalf("expired consume = %v, want ErrExpired", err)
	}
	if strings.Contains(expired.RequestState, "sha256:") {
		t.Fatal("request state contains persisted secret hash")
	}
}
