package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/config"
	"omnora/internal/domain"
)

func TestCreateAndRestoreBackup(t *testing.T) {
	db, _ := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)

	managed := filepath.Join(t.TempDir(), "managed")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatalf("mkdir managed: %v", err)
	}
	handler := New(config.Config{
		Database: config.DatabaseConfig{Path: "configured"},
		Storage:  config.StorageConfig{ManagedDir: managed},
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
		RouteEnvOverrides: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
	}, db)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups", nil)
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create backup status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created backupDTO
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create backup: %v", err)
	}
	if created.Status != "completed" || created.Path == "" || created.Notes != "sqlite online backup" {
		t.Fatalf("created backup = %#v", created)
	}
	if created.CreatedBy != admin.ID || created.CreatedByLabel != "Admin" || created.CreatedByEmail != admin.Email {
		t.Fatalf("created backup creator = %#v, want readable admin labels", created)
	}

	listRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/backups", adminCookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list backups status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Items []backupDTO `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed backups: %v", err)
	}
	if len(listed.Items) == 0 || listed.Items[0].CreatedByLabel != "Admin" || listed.Items[0].CreatedByEmail != admin.Email {
		t.Fatalf("listed backup creator = %#v, want readable admin labels", listed.Items)
	}

	restoreBody, err := json.Marshal(map[string]string{"confirmPhrase": "RESTORE"})
	if err != nil {
		t.Fatalf("marshal restore: %v", err)
	}
	restoreReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups/"+created.ID+"/restore", bytes.NewReader(restoreBody))
	restoreReq.Header.Set("Content-Type", "application/json")
	restoreReq.AddCookie(adminCookie)
	restoreRec := httptest.NewRecorder()
	handler.ServeHTTP(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", restoreRec.Code, restoreRec.Body.String())
	}
}
