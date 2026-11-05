package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteSessionUsesSlidingIdleExpiry(t *testing.T) {
	repository, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	user, _, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := repository.CreateLocalSession(t.Context(), user.ID, testSessionIdleTTL, domain.SessionMeta{DeviceID: "idle-test"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := repository.db.ExecContext(t.Context(), `
		UPDATE web_sessions SET created_at = ?, idle_expires_at = ? WHERE id = ?
	`, sqliteTimeValue(now.Add(-365*24*time.Hour)), sqliteTimeValue(now.Add(time.Minute)), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(t.Context(), session.Token, testSessionIdleTTL, testSessionRotationInterval, testSessionPreviousTokenTTL); err != nil {
		t.Fatalf("old but active session failed: %v", err)
	}
	items, err := repository.ListSessions(t.Context(), user.ID, session.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("sessions=%d err=%v", len(items), err)
	}
	remaining := time.Until(items[0].IdleExpiresAt)
	if remaining < testSessionIdleTTL-time.Minute || remaining > testSessionIdleTTL+time.Minute {
		t.Fatalf("invalid sliding window: %s", remaining)
	}
}

func TestSQLiteSessionCannotBeRevivedAfterIdleExpiry(t *testing.T) {
	repository, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	user, _, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := repository.CreateLocalSession(t.Context(), user.ID, testSessionIdleTTL, domain.SessionMeta{DeviceID: "expired-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(t.Context(), `UPDATE web_sessions SET idle_expires_at = ? WHERE id = ?`, sqliteTimeValue(time.Now().Add(-time.Second)), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(t.Context(), session.Token, testSessionIdleTTL, testSessionRotationInterval, testSessionPreviousTokenTTL); err == nil {
		t.Fatal("idle-expired session must not authenticate")
	}
}
