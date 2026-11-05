package httpapi

import "net/http"

// Remote session authentication belongs to the outer remote-listener gate,
// before the shared Gin router/session middleware. Keep its path and methods
// authoritative here so OpenAPI coverage includes transport-owned endpoints.
const RemoteSessionPath = "/v1/auth/remote-session"

func RemoteSessionMethods() []string {
	return []string{http.MethodGet, http.MethodPost}
}
