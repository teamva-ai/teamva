package httpapi

import (
	"context"
	"net/http"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

type NetworkAccessView struct {
	ExternalAccess bool     `json:"external_access"`
	ExternalURLs   []string `json:"external_urls"`
	Port           int      `json:"port"`
	PasswordSet    bool     `json:"password_set"`
	CanManage      bool     `json:"can_manage"`
	ListenError    bool     `json:"listen_error"`
}

type NetworkAccessUpdate struct {
	Enabled  *bool  `json:"enabled" binding:"required"`
	Password string `json:"password"`
}

// NetworkAccessController owns the optional listener, not business handlers.
type NetworkAccessController interface {
	View() NetworkAccessView
	Update(context.Context, NetworkAccessUpdate) (NetworkAccessView, error)
}

type remoteAccessKey struct{}

// MarkRemoteAccess is called only by the password-protected listener, never
// based on a client-supplied header (including Forwarded/X-Forwarded-For).
func MarkRemoteAccess(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), remoteAccessKey{}, true))
}

func IsRemoteAccess(r *http.Request) bool {
	marked, _ := r.Context().Value(remoteAccessKey{}).(bool)
	return marked || !IsLoopbackRemote(r.RemoteAddr)
}

func (a *API) getNetworkAccess(c *gin.Context) {
	view := NetworkAccessView{ExternalURLs: []string{}}
	if a.networkAccess != nil {
		view = a.networkAccess.View()
		view.CanManage = !IsRemoteAccess(c.Request)
	}
	respond(c, http.StatusOK, view, 0)
}

func (a *API) updateNetworkAccess(c *gin.Context) {
	// Remote guests may use the workbench, but cannot change its security gate.
	if IsRemoteAccess(c.Request) {
		respondError(c, domain.Forbidden("local_access_required", "请在本机设置远程访问"))
		return
	}
	if a.networkAccess == nil {
		respondError(c, domain.Invalid("当前服务不支持配置远程访问"))
		return
	}
	var request NetworkAccessUpdate
	if !bindJSON(c, &request) {
		return
	}
	view, err := a.networkAccess.Update(c.Request.Context(), request)
	if err != nil {
		respondError(c, err)
		return
	}
	a.audit(c, authenticatedUser(c).ID, "network_access.updated", nil)
	view.CanManage = true
	respond(c, http.StatusOK, view, 0)
}
