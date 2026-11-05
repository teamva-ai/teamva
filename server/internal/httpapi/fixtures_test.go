package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Tests use real SQLite in disposable directories. Extra identities only verify
// ownership; the product has no registration, password or enrollment endpoints.
type testRepository struct {
	*store.SQLite
	db               *sql.DB
	computersCreated map[string]bool
}

func newTestRepository(t *testing.T) *testRepository {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	repository, err := store.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		repository.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); repository.Close() })
	return &testRepository{SQLite: repository, db: db, computersCreated: make(map[string]bool)}
}

func newRouter(repository store.CoreLocalRepository, runtime Runtime, version string, auth bool) *gin.Engine {
	return newRouterWithOptions(repository, runtime, version, auth, Options{})
}

func newRouterWithOrigins(repository store.CoreLocalRepository, runtime Runtime, version string, auth bool, origins []string) *gin.Engine {
	return newRouterWithOptions(repository, runtime, version, auth, Options{AllowedOrigins: origins})
}

func newRouterWithOptions(repository store.CoreLocalRepository, runtime Runtime, version string, auth bool, options Options) *gin.Engine {
	if options.WebOrigin == "" {
		options.WebOrigin = testBrowserOrigin
		if len(options.AllowedOrigins) > 0 {
			options.WebOrigin = options.AllowedOrigins[0]
		}
	}
	if local, ok := repository.(interface {
		LocalIdentity() (domain.User, domain.Computer, error)
	}); ok {
		user, _, err := local.LocalIdentity()
		if err != nil {
			panic(err)
		}
		options.LocalUserID = user.ID
	}
	return buildRouter(repository, runtime, version, auth, options)
}

type testIdentity struct{ userID, token, csrf string }

func createTestIdentity(t *testing.T, repository *testRepository, email string) testIdentity {
	t.Helper()
	user, err := repository.userByEmail(t.Context(), email)
	if err == sql.ErrNoRows {
		id := "usr_" + uuid.NewString()
		now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
		_, err = repository.db.ExecContext(t.Context(), `
			INSERT INTO users (id, email, display_name, status, version, created_at, updated_at)
			VALUES (?, ?, 'Test User', 'active', 1, ?, ?)
		`, id, email, now, now)
		if err == nil {
			user, err = repository.GetUser(t.Context(), id)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := repository.CreateLocalSession(t.Context(), user.ID, time.Hour, domain.SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return testIdentity{userID: user.ID, token: session.Token, csrf: session.CSRF}
}

func (r *testRepository) userByEmail(ctx context.Context, email string) (domain.User, error) {
	var id string
	if err := r.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
		return domain.User{}, err
	}
	return r.GetUser(ctx, id)
}

func createTestComputer(t *testing.T, repository *testRepository, auth map[string]string, name string) domain.Computer {
	t.Helper()
	token := strings.TrimPrefix(auth["Cookie"], sessionCookieName+"=")
	identity, err := repository.AuthenticateSession(t.Context(), token, sessionIdleTTL, sessionRotationInterval, sessionPreviousTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	userID := identity.User.ID
	if !repository.computersCreated[userID] {
		repository.computersCreated[userID] = true
		items, err := repository.ListComputers(t.Context(), userID)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			return items[0]
		}
	}
	id := "cmp_" + uuid.NewString()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	_, err = repository.db.ExecContext(t.Context(), `
		INSERT INTO computers (id, user_id, name, fingerprint, platform, architecture, status, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'darwin', 'arm64', 'online', 1, ?, ?)
	`, id, userID, name, id, now, now)
	if err != nil {
		t.Fatal(err)
	}
	item, err := repository.GetComputer(t.Context(), userID, id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func testLoginHeaders(t *testing.T, _ *testRepository, router http.Handler) map[string]string {
	t.Helper()
	_, cookie, csrf := bootstrapViaCookies(t, router, testBrowserOrigin)
	return map[string]string{"Cookie": sessionCookieName + "=" + cookie, "X-CSRF-Token": csrf, "Origin": testBrowserOrigin}
}

func testLocalUserID(repository *testRepository, t *testing.T) string {
	t.Helper()
	user, _, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func computerID(t *testing.T, repository *testRepository) string {
	t.Helper()
	_, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return computer.ID
}
