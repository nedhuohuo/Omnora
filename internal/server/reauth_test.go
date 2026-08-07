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

func TestReauthenticateRotatesMemberSessionWithoutExtendingExpiry(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	oldCookie := issueAPITestSession(t, db, member.ID)
	oldSession, err := identity.New(db.SQL(), identity.Options{}).VerifySession(context.Background(), oldCookie.Value)
	if err != nil {
		t.Fatalf("verify original session: %v", err)
	}

	body, err := json.Marshal(map[string]string{"password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal reauthenticate request: %v", err)
	}
	reauth := httptest.NewRequest(http.MethodPost, "/api/v1/account/reauthenticate", bytes.NewReader(body))
	reauth.Header.Set("Content-Type", "application/json")
	reauth.AddCookie(oldCookie)
	reauthRec := httptest.NewRecorder()
	handler.ServeHTTP(reauthRec, reauth)
	if reauthRec.Code != http.StatusOK {
		t.Fatalf("reauthenticate status = %d, body = %s", reauthRec.Code, reauthRec.Body.String())
	}
	var response struct {
		Status               string `json:"status"`
		ReauthenticatedUntil string `json:"reauthenticatedUntil"`
	}
	if err := json.Unmarshal(reauthRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode reauthenticate response: %v", err)
	}
	if response.Status != "reauthenticated" || response.ReauthenticatedUntil == "" {
		t.Fatalf("reauthenticate response = %#v", response)
	}
	cookies := reauthRec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("reauthenticate cookies = %#v", cookies)
	}
	newSession, err := identity.New(db.SQL(), identity.Options{}).VerifySession(context.Background(), cookies[0].Value)
	if err != nil {
		t.Fatalf("verify rotated session: %v", err)
	}
	if !newSession.ExpiresAt.Equal(oldSession.ExpiresAt) {
		t.Fatalf("rotated expiry = %s, want %s", newSession.ExpiresAt, oldSession.ExpiresAt)
	}
	if newSession.ReauthenticatedAt.IsZero() {
		t.Fatal("rotated session has no reauthentication timestamp")
	}
	if _, err := identity.New(db.SQL(), identity.Options{}).VerifySession(context.Background(), oldCookie.Value); err == nil {
		t.Fatal("old session remained valid after reauthentication")
	}
}

func TestReauthenticateRejectsEnrollmentSession(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	loginBody, err := json.Marshal(map[string]string{"login": admin.Email, "password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal login: %v", err)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", bytes.NewReader(loginBody))
	login.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusCreated {
		t.Fatalf("enrollment login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("enrollment login cookies = %#v", cookies)
	}
	reauthBody, err := json.Marshal(map[string]string{"password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal reauthenticate: %v", err)
	}
	reauth := httptest.NewRequest(http.MethodPost, "/api/v1/account/reauthenticate", bytes.NewReader(reauthBody))
	reauth.Header.Set("Content-Type", "application/json")
	reauth.AddCookie(cookies[0])
	reauthRec := httptest.NewRecorder()
	handler.ServeHTTP(reauthRec, reauth)
	if reauthRec.Code != http.StatusForbidden || !bytes.Contains(reauthRec.Body.Bytes(), []byte(`"code":"enrollment_session"`)) {
		t.Fatalf("enrollment reauthenticate = %d %s", reauthRec.Code, reauthRec.Body.String())
	}
}
