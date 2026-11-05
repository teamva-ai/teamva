package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/node/mcpmanager"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Deliberately synthetic: these markers must only occur inside MCP's private
// files and privileged in-memory Effective result, never HTTP or durable jobs.
var mcpAppPrivateFixtures = []string{
	"mcp-app-environment-private-fixture",
	"mcp-app-header-private-fixture",
	"mcp-app-oauth-private-fixture",
}

func mcpAppRequest(t *testing.T, client *http.Client, origin, method, path, csrf string, input any, status int) json.RawMessage {
	t.Helper()
	var payload []byte
	if input != nil {
		var err error
		payload, err = json.Marshal(input)
		if err != nil {
			t.Fatal("cannot encode MCP test request")
		}
	}
	req, err := http.NewRequest(method, origin+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal("cannot construct MCP test request")
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("X-CSRF-Token", csrf)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("MCP HTTP request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal("cannot read MCP HTTP response")
	}
	for _, secret := range mcpAppPrivateFixtures {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("MCP HTTP response exposed private fixture")
		}
	}
	if response.StatusCode != status || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("MCP status/cache mismatch: status=%d, expected=%d", response.StatusCode, status)
	}
	var envelope testEnvelope[json.RawMessage]
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal("invalid MCP response envelope")
	}
	return envelope.Data
}

func mcpAppView(t *testing.T, raw json.RawMessage) mcpmanager.View {
	t.Helper()
	var view mcpmanager.View
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal("invalid MCP View")
	}
	return view
}

func mcpAppCommandCount(t *testing.T, dataDir string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "app.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM command_outbox").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestMCPHTTPPrivatePersistenceRestartAndNoDurableCommands(t *testing.T) {
	dataDir := t.TempDir()
	var logs bytes.Buffer
	start := func(nonce string) *Local {
		t.Helper()
		local, err := StartLocal(t.Context(), LocalConfig{
			Address: "127.0.0.1:0", DataDir: dataDir, BootstrapNonce: nonce,
			Version: "test", DisableRuntime: true, Logger: log.New(&logs, "", 0),
		})
		if err != nil {
			t.Fatal("cannot start isolated MCP test application")
		}
		return local
	}
	local := start("mcp-private-first")
	t.Cleanup(func() { _ = local.Close(context.Background()) })
	client := newTestClient(t)
	origin := local.Endpoint().Origin
	session := mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	csrf := session.CSRFToken
	baselineCommands := mcpAppCommandCount(t, dataDir)
	computers := getJSON[domain.Page[domain.Computer]](t, client, origin, "/v1/computers")
	if len(computers.Items) != 1 {
		t.Fatal("missing singleton computer for MCP project scope")
	}
	projects := getJSON[domain.Page[domain.Project]](t, client, origin, "/v1/projects?computer_id="+computers.Items[0].ID)
	if len(projects.Items) == 0 {
		t.Fatal("missing real project for MCP project scope")
	}
	projectID := projects.Items[0].ID
	marker := filepath.Join(dataDir, "mcp-process-started")
	program := filepath.Join(dataDir, "mcp-process-fixture.sh")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf started > \"$MCP_LAUNCH_MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	localInput := mcpmanager.Input{
		Name: "private-local", Scope: "user", Version: 0,
	}
	localInput.Config, _ = json.Marshal(map[string]any{
		"type": "local", "command": []string{program}, "cwd": dataDir,
		"environment": map[string]string{"MCP_PRIVATE_FIXTURE": mcpAppPrivateFixtures[0], "MCP_LAUNCH_MARKER": marker},
	})
	localView := mcpAppView(t, mcpAppRequest(t, client, origin, http.MethodPost, "/v1/mcp/servers", csrf, localInput, http.StatusOK))
	if localView.Version <= 0 || localView.Status != "disconnected" || !bytes.Contains(localView.Config, []byte(mcpmanager.KeepSecret)) {
		t.Fatal("saving local MCP must redact environment and remain disconnected")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("saving MCP configuration executed its process")
	}
	remoteConfig, _ := json.Marshal(map[string]any{
		"type": "remote", "url": "https://example.test/mcp",
		"headers": map[string]string{"Authorization": mcpAppPrivateFixtures[1]},
		"oauth":   map[string]string{"client_id": "fixture-client", "client_secret": mcpAppPrivateFixtures[2]},
	})
	remoteInput := mcpmanager.Input{Name: "private-remote", Scope: "project", ProjectID: projectID, Config: remoteConfig}
	remoteView := mcpAppView(t, mcpAppRequest(t, client, origin, http.MethodPost, "/v1/mcp/servers", csrf, remoteInput, http.StatusOK))
	if remoteView.Version <= 0 || remoteView.Status != "disconnected" {
		t.Fatal("saving remote MCP must remain disconnected")
	}
	var redacted map[string]any
	if err := json.Unmarshal(remoteView.Config, &redacted); err != nil {
		t.Fatal(err)
	}
	if redacted["headers"].(map[string]any)["Authorization"] != mcpmanager.KeepSecret || redacted["oauth"].(map[string]any)["client_secret"] != mcpmanager.KeepSecret {
		t.Fatal("remote credentials were not replaced by keep-secret sentinel")
	}
	// A duplicate create and a stale update must fail without replacing secrets.
	mcpAppRequest(t, client, origin, http.MethodPost, "/v1/mcp/servers", csrf, remoteInput, http.StatusConflict)
	remoteInput.Version = remoteView.Version + 1
	mcpAppRequest(t, client, origin, http.MethodPost, "/v1/mcp/servers", csrf, remoteInput, http.StatusConflict)
	remoteInput.Version, remoteInput.Config = remoteView.Version, remoteView.Config
	updated := mcpAppView(t, mcpAppRequest(t, client, origin, http.MethodPost, "/v1/mcp/servers", csrf, remoteInput, http.StatusOK))
	if updated.Version <= 0 || updated.Version == remoteView.Version {
		t.Fatal("successful optimistic update must change version")
	}
	// Valid but wrong scope must not delete another entry with the same name.
	mcpAppRequest(t, client, origin, http.MethodDelete, fmt.Sprintf("/v1/mcp/servers/private-local?scope=project&project_id=%s&version=%d", projectID, localView.Version), csrf, nil, http.StatusNotFound)
	mcpAppRequest(t, client, origin, http.MethodDelete, "/v1/mcp/servers/private-local?scope=user&version=0", csrf, nil, http.StatusBadRequest)
	for _, action := range []string{"disconnect", "revoke"} {
		mcpAppRequest(t, client, origin, http.MethodPost, "/v1/mcp/servers/private-remote/"+action, csrf, map[string]string{"scope": "project", "project_id": projectID}, http.StatusOK)
	}
	mcpAppRequest(t, client, origin, http.MethodGet, "/v1/mcp/servers?project_id="+projectID, csrf, nil, http.StatusOK)
	if got := mcpAppCommandCount(t, dataDir); got != baselineCommands {
		t.Fatal("MCP management entered durable Runtime command outbox")
	}
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Shutdown must release the private manager lock; restart must preserve
	// credentials and versions without reconnecting or running any Provider.
	reopened := start("mcp-private-second")
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	client = newTestClient(t)
	origin = reopened.Endpoint().Origin
	session = mutateJSON[testBootstrap](t, client, origin, http.MethodPost, "/v1/auth/local-session", "", map[string]any{})
	csrf = session.CSRFToken
	raw := mcpAppRequest(t, client, origin, http.MethodGet, "/v1/mcp/servers?project_id="+projectID, csrf, nil, http.StatusOK)
	var listing struct {
		Items []mcpmanager.View `json:"items"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil || len(listing.Items) != 2 {
		t.Fatal("MCP restart lost scoped configuration")
	}
	for _, item := range listing.Items {
		if item.Status != "disconnected" {
			t.Fatal("MCP restart must not reconnect implicitly")
		}
		if item.Name == "private-remote" && item.Version != updated.Version {
			t.Fatal("MCP restart changed optimistic version")
		}
	}
	effective, err := reopened.mcp.Effective(t.Context(), projectID)
	if err != nil {
		t.Fatal("cannot resolve private MCP configuration after restart")
	}
	for i, secret := range mcpAppPrivateFixtures {
		name := "private-remote"
		if i == 0 {
			name = "private-local"
		}
		if !bytes.Contains(effective[name], []byte(secret)) {
			t.Fatal("keep-secret update or restart lost private credential")
		}
	}
	mcpAppRequest(t, client, origin, http.MethodDelete, fmt.Sprintf("/v1/mcp/servers/private-local?scope=user&version=%d", localView.Version), csrf, nil, http.StatusOK)
	if got := mcpAppCommandCount(t, dataDir); got != baselineCommands {
		t.Fatal("MCP management created a durable command after restart")
	}
	if err := reopened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("MCP management or restart launched a process")
	}
	// DisableRuntime is intentional: this test never calls a production
	// Provider. Any Runtime DB/command result would be an unexpected side effect.
	if _, err := os.Stat(filepath.Join(dataDir, "runtime", "runtime.db")); !os.IsNotExist(err) {
		t.Fatal("MCP-only management initialized a model Runtime")
	}
	privateFiles := 0
	err = filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		private := strings.HasPrefix(rel, "mcp"+string(filepath.Separator))
		if private {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm() != 0600 {
				return fmt.Errorf("MCP private file has unsafe permissions")
			}
		}
		for _, secret := range mcpAppPrivateFixtures {
			if bytes.Contains(content, []byte(secret)) {
				if !private {
					return fmt.Errorf("private MCP credential escaped into database, log or state file")
				}
				privateFiles++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if privateFiles == 0 {
		t.Fatal("MCP credentials were not persisted in private files")
	}
	for _, secret := range mcpAppPrivateFixtures {
		if bytes.Contains(logs.Bytes(), []byte(secret)) {
			t.Fatal("MCP credential escaped into application logs")
		}
	}
}
