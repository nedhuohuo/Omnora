package audit

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"omnora/internal/domain"

	_ "modernc.org/sqlite"
)

func TestRecorderWritesNormalizedMetadata(t *testing.T) {
	db := newAuditDB(t)
	metadata, err := MetadataFromMap(map[string]any{"result": "ok", "count": float64(2)})
	if err != nil {
		t.Fatalf("MetadataFromMap() error = %v", err)
	}

	err = NewRecorder(db).Record(context.Background(), Event{
		ActorAccountID: "acct_1",
		RouteGroup:     domain.RouteGroupREST,
		Action:         "mount_scan",
		TargetType:     "mount",
		TargetID:       "mnt_1",
		IPHash:         HashForAudit("127.0.0.1"),
		UserAgentHash:  HashForAudit("test-agent"),
		MetadataJSON:   metadata,
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	var action, storedMetadata, ipHash string
	if err := db.QueryRow("SELECT action, metadata_json, ip_hash FROM audit_events").Scan(&action, &storedMetadata, &ipHash); err != nil {
		t.Fatalf("query audit event: %v", err)
	}
	if action != "mount_scan" {
		t.Fatalf("action = %q", action)
	}
	if storedMetadata != `{"count":2,"result":"ok"}` {
		t.Fatalf("metadata_json = %q", storedMetadata)
	}
	if ipHash == "" || ipHash == "127.0.0.1" {
		t.Fatalf("ip_hash = %q, want non-empty hash", ipHash)
	}
}

func TestRecorderRejectsSensitiveMetadata(t *testing.T) {
	db := newAuditDB(t)
	tests := []string{
		`{"password":"hunter2"}`,
		`{"nested":{"apiToken":"abc"}}`,
		`{"note":"Bearer abc"}`,
		`{"url":"https://omnora.example/share/public-secret"}`,
	}

	for _, metadata := range tests {
		t.Run(metadata, func(t *testing.T) {
			err := NewRecorder(db).Record(context.Background(), Event{
				Action:       "share_access",
				TargetType:   "share",
				MetadataJSON: metadata,
			})
			if !errors.Is(err, ErrSensitiveMetadata) {
				t.Fatalf("Record() error = %v, want ErrSensitiveMetadata", err)
			}
		})
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(1) FROM audit_events").Scan(&count); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 0 {
		t.Fatalf("audit event count = %d, want 0", count)
	}
}

func TestRedactValueRedactsObviousSecrets(t *testing.T) {
	if got := RedactValue("access_token", "abc"); got != "[redacted]" {
		t.Fatalf("RedactValue(access_token) = %v", got)
	}
	if got := RedactValue("result", "ok"); got != "ok" {
		t.Fatalf("RedactValue(result) = %v", got)
	}
}

func newAuditDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
CREATE TABLE audit_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	occurred_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	actor_account_id TEXT,
	route_group TEXT,
	action TEXT NOT NULL,
	target_type TEXT NOT NULL,
	target_id TEXT,
	ip_hash TEXT,
	user_agent_hash TEXT,
	metadata_json TEXT NOT NULL DEFAULT '{}'
);
`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}
