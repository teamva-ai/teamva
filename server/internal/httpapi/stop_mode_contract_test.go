package httpapi

import (
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestStopRequestOnlySupportsExplicitModeTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	run := f.createRun("explicit-stop-mode")
	for _, flag := range []bool{false, true} {
		f.request(http.MethodPost, "/v1/runs/"+run.ID+"/stop", map[string]any{
			"mode": "graceful", "allow_escalation": flag,
		}, "", http.StatusBadRequest)
	}
	unchanged, err := f.repo.GetRun(t.Context(), f.user.ID, run.ID)
	if err != nil || unchanged.Status != run.Status || unchanged.Version != run.Version {
		t.Fatal("unknown stop options changed the Run")
	}
	stopped := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, "/v1/runs/"+run.ID+"/stop",
		map[string]any{"mode": "force"}, "", http.StatusAccepted))
	if stopped.ID != run.ID || stopped.Status != domain.RunCancelling {
		t.Fatal("accepted stop must durably request cancellation, not claim Provider delivery")
	}
}
