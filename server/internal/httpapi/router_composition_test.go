package httpapi

import (
	"testing"

	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

// fakeLocalStore deliberately promotes only the local/core contract. It has
// no RemoteAuth or RemoteNodeEnrollment methods.
type fakeLocalStore struct {
	store.CoreLocalRepository
}

var (
	_ store.CoreLocalRepository                                             = (*fakeLocalStore)(nil)
	_ func(store.CoreLocalRepository, Runtime, string, Options) *gin.Engine = NewLocal
)

func TestLocalCompositionAcceptsCoreOnlyStoreAndOmitsRemoteRoutes(t *testing.T) {
	router := NewLocal(&fakeLocalStore{}, nil, "test", Options{
		LocalUserID: "usr_local", LocalBootstrapNonce: "nonce",
	})
	routes := make(map[string]bool)
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	if !routes["GET /desktop/bootstrap"] || !routes["GET /v1/projects"] {
		t.Fatalf("local routes missing: %#v", routes)
	}
	for _, remoteRoute := range []string{
		"POST /v1/auth/sessions",
		"POST /v1/auth/verifications",
		"POST /v1/device/codes",
		"GET /v1/node/connect",
	} {
		if routes[remoteRoute] {
			t.Fatalf("remote route %q was registered by local composition", remoteRoute)
		}
	}
}
