package store

import (
	"encoding/json"
	"net/http"
	"strings"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// isAbsoluteGitResourcePath recognizes local absolute paths without assuming
// that the control plane and the target computer use the same operating system.
func isAbsoluteGitResourcePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 &&
		((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

// ReadyGitResources is the one authoritative resource projection for Git HTTP,
// Issue Runs, mentions and schedules. A ready directory that has not actually
// been detected as Git is intentionally absent; projects with no resulting
// resources still execute in the project's stable workspace.
func ReadyGitResources(items []domain.ProjectResource) ([]nodeprotocol.GitResourceRequest, error) {
	resources := make([]nodeprotocol.GitResourceRequest, 0, len(items))
	for _, item := range items {
		if item.DeletedAt != nil || item.Kind != "code_directory" || item.Status != "ready" ||
			!item.GitDetected || !isAbsoluteGitResourcePath(item.Path) {
			continue
		}
		if len(resources) >= nodeprotocol.MaxGitResourcesPerRun {
			return nil, &domain.Error{
				Code: "git_resources_too_large", Message: "项目 Git 资源数量超过运行协议上限",
				HTTPStatus: http.StatusRequestEntityTooLarge,
				Details:    map[string]any{"max_resources": nodeprotocol.MaxGitResourcesPerRun},
			}
		}
		resource := nodeprotocol.GitResourceRequest{
			ResourceID: item.ID, Name: item.Name, Path: item.Path,
		}
		if err := validateRunGitResources([]nodeprotocol.GitResourceRequest{resource}); err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func validateRunGitResources(items []nodeprotocol.GitResourceRequest) error {
	if len(items) > nodeprotocol.MaxGitResourcesPerRun {
		return &domain.Error{
			Code: "git_resources_too_large", Message: "项目 Git 资源数量超过运行协议上限",
			HTTPStatus: http.StatusRequestEntityTooLarge,
			Details:    map[string]any{"max_resources": nodeprotocol.MaxGitResourcesPerRun},
		}
	}
	for _, item := range items {
		if len(item.ResourceID) == 0 || len(item.ResourceID) > nodeprotocol.MaxGitResourceIDBytes ||
			len(item.Name) > nodeprotocol.MaxGitResourceNameBytes || len(item.Path) == 0 ||
			len(item.Path) > nodeprotocol.MaxGitResourcePathBytes || !isAbsoluteGitResourcePath(item.Path) {
			return &domain.Error{
				Code: "git_resource_invalid", Message: "项目 Git 资源不符合运行协议边界",
				HTTPStatus: http.StatusUnprocessableEntity,
			}
		}
	}
	return nil
}

func marshalStartRunPayload(payload nodeprotocol.StartRunPayload) ([]byte, error) {
	if err := validateRunGitResources(payload.GitResources); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > nodeprotocol.MaxCommandPayloadBytes {
		return nil, &domain.Error{
			Code: "run_start_payload_too_large", Message: "运行启动数据超过 Node 协议帧上限",
			HTTPStatus: http.StatusRequestEntityTooLarge,
			Details:    map[string]any{"max_bytes": nodeprotocol.MaxCommandPayloadBytes},
		}
	}
	return encoded, nil
}
