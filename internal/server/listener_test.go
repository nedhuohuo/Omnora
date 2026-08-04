package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestListenerManagerLifecycle(t *testing.T) {
	lm := NewListenerManager()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr1 := freePort(t)
	if err := lm.Start(EntryLAN, addr1, handler); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !lm.Active(EntryLAN) || lm.ActiveAddr(EntryLAN) != addr1 {
		t.Fatalf("active = %v, addr = %q, want active on %q", lm.Active(EntryLAN), lm.ActiveAddr(EntryLAN), addr1)
	}

	// Start with the same address is a no-op.
	if err := lm.Start(EntryLAN, addr1, handler); err != nil {
		t.Fatalf("Start same addr: %v", err)
	}
	if lm.ActiveAddr(EntryLAN) != addr1 {
		t.Fatalf("addr changed on no-op start = %q, want %q", lm.ActiveAddr(EntryLAN), addr1)
	}

	// Start with a different address rebinds and serves on the new one.
	addr2 := freePort(t)
	if err := lm.Start(EntryLAN, addr2, handler); err != nil {
		t.Fatalf("Start rebind: %v", err)
	}
	if lm.ActiveAddr(EntryLAN) != addr2 {
		t.Fatalf("addr after rebind = %q, want %q", lm.ActiveAddr(EntryLAN), addr2)
	}
	if res, err := http.Get("http://" + addr2 + "/"); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET on rebound listener: err = %v, status = %v", err, res)
	}

	// Stop clears state and closes the listener.
	if err := lm.Stop(EntryLAN); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if lm.Active(EntryLAN) {
		t.Fatal("listener should be inactive after Stop")
	}
	if _, err := http.Get("http://" + addr2 + "/"); err == nil {
		t.Fatal("GET after Stop should fail")
	}

	// Restart after Stop works.
	if err := lm.Start(EntryLAN, addr1, handler); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	if !lm.Active(EntryLAN) {
		t.Fatal("listener should be active after restart")
	}

	// Unknown entry reports inactive and Stop is a no-op.
	if lm.Active(EntryProxy) {
		t.Fatal("unused entry should report inactive")
	}
	if err := lm.Stop(EntryProxy); err != nil {
		t.Fatalf("Stop unused entry: %v", err)
	}

	// Start validates inputs.
	if err := lm.Start(EntryProxy, "", handler); err == nil {
		t.Fatal("Start with empty addr should fail")
	}
	if err := lm.Start(EntryProxy, freePort(t), nil); err == nil {
		t.Fatal("Start with nil handler should fail")
	}

	if err := lm.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestBindControllerRejectBadAddr(t *testing.T) {
	c := NewBindController()
	if err := c.Start("not a valid addr", http.NotFoundHandler()); err == nil {
		t.Fatal("Start with invalid addr should fail")
	}
}

func TestEntryGateRejectsUnknownEntry(t *testing.T) {
	_, s := newDualEntryTestServer(t)
	unknown := s.HandlerFor("not-an-entry")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
	req.RemoteAddr = "127.0.0.1:3456"
	rec := httptest.NewRecorder()
	unknown.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unknown entry status = %d, want 403", rec.Code)
	}
}
