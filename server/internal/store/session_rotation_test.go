package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

const (
	testSessionIdleTTL          = 30 * 24 * time.Hour
	testSessionRotationInterval = 15 * time.Minute
	testSessionPreviousTokenTTL = 30 * time.Second
)

type concurrentAuthenticationResult struct {
	authentication domain.SessionAuthentication
	err            error
}

func authenticateOldTokenConcurrently(
	t *testing.T,
	authenticate func() (domain.SessionAuthentication, error),
) string {
	t.Helper()
	start := make(chan struct{})
	results := make(chan concurrentAuthenticationResult, 2)
	for range 2 {
		go func() {
			<-start
			authentication, err := authenticate()
			results <- concurrentAuthenticationResult{authentication: authentication, err: err}
		}()
	}
	close(start)

	currentMatches := 0
	previousMatches := 0
	rotatedToken := ""
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent authentication failed: %v", result.err)
		}
		switch result.authentication.TokenMatch {
		case domain.SessionTokenMatchCurrent:
			currentMatches++
			if result.authentication.RotatedToken == "" {
				t.Fatal("the current-token winner did not return its rotated token")
			}
			rotatedToken = result.authentication.RotatedToken
		case domain.SessionTokenMatchPrevious:
			previousMatches++
			if result.authentication.RotatedToken != "" {
				t.Fatal("a previous-token match returned a cookie token")
			}
		default:
			t.Fatalf("unexpected token match %q", result.authentication.TokenMatch)
		}
	}
	if currentMatches != 1 || previousMatches != 1 {
		t.Fatalf("current matches=%d previous matches=%d, want one of each", currentMatches, previousMatches)
	}
	return rotatedToken
}

func TestSQLiteConcurrentSessionRotationSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.db")
	repository, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := repository.CreateLocalSession(
		ctx, user.ID, testSessionIdleTTL, domain.SessionMeta{DeviceID: "rotation-restart"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.ExecContext(ctx, `
		UPDATE web_sessions SET rotated_at = ? WHERE id = ?
	`, sqliteTimeValue(time.Now().UTC().Add(-time.Hour)), session.ID); err != nil {
		t.Fatal(err)
	}

	rotatedToken := authenticateOldTokenConcurrently(t, func() (domain.SessionAuthentication, error) {
		return repository.AuthenticateSession(
			ctx, session.Token, testSessionIdleTTL,
			testSessionRotationInterval, testSessionPreviousTokenTTL,
		)
	})
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	// Both token slots and their match semantics must remain correct after the
	// desktop process reopens the durable SQLite database.
	repository, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	previous, err := repository.AuthenticateSession(
		ctx, session.Token, testSessionIdleTTL,
		testSessionRotationInterval, testSessionPreviousTokenTTL,
	)
	if err != nil {
		t.Fatalf("authenticate previous token after restart: %v", err)
	}
	if previous.TokenMatch != domain.SessionTokenMatchPrevious || previous.RotatedToken != "" {
		t.Fatalf("unsafe previous-token result after restart: %+v", previous)
	}
	current, err := repository.AuthenticateSession(
		ctx, rotatedToken, testSessionIdleTTL,
		testSessionRotationInterval, testSessionPreviousTokenTTL,
	)
	if err != nil || current.TokenMatch != domain.SessionTokenMatchCurrent || current.RotatedToken != "" {
		t.Fatalf("authenticate current token after restart: result=%+v err=%v", current, err)
	}

	if _, err := repository.db.ExecContext(ctx, `
		UPDATE web_sessions SET previous_token_expires_at = ? WHERE id = ?
	`, sqliteTimeValue(time.Now().UTC().Add(-time.Second)), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateSession(
		ctx, session.Token, testSessionIdleTTL,
		testSessionRotationInterval, testSessionPreviousTokenTTL,
	); err == nil {
		t.Fatal("expired previous token authenticated")
	}
	if _, err := repository.AuthenticateSession(
		ctx, rotatedToken, testSessionIdleTTL,
		testSessionRotationInterval, testSessionPreviousTokenTTL,
	); err != nil {
		t.Fatalf("current token failed after previous-token grace elapsed: %v", err)
	}
}
