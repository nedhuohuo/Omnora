package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestMemberSharesListRouteReturnsEmptyCollection(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	rec := authorizedAPITestRequest(t, handler, "/api/v1/shares", issueAPITestSession(t, db, admin.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("list shares status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode shares response: %v", err)
	}
	if response.Items == nil {
		t.Fatalf("items = nil, want an empty collection")
	}
}
