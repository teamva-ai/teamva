package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

type resourceOpenTestRelay struct {
	command string
	input   map[string]any
}

func (r *resourceOpenTestRelay) NotifyIssueActivation(string) {}

func (r *resourceOpenTestRelay) ExecuteLocalCommand(_ context.Context, command string, input any) (json.RawMessage, error) {
	r.command = command
	encoded, _ := json.Marshal(input)
	_ = json.Unmarshal(encoded, &r.input)
	switch command {
	case "skill.install":
		return json.RawMessage(`{"slug":"hallmark","path":"/tmp/skills/hallmark","commit_sha":"13ac0ec7e148655948100b6396439e481361d690","content_digest":"sha256:test-hallmark","manifest":{"skill_md":"---\nname: hallmark\ndescription: test\n---\n"},"file_count":1,"size_bytes":52}`), nil
	case "resource.openers":
		return json.RawMessage(`{"platform":"darwin","items":[{"id":"finder","kind":"file_manager","name":"Finder","available":true},{"id":"vscode","kind":"ide","name":"Visual Studio Code","available":false}]}`), nil
	case "resource.open":
		return json.RawMessage(`{"opened":true}`), nil
	case "skill.open":
		return json.RawMessage(`{"opened":true,"path":"/tmp/skills/hallmark"}`), nil
	default:
		return nil, nil
	}
}

func TestOpenInstalledSkillUsesLocalFileManagerCommand(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	if err := store.EnsureBuiltinSkillPackages(t.Context(), repository); err != nil {
		t.Fatal(err)
	}
	relay := &resourceOpenTestRelay{}
	router := newRouterWithOrigins(repository, relay, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "Skill Mac")
	installed := decodeData[domain.ComputerSkillPackage](t, request(t, router, http.MethodPost,
		"/v1/computers/"+computer.ID+"/skill-packages", map[string]any{
			"skill_package_id": "skp_builtin_hallmark",
		}, auth, http.StatusCreated))
	request(t, router, http.MethodPost,
		"/v1/computers/"+computer.ID+"/skill-packages/"+installed.ID+"/open",
		nil, auth, http.StatusOK)
	if relay.command != "skill.open" || relay.input["slug"] != "hallmark" {
		t.Fatalf("command=%s input=%+v", relay.command, relay.input)
	}
}

func TestResourceOpenersAndOpenUseRegisteredCodeDirectory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	relay := &resourceOpenTestRelay{}
	router := newRouterWithOrigins(repository, relay, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "Resource Mac")
	project := decodeData[domain.Project](t, request(t, router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computer.ID, "name": "Resource project",
	}, auth, http.StatusCreated))
	resource := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/resources", map[string]any{
			"kind": "code_directory", "name": "Repository", "path": "/tmp/registered-repository",
		}, auth, http.StatusCreated))

	openers := decodeData[struct {
		Platform string `json:"platform"`
		Items    []struct {
			ID        string `json:"id"`
			Available bool   `json:"available"`
		} `json:"items"`
	}](t, request(t, router, http.MethodGet,
		"/v1/computers/"+computer.ID+"/resource-openers", nil, auth, http.StatusOK))
	if openers.Platform != "darwin" || len(openers.Items) != 2 || !openers.Items[0].Available {
		t.Fatalf("openers = %+v", openers)
	}

	request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/resources/"+resource.ID+"/open",
		map[string]any{"opener_id": "finder"}, auth, http.StatusOK)
	if relay.command != "resource.open" || relay.input["path"] != "/tmp/registered-repository" || relay.input["opener_id"] != "finder" {
		t.Fatalf("command=%s input=%+v", relay.command, relay.input)
	}

	document := decodeData[domain.ProjectResource](t, request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/resources", map[string]any{
			"kind": "document_directory", "name": "Docs", "path": "/tmp/docs",
		}, auth, http.StatusCreated))
	response := request(t, router, http.MethodPost,
		"/v1/projects/"+project.ID+"/resources/"+document.ID+"/open",
		map[string]any{"opener_id": "finder"}, auth, http.StatusBadRequest)
	if response.Error == nil || response.Error.Code != "invalid_argument" {
		t.Fatalf("document open error = %+v", response.Error)
	}
}
