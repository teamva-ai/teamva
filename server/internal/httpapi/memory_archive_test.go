package httpapi

import (
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestArchivedMemoriesAreQueryableAndRestorableWithoutLeakingIntoActiveContext(t *testing.T) {
	repository := newTestRepository(t)
	router := newRouter(repository, nil, "test", true)
	auth := testLoginHeaders(t, repository, router)
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	project, err := repository.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "memory lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := repository.CreateMemory(t.Context(), user.ID, domain.ProjectMemoryEntry{ProjectID: project.ID, Key: "memory", Kind: "fact", Title: "Memory", Content: "keep", SourceType: "user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/projects/" + project.ID + "/memories"
	request(t, router, http.MethodPost, path+"/"+entry.ID+"/archive", map[string]any{"expected_version": entry.Version}, auth, http.StatusNoContent)
	active := decodeData[domain.Page[domain.ProjectMemoryEntry]](t, request(t, router, http.MethodGet, path, nil, auth, http.StatusOK))
	archived := decodeData[domain.Page[domain.ProjectMemoryEntry]](t, request(t, router, http.MethodGet, path+"?archived=true", nil, auth, http.StatusOK))
	if len(active.Items) != 0 || len(archived.Items) != 1 || archived.Items[0].DeletedAt == nil {
		t.Fatal("archive filtering failed")
	}
	request(t, router, http.MethodGet, path+"?archived=invalid", nil, auth, http.StatusBadRequest)
	other := createTestIdentity(t, repository, "memory-other@example.test")
	request(t, router, http.MethodGet, path+"?archived=true", nil, map[string]string{"Cookie": sessionCookieName + "=" + other.token}, http.StatusNotFound)
	request(t, router, http.MethodPost, path+"/"+entry.ID+"/restore", map[string]any{"expected_version": archived.Items[0].Version}, auth, http.StatusOK)
	restored, err := repository.ListMemories(t.Context(), user.ID, project.ID)
	if err != nil || len(restored) != 1 || restored[0].Content != "keep" {
		t.Fatal("restore lost the memory")
	}
}
