package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadinessRemainsReadyAfterInitialization(t *testing.T) {
	db, handler := newAPITestServer(t)
	if _, err := db.SQL().Exec(`UPDATE system_state SET value = 'true' WHERE key = 'initialized'`); err != nil {
		t.Fatalf("mark initialized: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"ready"`) {
		t.Fatalf("/readyz body = %s, want ready status", rec.Body.String())
	}
}

func TestReadinessReportsMCPAuditDegraded(t *testing.T) {
	db, handler := newAPITestServer(t)
	if _, err := db.SQL().Exec(`
INSERT INTO system_state(key, value, updated_at)
VALUES ('mcp_audit_risk', '{"reason":"audit_write_failed","timestamp":"2026-08-06T00:00:00Z"}', CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
`); err != nil {
		t.Fatalf("mark MCP audit risk: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /readyz error: %v", err)
	}
	if body.Error.Code != "mcp_audit_degraded" || !strings.Contains(body.Error.Message, "mcp_audit_degraded") {
		t.Fatalf("/readyz error = %#v, want stable MCP audit degradation", body.Error)
	}
}

func TestReadinessReportsRecoveryNotReady(t *testing.T) {
	db, handler := newAPITestServer(t)
	if _, err := db.SQL().Exec(`UPDATE recovery_control SET ready = 0 WHERE id = 1`); err != nil {
		t.Fatalf("mark recovery control not ready: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"recovery_not_ready"`) {
		t.Fatalf("/readyz body = %s, want recovery_not_ready", rec.Body.String())
	}
}

func TestReadinessReportsAuditWriteRisk(t *testing.T) {
	db, handler := newAPITestServer(t)
	if _, err := db.SQL().Exec(`
INSERT INTO system_state(key, value, updated_at)
VALUES ('audit_write_risk', '{"reason":"audit_write_failed"}', CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
`); err != nil {
		t.Fatalf("mark audit risk: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"audit_degraded"`) {
		t.Fatalf("/readyz = %d %s, want audit_degraded", rec.Code, rec.Body.String())
	}
}
