package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditTargetValueRedactsContentPaths(t *testing.T) {
	for _, tc := range []struct {
		targetType string
		targetID   string
		path       string
	}{
		{targetType: "share", targetID: "shr_1", path: "private/contracts/acquisition.pdf"},
		{targetType: "upload", targetID: "upl_1", path: "finance/payroll.xlsx"},
		{targetType: "backup", targetID: "bak_1", path: "/srv/omnora/backups/private.tar"},
	} {
		if got := auditTargetValue(tc.targetType, tc.targetID, tc.path); got != tc.targetID {
			t.Fatalf("auditTargetValue(%q) = %q, want opaque id %q", tc.targetType, got, tc.targetID)
		}
	}
}

func TestAuditTargetValueKeepsSafeControlPlaneLabels(t *testing.T) {
	if got := auditTargetValue("account", "acct_1", "Administrator"); got != "Administrator" {
		t.Fatalf("auditTargetValue(account) = %q", got)
	}
	if got := auditTargetLabel("mount", "mnt_1", "Documents"); got != "mount Documents" {
		t.Fatalf("auditTargetLabel(mount) = %q", got)
	}
}

func TestAuditSurfacesRedactContentTargetPaths(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	_ = createTestSpaceAndMount(t, db, "space-audit", "mount-audit", admin.ID, "read_write")
	adminCookie := issueAPITestSession(t, db, admin.ID)

	const (
		sharePath  = "private/contracts/acquisition.pdf"
		uploadPath = "finance/payroll.xlsx"
		backupPath = "/srv/omnora/backups/private.tar"
	)
	if _, err := db.SQL().Exec(`
INSERT INTO shares(id, public_id, secret_hash, creator_account_id, space_id, mount_id, relative_path, expires_at)
VALUES ('shr-audit', 'pub-audit', 'hash', ?, 'space-audit', 'mount-audit', ?, '2099-01-01T00:00:00Z');
INSERT INTO upload_sessions(id, account_id, space_id, mount_id, target_relative_path, declared_size, part_size, temp_dir, expires_at)
VALUES ('upl-audit', ?, 'space-audit', 'mount-audit', ?, 1, 1, 'tmp-audit', '2099-01-01T00:00:00Z');
INSERT INTO backups(id, status, path, created_by, created_at, notes)
VALUES ('bak-audit', 'completed', ?, ?, '2026-08-04T15:00:00Z', 'privacy test');
INSERT INTO audit_events(actor_account_id, route_group, action, target_type, target_id, metadata_json)
VALUES
	(?, 'rest', 'privacy_share', 'share', 'shr-audit', '{}'),
	(?, 'rest', 'privacy_upload', 'upload', 'upl-audit', '{}'),
	(?, 'rest', 'privacy_backup', 'backup', 'bak-audit', '{}');
`, admin.ID, sharePath, admin.ID, uploadPath, backupPath, admin.ID, admin.ID, admin.ID, admin.ID); err != nil {
		t.Fatalf("insert privacy fixtures: %v", err)
	}

	rec := authorizedAPITestRequest(t, handler, "/api/v1/audit/events?limit=20", adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list audit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, secretPath := range []string{sharePath, uploadPath, backupPath} {
		if strings.Contains(rec.Body.String(), secretPath) {
			t.Fatalf("audit API leaked content path %q: %s", secretPath, rec.Body.String())
		}
	}
	var listed struct {
		Items []auditEventDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode audit events: %v", err)
	}
	assertAuditEventTargets(t, listed.Items, map[string]string{
		"privacy_share":  "shr-audit",
		"privacy_upload": "upl-audit",
		"privacy_backup": "bak-audit",
	})

	bootstrapRequest := httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	bootstrapItems := (&Server{}).bootstrapAudit(bootstrapRequest, db.SQL())
	for _, item := range bootstrapItems {
		for _, secretPath := range []string{sharePath, uploadPath, backupPath} {
			if strings.Contains(item.Target, secretPath) {
				t.Fatalf("bootstrap audit leaked content path %q in target %q", secretPath, item.Target)
			}
		}
	}
	assertBootstrapAuditTargets(t, bootstrapItems, map[string]string{
		"privacy_share":  "share shr-audit",
		"privacy_upload": "upload upl-audit",
		"privacy_backup": "backup bak-audit",
	})
}

func assertAuditEventTargets(t *testing.T, items []auditEventDTO, expected map[string]string) {
	t.Helper()
	for _, item := range items {
		want, ok := expected[item.Action]
		if !ok {
			continue
		}
		if item.TargetID != want || item.TargetLabel != want {
			t.Fatalf("audit event %s target = id %q label %q, want opaque id %q", item.Action, item.TargetID, item.TargetLabel, want)
		}
		delete(expected, item.Action)
	}
	if len(expected) != 0 {
		t.Fatalf("missing audit events: %#v", expected)
	}
}

func assertBootstrapAuditTargets(t *testing.T, items []auditDTO, expected map[string]string) {
	t.Helper()
	for _, item := range items {
		want, ok := expected[item.Event]
		if !ok {
			continue
		}
		if item.Target != want {
			t.Fatalf("bootstrap audit event %s target = %q, want %q", item.Event, item.Target, want)
		}
		delete(expected, item.Event)
	}
	if len(expected) != 0 {
		t.Fatalf("missing bootstrap audit events: %#v", expected)
	}
}
