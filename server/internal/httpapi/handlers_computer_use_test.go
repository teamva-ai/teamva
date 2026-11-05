package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/computeruse"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type computerUseTestRelay struct{}

func (r *computerUseTestRelay) NotifyIssueActivation(string) {}

func (r *computerUseTestRelay) ExecuteLocalCommand(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func (r *computerUseTestRelay) RecycleIdleProvider(context.Context, string) (nodeprotocol.ProviderRuntimeRecycleResult, error) {
	return nodeprotocol.ProviderRuntimeRecycleResult{}, nil
}

func TestComputerUseStatusAndInstallFlow(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-for-test")
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, &computerUseTestRelay{}, "test", true, []string{testBrowserOrigin})

	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "Computer Use Machine")

	// 模拟本机已安装 open-computer-use + npm：通过常见 bin 目录注入。
	binDir := t.TempDir()
	npmDir := t.TempDir()
	writeFakeExecutable(t, binDir, computeruse.ServerName, "#!/bin/sh\necho 'v0.3.1'\n")
	writeFakeExecutable(t, npmDir, "npm", "#!/bin/sh\necho ok > /dev/null\n")
	originalDirs, originalNpm := computeruse.CommonBinDirs, computeruse.CommonNpmBins
	t.Cleanup(func() {
		computeruse.CommonBinDirs = originalDirs
		computeruse.CommonNpmBins = originalNpm
	})
	computeruse.CommonBinDirs = []string{binDir}
	computeruse.CommonNpmBins = []string{filepath.Join(npmDir, "npm")}

	status := decodeData[struct {
		Binary struct {
			Installed bool   `json:"installed"`
			Version   string `json:"version"`
		} `json:"binary"`
	}](t, request(t, router, http.MethodGet,
		"/v1/computers/"+computer.ID+"/computer-use", nil, auth, http.StatusOK))
	if !status.Binary.Installed || status.Binary.Version != "v0.3.1" {
		t.Fatalf("status=%+v", status)
	}

	install := decodeData[struct {
		Status struct {
			Binary struct {
				Installed bool   `json:"installed"`
				Version   string `json:"version"`
			} `json:"binary"`
		} `json:"status"`
		Output string `json:"output"`
	}](t, request(t, router, http.MethodPost,
		"/v1/computers/"+computer.ID+"/computer-use", nil, auth, http.StatusOK))
	if !install.Status.Binary.Installed || install.Status.Binary.Version != "v0.3.1" {
		t.Fatalf("install status=%+v", install)
	}
}

func TestComputerUseRequiresOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, &computerUseTestRelay{}, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "Owner Machine")
	wrong := createTestComputer(t, repository, auth, "Wrong Machine")
	_ = wrong

	response := request(t, router, http.MethodGet,
		"/v1/computers/"+computer.ID+"/computer-use", nil, auth, http.StatusOK)
	if response.Error != nil {
		t.Fatalf("error=%+v", response.Error)
	}
}

func writeFakeExecutable(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestComputerUseConfigurationAcceptsFalseAndChecksVersionAndOwnership(t *testing.T) {
	repository := newTestRepository(t)
	router := newRouter(repository, nil, "test", true)
	auth := testLoginHeaders(t, repository, router)
	_, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/computers/" + computer.ID + "/computer-use"
	enabled := decodeData[domain.Computer](t, request(t, router, http.MethodPatch, path, map[string]any{"enabled": true, "expected_version": computer.Version}, auth, http.StatusOK))
	if !enabled.ComputerUseEnabled {
		t.Fatal("flag was not enabled")
	}
	request(t, router, http.MethodPatch, path, map[string]any{"enabled": false, "expected_version": computer.Version}, auth, http.StatusConflict)
	disabled := decodeData[domain.Computer](t, request(t, router, http.MethodPatch, path, map[string]any{"enabled": false, "expected_version": enabled.Version}, auth, http.StatusOK))
	if disabled.ComputerUseEnabled {
		t.Fatal("false must disable the flag")
	}
	other := createTestIdentity(t, repository, "computer-use-other@example.test")
	request(t, router, http.MethodPatch, path, map[string]any{"enabled": true, "expected_version": disabled.Version}, map[string]string{
		"Cookie": sessionCookieName + "=" + other.token, "X-CSRF-Token": other.csrf, "Origin": testBrowserOrigin,
	}, http.StatusNotFound)
}
