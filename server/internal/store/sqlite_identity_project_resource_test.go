package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteTimeValueIsFixedWidthAndLexicallyOrdered(t *testing.T) {
	base := time.Date(2026, time.August, 7, 1, 2, 3, 0, time.FixedZone("offset", 8*60*60))
	earlier := sqliteTimeValue(base)
	later := sqliteTimeValue(base.Add(time.Nanosecond))
	if earlier != "2026-08-06T17:02:03.000000000Z" || len(earlier) != len(later) || earlier >= later {
		t.Fatalf("timestamps are not canonical/ordered: earlier=%q later=%q", earlier, later)
	}
	parsed, err := sqliteParseTime(later)
	if err != nil || !parsed.Equal(base.Add(time.Nanosecond)) {
		t.Fatalf("canonical timestamp did not round trip: parsed=%v err=%v", parsed, err)
	}
}

func TestSQLiteParseTimeAcceptsLegacyFractionWidths(t *testing.T) {
	cases := map[string]string{
		"2026-08-08T16:32:54.884Z":       "2026-08-08T16:32:54.884Z",
		"2026-08-08T16:32:54.884123Z":    "2026-08-08T16:32:54.884123Z",
		"2026-08-08T16:32:54.884123000Z": "2026-08-08T16:32:54.884123Z",
	}
	for value, expected := range cases {
		parsed, err := sqliteParseTime(value)
		if err != nil {
			t.Fatalf("sqliteParseTime(%q): %v", value, err)
		}
		if actual := parsed.UTC().Format(time.RFC3339Nano); actual != expected {
			t.Fatalf("sqliteParseTime(%q) = %q, want %q", value, actual, expected)
		}
	}
}

func TestSQLiteIdentityProjectResourceSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	repository, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := repository.GetUserPreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferences, err = repository.UpdateUserPreferences(
		ctx, user.ID, preferences.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	installationHMAC := repository.HMACValue("127.0.0.1")
	_, session, err := repository.CreateLocalSession(ctx, user.ID, 24*time.Hour, domain.SessionMeta{
		DeviceID: "restart-test", IPHMAC: installationHMAC,
		UserAgentHMAC: repository.HMACValue("test-agent"),
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := repository.CreateProject(ctx, user.ID, domain.Project{
		ComputerID: computer.ID, Name: "durable project", Description: "before restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := repository.CreateResource(ctx, user.ID, domain.ProjectResource{
		ProjectID: project.ID, Kind: "code_directory", Name: "source", Path: "/tmp/restart-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	resource, err = repository.UpdateResource(ctx, user.ID, resource.ID, resource.Version, func(item *domain.ProjectResource) {
		item.Status = "ready"
		item.GitDetected = true
	})
	if err != nil {
		t.Fatal(err)
	}
	var rawTokenRows, hashedTokenRows int
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM web_sessions WHERE token_hash = ?`, session.Token).Scan(&rawTokenRows); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRowContext(ctx, `SELECT count(*) FROM web_sessions WHERE token_hash = ?`, hashToken(session.Token)).Scan(&hashedTokenRows); err != nil {
		t.Fatal(err)
	}
	if rawTokenRows != 0 || hashedTokenRows != 1 {
		t.Fatalf("session token persistence is unsafe: raw=%d hashed=%d", rawTokenRows, hashedTokenRows)
	}
	var secretCount int
	if err := repository.db.QueryRowContext(ctx, `
		SELECT count(*) FROM app_metadata WHERE key IN ('install_csrf_key', 'install_hmac_key')
	`).Scan(&secretCount); err != nil {
		t.Fatal(err)
	}
	if secretCount != 2 {
		t.Fatalf("expected two installation secrets, got %d", secretCount)
	}
	if err := repository.Health(ctx); err != nil {
		t.Fatalf("quick_check before restart: %v", err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close SQLite: %v", err)
		}
	}()
	authentication, err := repository.AuthenticateSession(
		ctx, session.Token, 24*time.Hour, 15*time.Minute, 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	authenticated, info := authentication.User, authentication.Session
	if authenticated.ID != user.ID || info.ID != session.ID {
		t.Fatalf("unexpected restored session: user=%q session=%q", authenticated.ID, info.ID)
	}
	csrf, err := repository.GetSessionCSRF(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if csrf != session.CSRF || !repository.VerifySessionCSRF(ctx, session.ID, csrf) {
		t.Fatal("installation CSRF secret did not survive restart")
	}
	if repository.HMACValue("127.0.0.1") != installationHMAC {
		t.Fatal("installation HMAC secret did not survive restart")
	}
	restoredPreferences, err := repository.GetUserPreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredPreferences != preferences {
		t.Fatalf("preferences changed after restart: %#v != %#v", restoredPreferences, preferences)
	}
	restoredProject, err := repository.GetProject(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredProject.Name != project.Name || restoredProject.Version != project.Version || restoredProject.Description != project.Description {
		t.Fatalf("project changed after restart: %#v", restoredProject)
	}
	resources, err := repository.ListResources(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 || resources[0].ID != resource.ID || !resources[0].GitDetected || resources[0].Status != "ready" {
		t.Fatalf("resource changed after restart: %#v", resources)
	}
	if err := repository.Health(ctx); err != nil {
		t.Fatalf("quick_check after restart: %v", err)
	}
}

func TestSQLiteOptimisticLocksAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	repository, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	owner, ownerComputer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	project, err := repository.CreateProject(ctx, owner.ID, domain.Project{ComputerID: ownerComputer.ID, Name: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	updatedProject, err := repository.UpdateProject(ctx, owner.ID, project.ID, project.Version, func(item *domain.Project) {
		item.Name = "updated"
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpdateProject(ctx, owner.ID, project.ID, project.Version, func(item *domain.Project) {
		item.Name = "stale"
	})
	assertDomainCode(t, err, "version_conflict")
	resource, err := repository.CreateResource(ctx, owner.ID, domain.ProjectResource{
		ProjectID: project.ID, Kind: "url", Name: "website", URL: "https://example.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedResource, err := repository.UpdateResource(ctx, owner.ID, resource.ID, resource.Version, func(item *domain.ProjectResource) {
		item.Name = "renamed"
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteResource(ctx, owner.ID, resource.ID, resource.Version); err == nil {
		t.Fatal("stale resource delete unexpectedly succeeded")
	} else {
		assertDomainCode(t, err, "version_conflict")
	}
	preferences, err := repository.GetUserPreferences(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateUserPreferences(ctx, owner.ID, preferences.Version, domain.LocaleZhCN, domain.ThemeLight, false, true, nil); err != nil {
		t.Fatal(err)
	}
	_, err = repository.UpdateUserPreferences(ctx, owner.ID, preferences.Version, domain.LocaleZhCN, domain.ThemeDark, false, true, nil)
	assertDomainCode(t, err, "version_conflict")

	otherUserID := newID("usr")
	otherComputerID := newID("cmp")
	now := sqliteTimeValue(time.Now())
	if _, err := repository.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, status, version, created_at, updated_at)
		VALUES (?, 'other@teamva.invalid', 'Other', 'active', 1, ?, ?)
	`, otherUserID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `
		INSERT INTO computers (
			id, user_id, name, platform, architecture, fingerprint, status,
			protocol_version, version, created_at, updated_at
		) VALUES (?, ?, 'Other computer', 'test', 'test', 'other-fingerprint', 'online', 1, 1, ?, ?)
	`, otherComputerID, otherUserID, now, now); err != nil {
		t.Fatal(err)
	}
	otherProject, err := repository.CreateProject(ctx, otherUserID, domain.Project{ComputerID: otherComputerID, Name: "private"})
	if err != nil {
		t.Fatal(err)
	}
	otherResource, err := repository.CreateResource(ctx, otherUserID, domain.ProjectResource{
		ProjectID: otherProject.ID, Kind: "url", Name: "private URL", URL: "https://other.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.GetProject(ctx, owner.ID, otherProject.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.ListResources(ctx, owner.ID, otherProject.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.UpdateResource(ctx, owner.ID, otherResource.ID, otherResource.Version, func(item *domain.ProjectResource) {
		item.Name = "cross tenant"
	})
	assertDomainCode(t, err, "not_found")
	if err := repository.DeleteProject(ctx, owner.ID, otherProject.ID, otherProject.Version); err == nil {
		t.Fatal("cross-tenant project delete unexpectedly succeeded")
	} else {
		assertDomainCode(t, err, "not_found")
	}

	current, err := repository.GetProject(ctx, owner.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != updatedProject.Version || updatedResource.Version != resource.Version+1 {
		t.Fatalf("unexpected optimistic versions: project=%d resource=%d", current.Version, updatedResource.Version)
	}
}

func TestSQLiteQueuedDirectoryCommandsUseFencedTerminalStates(t *testing.T) {
	ctx := context.Background()
	repository, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}

	completedID, err := repository.CreateDirectoryListCommand(ctx, user.ID, computer.ID, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	completed, ok, err := repository.ClaimLocalCommand(ctx, time.Now(), time.Minute)
	if err != nil || !ok || completed.ID != completedID {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := repository.CompleteLocalCommand(ctx, completedID, completed.ClaimToken, []byte(`{"items":[]}`)); err != nil {
		t.Fatal(err)
	}
	status, result, err := repository.GetDirectoryListResult(ctx, user.ID, computer.ID, completedID)
	if err != nil || status != LocalCommandCompleted || string(result) != `{"items":[]}` {
		t.Fatalf("completed command status=%q result=%s err=%v", status, result, err)
	}

	failedID, err := repository.CreateResourceRegisterCommand(ctx, user.ID, computer.ID, "/missing")
	if err != nil {
		t.Fatal(err)
	}
	failed, ok, err := repository.ClaimLocalCommand(ctx, time.Now(), time.Minute)
	if err != nil || !ok || failed.ID != failedID {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := repository.FailLocalCommand(ctx, failedID, failed.ClaimToken, "runtime_failed", nil); err != nil {
		t.Fatal(err)
	}
	status, _, err = repository.GetDirectoryListResult(ctx, user.ID, computer.ID, failedID)
	if err != nil || status != LocalCommandFailed {
		t.Fatalf("failed command status=%q err=%v", status, err)
	}

	expiredID, err := repository.CreateDirectoryListCommand(ctx, user.ID, computer.ID, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `UPDATE command_outbox SET expires_at = ? WHERE id = ?`,
		sqliteTimeValue(time.Now().Add(-time.Second)), expiredID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.GetDirectoryListResult(ctx, "intruder", computer.ID, expiredID); err == nil {
		t.Fatal("unowned read succeeded")
	}
	untouched, err := repository.GetLocalCommand(ctx, expiredID)
	if err != nil || untouched.Status != LocalCommandPending {
		t.Fatal("unowned GET changed a command")
	}
	if _, ok, err := repository.ClaimLocalCommand(ctx, time.Now(), time.Minute); err != nil || ok {
		t.Fatalf("expired claim: ok=%v err=%v", ok, err)
	}
	if err := repository.CompleteLocalCommand(ctx, expiredID, "invalid-claim", []byte(`{"items":[]}`)); err == nil {
		t.Fatal("expired command accepted a successful result")
	}
	status, _, err = repository.GetDirectoryListResult(ctx, user.ID, computer.ID, expiredID)
	if err != nil || status != LocalCommandExpired {
		t.Fatalf("expired command status=%q err=%v", status, err)
	}
}

func TestSQLiteSessionLifecycleUsesDurableHashes(t *testing.T) {
	ctx := context.Background()
	repository, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	user, _, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := repository.CreateLocalSession(ctx, user.ID, time.Hour, domain.SessionMeta{DeviceID: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(
		ctx, session.Token, 2*time.Hour, 15*time.Minute, 30*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	if !repository.VerifySessionCSRF(ctx, session.ID, session.CSRF) || repository.VerifySessionCSRF(ctx, session.ID, "wrong") {
		t.Fatal("unexpected CSRF verification result")
	}
	if _, err := repository.db.ExecContext(ctx, `UPDATE web_sessions SET rotated_at = ? WHERE id = ?`,
		sqliteTimeValue(time.Now().Add(-time.Hour)), session.ID); err != nil {
		t.Fatal(err)
	}
	authentication, err := repository.AuthenticateSession(
		ctx, session.Token, 2*time.Hour, 15*time.Minute, 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	rotatedToken := authentication.RotatedToken
	if rotatedToken == "" || rotatedToken == session.Token {
		t.Fatal("session token was not rotated")
	}
	previous, err := repository.AuthenticateSession(
		ctx, session.Token, 2*time.Hour, 15*time.Minute, 30*time.Second,
	)
	if err != nil {
		t.Fatalf("previous token should be valid during grace period: %v", err)
	}
	if previous.TokenMatch != domain.SessionTokenMatchPrevious || previous.RotatedToken != "" {
		t.Fatalf("previous token was not identified safely: %+v", previous)
	}
	if _, err := repository.AuthenticateSession(
		ctx, rotatedToken, 2*time.Hour, 15*time.Minute, 30*time.Second,
	); err != nil {
		t.Fatalf("rotated token should authenticate: %v", err)
	}
	_, second, err := repository.CreateLocalSession(ctx, user.ID, time.Hour, domain.SessionMeta{DeviceID: "secondary"})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := repository.ListSessions(ctx, user.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected two active sessions, got %d", len(sessions))
	}
	if err := repository.RevokeOtherSessions(ctx, user.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(
		ctx, second.Token, 2*time.Hour, 15*time.Minute, 30*time.Second,
	); err == nil {
		t.Fatal("revoked secondary session authenticated")
	}
	if err := repository.RevokeSession(ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(
		ctx, rotatedToken, 2*time.Hour, 15*time.Minute, 30*time.Second,
	); err == nil {
		t.Fatal("revoked rotated session authenticated")
	}
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != code {
		t.Fatalf("expected domain error %q, got %v", code, err)
	}
}
