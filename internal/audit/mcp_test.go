package audit

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMCPAuditIntentAndOutcome(t *testing.T) {
	db := newMCPAuditDB(t)
	recorder := NewRecorder(db)

	intentID, err := recorder.RecordMCPIntent(context.Background(), MCPEvent{
		AccountID:          "acct_1",
		CredentialPublicID: "tok_pub_1",
		ToolName:           "files.rename",
		RequestID:          "req_1",
		TraceID:            "trace_1",
		TargetType:         "member_file",
		TargetID:           "mount_1:docs/readme.md",
		MetadataJSON:       `{"object":"mount_1:docs/readme.md","mode":"rename"}`,
	})
	if err != nil {
		t.Fatalf("RecordMCPIntent() error = %v", err)
	}
	if intentID <= 0 {
		t.Fatalf("intent id = %d, want positive id", intentID)
	}

	if err := recorder.RecordMCPOutcome(context.Background(), intentID, MCPEvent{
		AccountID:          "acct_1",
		CredentialPublicID: "tok_pub_1",
		ToolName:           "files.rename",
		Result:             "succeeded",
		RequestID:          "req_1",
		TraceID:            "trace_1",
		TargetType:         "member_file",
		TargetID:           "mount_1:docs/readme.md",
		MetadataJSON:       `{"changed":true}`,
	}); err != nil {
		t.Fatalf("RecordMCPOutcome() error = %v", err)
	}

	var (
		credentialID, toolName, targetType, targetID, requestID, traceID, result, metadata string
		parentID                                                                           sql.NullInt64
	)
	rows, err := db.Query(`
SELECT credential_public_id, tool_name, target_type, target_id, request_id, trace_id, result, metadata_json, parent_event_id
FROM audit_events ORDER BY id`)
	if err != nil {
		t.Fatalf("query MCP audit rows: %v", err)
	}
	defer rows.Close()
	var got int
	for rows.Next() {
		got++
		if err := rows.Scan(&credentialID, &toolName, &targetType, &targetID, &requestID, &traceID, &result, &metadata, &parentID); err != nil {
			t.Fatalf("scan MCP audit row: %v", err)
		}
		if credentialID != "tok_pub_1" || toolName != "files.rename" || targetType != "member_file" || targetID != "mount_1:docs/readme.md" || requestID != "req_1" || traceID != "trace_1" {
			t.Fatalf("MCP audit identity fields = %q %q %q %q %q %q", credentialID, toolName, targetType, targetID, requestID, traceID)
		}
		if strings.Contains(metadata, "token") || strings.Contains(metadata, "secret") || strings.Contains(metadata, "password") {
			t.Fatalf("MCP audit metadata contains a secret-bearing field: %s", metadata)
		}
		if got == 1 {
			if result != "intent" || parentID.Valid {
				t.Fatalf("intent row result=%q parent=%v", result, parentID)
			}
		} else if result != "succeeded" || !parentID.Valid || parentID.Int64 != intentID {
			t.Fatalf("outcome row result=%q parent=%v, want parent %d", result, parentID, intentID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate MCP audit rows: %v", err)
	}
	if got != 2 {
		t.Fatalf("MCP audit rows = %d, want 2", got)
	}
}

func TestMCPAuditRejectsSecretBearingMetadata(t *testing.T) {
	db := newMCPAuditDB(t)
	recorder := NewRecorder(db)
	for _, metadata := range []string{
		`{"token":"bearer-secret"}`,
		`{"file_content":"contents must never be audited"}`,
		`{"fileContent":"contents must never be audited"}`,
		`{"host_path":"/srv/omnora/private/file.txt"}`,
	} {
		t.Run(metadata, func(t *testing.T) {
			if _, err := recorder.RecordMCPIntent(context.Background(), MCPEvent{ToolName: "files.read", TargetType: "member_file", MetadataJSON: metadata}); !errors.Is(err, ErrSensitiveMetadata) {
				t.Fatalf("RecordMCPIntent() error = %v, want ErrSensitiveMetadata", err)
			}
		})
	}
}

func TestMCPAuditIntentFailurePreventsExecution(t *testing.T) {
	db := newMCPAuditDB(t)
	recorder := NewRecorder(db)
	executed := false
	if _, err := recorder.RecordMCPIntent(context.Background(), MCPEvent{
		ToolName:     "files.delete_permanently",
		TargetType:   "member_file",
		MetadataJSON: `{"token":"must-fail"}`,
	}); err == nil {
		executed = true
	}
	if executed {
		t.Fatal("operation executed after MCP intent audit failure")
	}
}

func TestMCPAuditOutcomeFailureMarksReadinessRisk(t *testing.T) {
	db := newMCPAuditDB(t)
	recorder := NewRecorder(db)
	intentID, err := recorder.RecordMCPIntent(context.Background(), MCPEvent{ToolName: "files.move", TargetType: "member_file"})
	if err != nil {
		t.Fatalf("RecordMCPIntent() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_mcp_outcome BEFORE INSERT ON audit_events WHEN NEW.parent_event_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'write /srv/private failed'); END`); err != nil {
		t.Fatalf("create outcome failure trigger: %v", err)
	}
	if err := recorder.RecordMCPOutcome(context.Background(), intentID, MCPEvent{ToolName: "files.move", TargetType: "member_file", Result: "succeeded"}); err == nil {
		t.Fatal("RecordMCPOutcome() error = nil, want simulated write failure")
	} else if err := recorder.MarkMCPReadinessRisk(context.Background(), err); err != nil {
		t.Fatalf("MarkMCPReadinessRisk() error = %v", err)
	}

	var risk string
	if err := db.QueryRow(`SELECT value FROM system_state WHERE key = 'mcp_audit_risk'`).Scan(&risk); err != nil {
		t.Fatalf("query readiness risk: %v", err)
	}
	if strings.Contains(risk, "/srv/private") || strings.Contains(risk, "write /srv") {
		t.Fatalf("readiness risk leaked original error: %s", risk)
	}
	if !strings.Contains(risk, "audit_write_failed") || !strings.Contains(risk, "timestamp") {
		t.Fatalf("readiness risk = %s, want sanitized class and timestamp", risk)
	}
}

func newMCPAuditDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
CREATE TABLE system_state (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE audit_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	occurred_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	actor_account_id TEXT,
	route_group TEXT,
	action TEXT NOT NULL,
	target_type TEXT NOT NULL,
	target_id TEXT,
	metadata_json TEXT NOT NULL DEFAULT '{}',
	credential_public_id TEXT,
	tool_name TEXT,
	result TEXT,
	request_id TEXT,
	trace_id TEXT,
	parent_event_id INTEGER REFERENCES audit_events(id) ON DELETE SET NULL
);
`)
	if err != nil {
		t.Fatalf("create MCP audit schema: %v", err)
	}
	return db
}
