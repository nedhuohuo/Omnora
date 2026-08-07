package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRotateSessionPreservesAbsoluteExpiryAndAddsRecentReauthentication(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(db)
	ctx := context.Background()
	secret, err := svc.PrepareInitialization(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PrepareInitialization() error = %v", err)
	}
	created, err := svc.Initialize(ctx, InitializationRequest{
		Token: secret.Token, Email: "reauth@example.test", DisplayName: "Reauth", Password: "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	issued, err := svc.CreateSession(ctx, SessionRequest{
		AccountID: created.Account.ID, TTL: time.Hour, Purpose: SessionPurposeFull,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	rotated, err := svc.RotateSession(ctx, issued.Session, svc.now())
	if err != nil {
		t.Fatalf("RotateSession() error = %v", err)
	}
	if !rotated.Session.ExpiresAt.Equal(issued.Session.ExpiresAt) {
		t.Fatalf("rotated expiry = %s, want %s", rotated.Session.ExpiresAt, issued.Session.ExpiresAt)
	}
	if rotated.Session.ReauthenticatedAt.IsZero() {
		t.Fatal("rotated session has no reauthentication timestamp")
	}
	if _, err := svc.VerifySession(ctx, issued.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("old session error = %v, want ErrSessionInvalid", err)
	}
	verified, err := svc.VerifySession(ctx, rotated.Token)
	if err != nil {
		t.Fatalf("verify rotated session: %v", err)
	}
	if verified.ID != rotated.Session.ID || !verified.ReauthenticatedAt.Equal(rotated.Session.ReauthenticatedAt) {
		t.Fatalf("verified rotated session = %#v", verified)
	}
}

func TestRotateSessionRejectsEnrollmentSession(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(db)
	ctx := context.Background()
	secret, err := svc.PrepareInitialization(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PrepareInitialization() error = %v", err)
	}
	created, err := svc.Initialize(ctx, InitializationRequest{
		Token: secret.Token, Email: "enrollment@example.test", DisplayName: "Enrollment", Password: "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	// Initialization creates the first account as admin, so enrollment is a
	// valid purpose for this fixture.
	issued, err := svc.CreateSession(ctx, SessionRequest{
		AccountID: created.Account.ID, TTL: time.Hour, Purpose: SessionPurposeTOTPEnrollment,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, err := svc.RotateSession(ctx, issued.Session, svc.now()); !errors.Is(err, ErrEnrollmentSession) {
		t.Fatalf("RotateSession() error = %v, want ErrEnrollmentSession", err)
	}
}
