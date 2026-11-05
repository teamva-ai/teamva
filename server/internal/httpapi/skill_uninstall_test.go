package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

type skillUninstallTestRelay struct {
	calls  atomic.Int64
	remove func(context.Context, nodeprotocol.UninstallSkillPayload) (json.RawMessage, error)
}

type skillUninstallRepositoryWithoutArtifact struct{ store.CoreLocalRepository }

func (*skillUninstallTestRelay) NotifyIssueActivation(string) {}

func (r *skillUninstallTestRelay) ExecuteLocalCommand(ctx context.Context, name string, input any) (json.RawMessage, error) {
	if name != "skill.uninstall" {
		return nil, errors.New("unexpected command")
	}
	payload, ok := input.(nodeprotocol.UninstallSkillPayload)
	if !ok {
		return nil, errors.New("unexpected payload type")
	}
	r.calls.Add(1)
	return r.remove(ctx, payload)
}

type skillUninstallHTTPFixture struct {
	repository   *testRepository
	relay        *skillUninstallTestRelay
	router       http.Handler
	headers      map[string]string
	userID       string
	computerID   string
	packageItem  domain.SkillPackage
	installation domain.ComputerSkillPackage
	directory    string
}

func newSkillUninstallHTTPFixture(t *testing.T) skillUninstallHTTPFixture {
	t.Helper()
	ginSetTestMode(t)
	repository := newTestRepository(t)
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	packageItem, err := repository.UpsertSkillPackage(t.Context(), domain.SkillPackage{
		Slug: "uninstall-test", Name: "Uninstall test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installation, err := repository.InstallComputerSkillPackageVersion(t.Context(), user.ID, computer.ID, packageItem.ID,
		domain.SkillPackageVersion{
			CommitSHA: "0123456789abcdef0123456789abcdef01234567", ContentDigest: "sha256:test",
			Manifest: map[string]any{"files": []any{"SKILL.md"}}, FileCount: 1,
		})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	directory := filepath.Join(root, packageItem.Slug)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("test artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	relay := &skillUninstallTestRelay{remove: func(_ context.Context, payload nodeprotocol.UninstallSkillPayload) (json.RawMessage, error) {
		if payload.Slug != packageItem.Slug {
			return nil, errors.New("wrong artifact selected")
		}
		if err := os.RemoveAll(filepath.Join(root, payload.Slug)); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"uninstalled":true}`), nil
	}}
	router := newRouter(repository, relay, "test", true)
	return skillUninstallHTTPFixture{
		repository: repository, relay: relay, router: router, headers: testLoginHeaders(t, repository, router),
		userID: user.ID, computerID: computer.ID, packageItem: packageItem, installation: installation, directory: directory,
	}
}

func (f skillUninstallHTTPFixture) uninstallPath() string {
	return "/v1/computers/" + f.computerID + "/skill-packages/" + f.installation.ID +
		"?expected_version=" + fmt.Sprint(f.installation.Version)
}

func (f skillUninstallHTTPFixture) assertVisible(t *testing.T, visible bool) {
	t.Helper()
	page := decodeData[domain.Page[domain.ComputerSkillPackage]](t, request(t, f.router, http.MethodGet,
		"/v1/computers/"+f.computerID+"/skill-packages", nil, f.headers, http.StatusOK))
	if !visible {
		if len(page.Items) != 0 {
			t.Fatalf("uninstalled record is still visible: count=%d", len(page.Items))
		}
		return
	}
	if len(page.Items) != 1 || page.Items[0].ID != f.installation.ID || page.Items[0].Version != f.installation.Version {
		t.Fatalf("retryable installation changed: count=%d", len(page.Items))
	}
}

func (f skillUninstallHTTPFixture) createAgent(t *testing.T, bind bool) domain.Agent {
	t.Helper()
	service, err := f.repository.UpsertAgentService(t.Context(), f.userID, domain.AgentService{
		ComputerID: f.computerID, Provider: "codex", ProviderVersion: "test",
		LoginStatus: "authenticated", Status: "ready", CapabilityVersion: 1, Capabilities: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var skills []string
	if bind {
		skills = []string{f.installation.SkillPackageVersionID}
	}
	agent, err := f.repository.CreateConfiguredAgent(t.Context(), f.userID, domain.Agent{
		ComputerID: f.computerID, AgentServiceID: service.ID, Name: "Skill uninstall test",
	}, "", skills)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func TestSkillUninstallHTTPDeletionFailureRetainsRecordAndRetrySucceeds(t *testing.T) {
	f := newSkillUninstallHTTPFixture(t)
	remove := f.relay.remove
	f.relay.remove = func(context.Context, nodeprotocol.UninstallSkillPayload) (json.RawMessage, error) {
		return nil, errors.New("private-payload-or-secret-must-not-be-logged")
	}
	f.headers["Accept-Language"] = "zh-CN"
	response := request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusServiceUnavailable)
	if response.Error == nil || response.Error.Code != "skill_uninstall_failed" ||
		strings.Contains(response.Recorder.Body.String(), "private-payload") ||
		!strings.Contains(response.Recorder.Body.String(), `"installation_record_retained":true`) {
		t.Fatalf("unexpected safe failure response: %s", response.Recorder.Body.String())
	}
	f.assertVisible(t, true)
	if _, err := os.Stat(filepath.Join(f.directory, "SKILL.md")); err != nil {
		t.Fatalf("failed deletion damaged artifact: %v", err)
	}
	f.relay.remove = remove
	request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusNoContent)
	f.assertVisible(t, false)
	if _, err := os.Stat(f.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact remains after successful uninstall: %v", err)
	}
	request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusNotFound)
	if f.relay.calls.Load() != 2 {
		t.Fatalf("relay calls=%d, want failure plus successful retry", f.relay.calls.Load())
	}
}

func TestSkillUninstallHTTPRejectsUnauthorizedMissingStaleAndBoundBeforeDeleting(t *testing.T) {
	for _, scenario := range []string{"other-user", "other-computer", "missing", "stale", "bound"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSkillUninstallHTTPFixture(t)
			path, headers, status, code := f.uninstallPath(), f.headers, http.StatusNotFound, "not_found"
			switch scenario {
			case "other-user":
				other := createTestIdentity(t, f.repository, "skill-uninstall-other@example.test")
				headers = map[string]string{"Cookie": sessionCookieName + "=" + other.token,
					"X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin}
			case "other-computer":
				path = strings.Replace(path, f.computerID, "cmp_missing", 1)
			case "missing":
				path = strings.Replace(path, f.installation.ID, "csk_missing", 1)
			case "stale":
				path = strings.Replace(path, "expected_version=1", "expected_version=2", 1)
				status, code = http.StatusConflict, "version_conflict"
			case "bound":
				f.createAgent(t, true)
				status, code = http.StatusConflict, "skill_in_use"
			}
			response := request(t, f.router, http.MethodDelete, path, nil, headers, status)
			if response.Error == nil || response.Error.Code != code {
				t.Fatalf("error=%+v, want %s", response.Error, code)
			}
			if f.relay.calls.Load() != 0 {
				t.Fatalf("rejected uninstall reached file deletion: calls=%d", f.relay.calls.Load())
			}
			f.assertVisible(t, true)
			if _, err := os.Stat(f.directory); err != nil {
				t.Fatalf("rejected uninstall removed artifact: %v", err)
			}
		})
	}
}

func TestSkillUninstallHTTPInUseExplainsUnbindingAndStoppingBeforeRetry(t *testing.T) {
	const chinese = "该技能正在被 Agent 或活动运行使用。请先在 Agent 配置中解除所有绑定，并停止相关运行，再重试卸载。"
	const english = "This skill is in use by an Agent or an active run. Unbind it from all Agents in their configuration and stop related runs, then retry uninstalling."
	for _, language := range []string{"zh-CN", "zh-TW", "en-US", "ja-JP", "de-DE", "fr-FR", "es-ES", "pt-BR", "ru-RU"} {
		t.Run(language, func(t *testing.T) {
			f := newSkillUninstallHTTPFixture(t)
			agent := f.createAgent(t, true)
			f.headers["Accept-Language"] = language
			response := request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusConflict)
			body := decodeErrorBody(t, response.Recorder)
			want := english
			if strings.HasPrefix(language, "zh") {
				want = chinese
			}
			if body.Error.Code != "skill_in_use" || body.Error.Message != want {
				t.Fatalf("unactionable skill conflict: code=%s message=%q", body.Error.Code, body.Error.Message)
			}
			f.assertVisible(t, true)
			if f.relay.calls.Load() != 0 {
				t.Fatal("bound skill reached artifact deletion")
			}
			if _, err := os.Stat(filepath.Join(f.directory, "SKILL.md")); err != nil {
				t.Fatalf("bound skill artifact changed: %v", err)
			}
			// Apply the suggested action through the same public Agent API, then
			// retry with the original installation version (no hidden mutation).
			request(t, f.router, http.MethodPatch, "/v1/agents/"+agent.ID,
				map[string]any{"skill_version_ids": []string{}, "expected_version": agent.Version}, f.headers, http.StatusOK)
			request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusNoContent)
			f.assertVisible(t, false)
			if f.relay.calls.Load() != 1 {
				t.Fatalf("unbound artifact deletion calls=%d", f.relay.calls.Load())
			}
		})
	}
}

func TestSkillInUsePublicMessageNeverEchoesPrivateDomainProse(t *testing.T) {
	for _, language := range []string{"", "zh-CN", "zh-TW", "en-US", "ja-JP", "de-DE", "fr-FR", "es-ES", "pt-BR", "ru-RU"} {
		t.Run(language, func(t *testing.T) {
			want := "This skill is in use by an Agent or an active run. Unbind it from all Agents in their configuration and stop related runs, then retry uninstalling."
			if strings.HasPrefix(language, "zh") {
				want = "该技能正在被 Agent 或活动运行使用。请先在 Agent 配置中解除所有绑定，并停止相关运行，再重试卸载。"
			}
			for _, message := range []string{"", "private-runtime-payload /private/skill-path", "内部配置 private-runtime-payload /private/skill-path"} {
				if got := publicErrorMessage("skill_in_use", message, http.StatusConflict, language); got != want {
					t.Fatalf("skill conflict must use fixed safe copy, got %q", got)
				}
			}
		})
	}
}

func TestSkillUninstallHTTPUnsupportedRuntimeAndInvalidResultsRetainRecord(t *testing.T) {
	for _, scenario := range []string{"unsupported", "repository", "malformed", "negative", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSkillUninstallHTTPFixture(t)
			status := http.StatusServiceUnavailable
			if scenario == "unsupported" {
				f.router = newRouter(f.repository, nil, "test", true)
				status = http.StatusNotImplemented
			} else if scenario == "repository" {
				f.router = newRouterWithOptions(skillUninstallRepositoryWithoutArtifact{f.repository}, f.relay,
					"test", true, Options{LocalUserID: f.userID})
				status = http.StatusNotImplemented
			} else {
				f.relay.remove = func(context.Context, nodeprotocol.UninstallSkillPayload) (json.RawMessage, error) {
					switch scenario {
					case "malformed":
						return json.RawMessage(`not-json`), nil
					case "negative":
						return json.RawMessage(`{"uninstalled":false}`), nil
					default:
						return nil, nil
					}
				}
			}
			request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, status)
			f.assertVisible(t, true)
			if (scenario == "unsupported" || scenario == "repository") && f.relay.calls.Load() != 0 {
				t.Fatal("unsupported runtime reached file deletion")
			}
		})
	}
}

func TestSkillUninstallHTTPRejectsUnsafeSlugsBeforeRelay(t *testing.T) {
	for _, slug := range []string{".", "..", "../outside", "/absolute", `a\b`, ".hidden", "with space", "a.b", strings.Repeat("a", 129)} {
		t.Run(slug, func(t *testing.T) {
			f := newSkillUninstallHTTPFixture(t)
			if _, err := f.repository.db.ExecContext(t.Context(), `UPDATE skill_packages SET slug = ? WHERE id = ?`, slug, f.packageItem.ID); err != nil {
				t.Fatal(err)
			}
			request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusBadRequest)
			f.assertVisible(t, true)
			if f.relay.calls.Load() != 0 {
				t.Fatal("unsafe slug reached artifact deletion")
			}
		})
	}
}

func TestSkillUninstallHTTPCommitFailureLeavesRetryableRecordAfterArtifactRemoval(t *testing.T) {
	f := newSkillUninstallHTTPFixture(t)
	// Inject a deferred FK violation only at uninstall commit, after the fake
	// relay has really removed its disposable artifact directory.
	if _, err := f.repository.db.ExecContext(t.Context(), `
		CREATE TABLE uninstall_commit_failure (computer_id TEXT REFERENCES computers(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_uninstall_commit AFTER UPDATE OF deleted_at ON computer_skill_packages
		WHEN NEW.deleted_at IS NOT NULL BEGIN
		  INSERT INTO uninstall_commit_failure VALUES ('cmp_nonexistent');
		END;
	`); err != nil {
		t.Fatal(err)
	}
	response := request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusServiceUnavailable)
	if response.Error == nil || response.Error.Code != "skill_uninstall_incomplete" ||
		!strings.Contains(response.Recorder.Body.String(), `"artifact_state":"removed"`) {
		t.Fatalf("commit failure state unclear: %s", response.Recorder.Body.String())
	}
	f.assertVisible(t, true)
	if _, err := os.Stat(f.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test did not reach irreversible removal: %v", err)
	}
	if _, err := f.repository.db.ExecContext(t.Context(), `DROP TRIGGER fail_uninstall_commit`); err != nil {
		t.Fatal(err)
	}
	// Missing files are an idempotent success, allowing the retained installation
	// to be removed with its original expected_version.
	request(t, f.router, http.MethodDelete, f.uninstallPath(), nil, f.headers, http.StatusNoContent)
	f.assertVisible(t, false)
	if f.relay.calls.Load() != 2 {
		t.Fatalf("relay calls=%d", f.relay.calls.Load())
	}
}

func TestSkillUninstallHTTPConcurrentBindingCannotPublishAfterUninstall(t *testing.T) {
	f := newSkillUninstallHTTPFixture(t)
	agent := f.createAgent(t, false)
	entered, release := make(chan struct{}), make(chan struct{})
	remove := f.relay.remove
	f.relay.remove = func(ctx context.Context, payload nodeprotocol.UninstallSkillPayload) (json.RawMessage, error) {
		close(entered)
		select {
		case <-release:
			return remove(ctx, payload)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	uninstallRequest := httptest.NewRequest(http.MethodDelete, f.uninstallPath(), nil)
	for key, value := range f.headers {
		uninstallRequest.Header.Set(key, value)
	}
	uninstallRecorder := httptest.NewRecorder()
	uninstalled := make(chan struct{})
	go func() {
		f.router.ServeHTTP(uninstallRecorder, uninstallRequest)
		close(uninstalled)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("uninstall did not reach artifact callback")
	}
	body := fmt.Sprintf(`{"skill_version_ids":[%q],"expected_version":%d}`, f.installation.SkillPackageVersionID, agent.Version)
	bindRequest := httptest.NewRequest(http.MethodPatch, "/v1/agents/"+agent.ID, strings.NewReader(body))
	bindRequest.Header.Set("Content-Type", "application/json")
	for key, value := range f.headers {
		bindRequest.Header.Set(key, value)
	}
	bindRecorder := httptest.NewRecorder()
	bindStarted, bound := make(chan struct{}), make(chan struct{})
	go func() {
		close(bindStarted)
		f.router.ServeHTTP(bindRecorder, bindRequest)
		close(bound)
	}()
	<-bindStarted
	close(release)
	for _, done := range []<-chan struct{}{uninstalled, bound} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent request did not finish")
		}
	}
	if uninstallRecorder.Code != http.StatusNoContent || bindRecorder.Code != http.StatusBadRequest {
		t.Fatalf("concurrent statuses: uninstall=%d bind=%d", uninstallRecorder.Code, bindRecorder.Code)
	}
	current, err := f.repository.GetAgent(t.Context(), f.userID, agent.ID)
	if err != nil || current.Version != agent.Version || current.CurrentRevisionID != agent.CurrentRevisionID {
		t.Fatalf("failed concurrent binding changed Agent: err=%v", err)
	}
	f.assertVisible(t, false)
}
