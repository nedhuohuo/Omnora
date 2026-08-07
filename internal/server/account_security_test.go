package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"omnora/internal/identity"
)

func TestChangeAccountPassword(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)

	const newPassword = "NewCorrectHorse2!"

	wrongBody, err := json.Marshal(map[string]any{
		"currentPassword": "definitely-wrong",
		"newPassword":     newPassword,
	})
	if err != nil {
		t.Fatalf("marshal wrong password request: %v", err)
	}
	wrongReq := httptest.NewRequest(http.MethodPatch, "/api/v1/account/password", bytes.NewReader(wrongBody))
	wrongReq.Header.Set("Content-Type", "application/json")
	wrongReq.AddCookie(adminCookie)
	wrongRec := httptest.NewRecorder()
	handler.ServeHTTP(wrongRec, wrongReq)
	if wrongRec.Code != http.StatusUnauthorized {
		t.Fatalf("change password with wrong current password status = %d, want %d, body = %s", wrongRec.Code, http.StatusUnauthorized, wrongRec.Body.String())
	}

	correctBody, err := json.Marshal(map[string]any{
		"currentPassword": apiTestPassword,
		"newPassword":     newPassword,
	})
	if err != nil {
		t.Fatalf("marshal correct password request: %v", err)
	}
	correctReq := httptest.NewRequest(http.MethodPatch, "/api/v1/account/password", bytes.NewReader(correctBody))
	correctReq.Header.Set("Content-Type", "application/json")
	correctReq.AddCookie(adminCookie)
	correctRec := httptest.NewRecorder()
	handler.ServeHTTP(correctRec, correctReq)
	if correctRec.Code != http.StatusOK {
		t.Fatalf("change password status = %d, body = %s", correctRec.Code, correctRec.Body.String())
	}

	svc := identity.New(db.SQL(), identity.Options{})
	if _, err := svc.Authenticate(context.Background(), admin.Email, apiTestPassword); err == nil {
		t.Fatalf("expected old password to no longer authenticate")
	}
	if _, err := svc.Authenticate(context.Background(), admin.Email, newPassword); err != nil {
		t.Fatalf("expected new password to authenticate, got error: %v", err)
	}
}

func TestAdminCanDisableOwnTOTP(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	reqBody, err := json.Marshal(map[string]string{"password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal disable TOTP request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/totp/disable", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(issueAPITestSession(t, db, admin.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin disable TOTP status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
