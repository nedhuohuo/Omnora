package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"omnora/internal/identity"
	"omnora/internal/totp"
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

func TestChangeAccountPasswordRejectsInvalidTOTPInSameRequest(t *testing.T) {
	db, handler := newTOTPTestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	cookie := issueAPITestSession(t, db, admin.ID)

	setupReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/totp/setup", bytes.NewReader([]byte(`{}`)))
	setupReq.Header.Set("Content-Type", "application/json")
	setupReq.AddCookie(cookie)
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setupReq)
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("TOTP setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(setupRec.Body.Bytes(), &setup); err != nil {
		t.Fatalf("decode TOTP setup: %v", err)
	}
	code, err := totp.GenerateCode(setup.Secret, time.Now().UTC(), totp.Config{})
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	confirmBody, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatalf("marshal TOTP confirmation: %v", err)
	}
	confirmReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/totp/confirm", bytes.NewReader(confirmBody))
	confirmReq.Header.Set("Content-Type", "application/json")
	confirmReq.AddCookie(cookie)
	confirmRec := httptest.NewRecorder()
	handler.ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("TOTP confirmation status = %d, body = %s", confirmRec.Code, confirmRec.Body.String())
	}
	var rotatedCookie *http.Cookie
	for _, responseCookie := range confirmRec.Result().Cookies() {
		if responseCookie.Name == sessionCookieName {
			rotatedCookie = responseCookie
			break
		}
	}
	if rotatedCookie == nil {
		t.Fatalf("TOTP confirmation did not rotate the session: %#v", confirmRec.Result().Cookies())
	}

	body, err := json.Marshal(map[string]string{
		"currentPassword": apiTestPassword,
		"newPassword":     "NewCorrectHorse2!",
		"totpCode":        "invalid",
	})
	if err != nil {
		t.Fatalf("marshal password change: %v", err)
	}
	changeReq := httptest.NewRequest(http.MethodPatch, "/api/v1/account/password", bytes.NewReader(body))
	changeReq.Header.Set("Content-Type", "application/json")
	changeReq.AddCookie(rotatedCookie)
	changeRec := httptest.NewRecorder()
	handler.ServeHTTP(changeRec, changeReq)
	if changeRec.Code != http.StatusUnauthorized {
		t.Fatalf("password change with invalid TOTP status = %d, want %d, body = %s", changeRec.Code, http.StatusUnauthorized, changeRec.Body.String())
	}
}
