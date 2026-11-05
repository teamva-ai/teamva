package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Keep the optional artifact transaction capability separate from the broad
// repository interface. A repository without it must fail closed, not fall back
// to committing a database-only uninstall before removing files.
type skillArtifactUninstaller interface {
	UninstallComputerSkillPackageWithArtifact(context.Context, string, string, string, int64, func(context.Context, string) error) error
}

func skillUninstallUnsupported() error {
	return &domain.Error{
		Code: "capability_unsupported", Message: "当前运行模式不支持安全卸载本机 Skill，安装记录已保留",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (a *API) removeSkillArtifact(ctx context.Context, slug string) error {
	executor, ok := a.runtime.(localCommandExecutor)
	if !ok {
		return skillUninstallUnsupported()
	}
	raw, err := executor.ExecuteLocalCommand(ctx, "skill.uninstall", nodeprotocol.UninstallSkillPayload{Slug: slug})
	if err == nil {
		var result struct {
			Uninstalled bool `json:"uninstalled"`
		}
		if json.Unmarshal(raw, &result) == nil && result.Uninstalled {
			return nil
		}
	}
	// Runtime errors and payloads may contain local paths or other private data.
	// Return only an actionable, stable explanation; never log or echo them.
	return &domain.Error{
		Code: "skill_uninstall_failed", Message: "Skill 文件未能确认卸载，安装记录已保留，请重试卸载；如需继续使用，请重新安装",
		HTTPStatus: http.StatusServiceUnavailable, Retryable: true,
		Details: map[string]any{"installation_record_retained": true, "artifact_state": "unknown", "retry_action": "uninstall"},
	}
}
