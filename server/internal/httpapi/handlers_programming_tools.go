package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

// ProgrammingToolExecutable is the settings projection of one Provider's CLI
// resolution. Path is the user-configured override ("" = automatic) and
// Resolved is what the Runtime actually executes now.
type ProgrammingToolExecutable struct {
	Provider string `json:"provider"`
	Path     string `json:"path"`
	Resolved string `json:"resolved"`
	Source   string `json:"source"`
}

type updateProgrammingToolExecutableRequest struct {
	Path *string `json:"path" binding:"required,max=4096"`
}

// Executables are process/machine-scoped capabilities, not per-request data.
// Serialize settings projections and changes, including failure compensation,
// so one failed save cannot roll back a newer successful runtime update.
var programmingToolExecutableSettingsMu sync.Mutex

// normalizeProgrammingToolExecutable accepts an empty value (restore the
// default resolution) or an executable file path. `~` is expanded for
// convenience; everything else must be an absolute file with an executable
// bit. The path is validated before any Runtime or database change.
func normalizeProgrammingToolExecutable(requestPath string) (string, error) {
	value := strings.TrimSpace(requestPath)
	if value == "" {
		return "", nil
	}
	if value == "~" {
		return "", errors.New("请填写可执行文件的完整路径")
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法解析用户主目录: %w", err)
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	if !filepath.IsAbs(value) {
		return "", errors.New("请填写可执行文件的绝对路径")
	}
	value = filepath.Clean(value)
	info, err := os.Stat(value)
	if err != nil {
		return "", fmt.Errorf("无法访问该文件: %w", err)
	}
	if info.IsDir() {
		return "", errors.New("路径指向目录，不是可执行文件")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("路径不是普通文件")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("文件没有可执行权限")
	}
	return value, nil
}

func (a *API) listProgrammingToolExecutables(c *gin.Context) {
	programmingToolExecutableSettingsMu.Lock()
	defer programmingToolExecutableSettingsMu.Unlock()
	lister, ok := a.runtime.(interface {
		ProviderExecutables() ([]nodeprotocol.ProviderExecutable, error)
	})
	if !ok {
		respondError(c, &domain.Error{
			Code: "capability_unsupported", Message: "本地 Runtime 不支持可执行文件设置",
			HTTPStatus: http.StatusNotImplemented,
		})
		return
	}
	items, err := lister.ProviderExecutables()
	if err != nil {
		respondError(c, err)
		return
	}
	result := make([]ProgrammingToolExecutable, 0, len(items))
	for _, item := range items {
		stored, storedErr := a.store.ProviderExecutablePath(c.Request.Context(), item.Provider)
		if storedErr != nil {
			respondError(c, storedErr)
			return
		}
		result = append(result, ProgrammingToolExecutable{
			Provider: item.Provider, Path: stored, Resolved: item.Path, Source: item.Source,
		})
	}
	respond(c, http.StatusOK, domain.Page[ProgrammingToolExecutable]{Items: result}, 0)
}

func (a *API) updateProgrammingToolExecutable(c *gin.Context) {
	// Remote guests may use the workbench, but the local runtime's binaries are
	// a machine-level setting only the local entry point may change.
	if IsRemoteAccess(c.Request) {
		respondError(c, domain.Forbidden("local_access_required", "请在本机设置编程工具路径"))
		return
	}
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if _, supported := catalog.Lookup(provider); !supported {
		respondError(c, domain.Invalid("暂不支持设置该编程工具"))
		return
	}
	var request updateProgrammingToolExecutableRequest
	if !bindJSON(c, &request) {
		return
	}
	path, err := normalizeProgrammingToolExecutable(*request.Path)
	if err != nil {
		respondError(c, domain.Invalid(err.Error()))
		return
	}
	updater, ok := a.runtime.(interface {
		SetProviderExecutable(context.Context, string, string) (nodeprotocol.ProviderExecutable, error)
	})
	if !ok {
		respondError(c, &domain.Error{
			Code: "capability_unsupported", Message: "本地 Runtime 不支持可执行文件设置",
			HTTPStatus: http.StatusNotImplemented,
		})
		return
	}
	programmingToolExecutableSettingsMu.Lock()
	defer programmingToolExecutableSettingsMu.Unlock()
	previousPath, err := a.store.ProviderExecutablePath(c.Request.Context(), provider)
	if err != nil {
		respondError(c, err)
		return
	}
	item, err := updater.SetProviderExecutable(c.Request.Context(), provider, path)
	if err != nil {
		respondError(c, err)
		return
	}
	if err := a.store.SetProviderExecutablePath(c.Request.Context(), provider, path); err != nil {
		// The runtime has already swapped its idle capability. Restore the durable
		// value even when the HTTP request was canceled during SQLite persistence.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
		_, rollbackErr := updater.SetProviderExecutable(rollbackCtx, provider, previousPath)
		cancel()
		if rollbackErr != nil {
			respondError(c, &domain.Error{
				Code: "programming_tool_update_incomplete", Message: "编程工具路径保存失败，运行时状态未能确认，请重试设置",
				HTTPStatus: http.StatusServiceUnavailable, Retryable: true,
				Details: map[string]any{"durable_setting_retained": true, "runtime_state": "unknown", "retry_action": "save"},
			})
			return
		}
		respondError(c, err)
		return
	}
	a.audit(c, authenticatedUser(c).ID, "programming_tool.executable_updated", map[string]any{"provider": provider})
	respond(c, http.StatusOK, ProgrammingToolExecutable{
		Provider: provider, Path: path, Resolved: item.Path, Source: item.Source,
	}, 0)
}
