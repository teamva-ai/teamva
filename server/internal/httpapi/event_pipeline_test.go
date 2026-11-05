package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestDiagnosticEventsPersistSafelyRetryStaysRunningAndSSEKeepsCause(t *testing.T) {
	repo, user, _, _, run := issueHistoryFixture(t)
	event := func(seq int64, kind string, payload map[string]any) nodeprotocol.RunEvent {
		return nodeprotocol.RunEvent{ID: fmt.Sprint("pipeline-", seq), RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: seq, Type: kind, Payload: payload, OccurredAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	}
	retry := []nodeprotocol.RunEvent{
		event(1, "run.started", map[string]any{}),
		event(2, "run.error", map[string]any{"willRetry": true, "error": map[string]any{"data": map[string]any{"message": "Gateway retry fixture; API_KEY=fixture-only-key"}}}),
	}
	if _, err := repo.ApplyLocalEvents(t.Context(), retry); err != nil {
		t.Fatal(err)
	}
	current, err := repo.GetRun(t.Context(), user.ID, run.ID)
	if err != nil || domain.IsRunTerminal(current.Status) {
		t.Fatalf("retry became terminal: %+v err=%v", current, err)
	}
	failed := []nodeprotocol.RunEvent{
		event(3, "context.compaction", map[string]any{"id": "compact", "status": "failed", "message": "Compaction gateway 502 fixture"}),
		event(4, "run.finished", map[string]any{"status": "failed", "error": map[string]any{"data": map[string]any{"message": "Gateway 429 quota exhausted fixture", "responseBody": `{"Authorization":"Bearer fixture-only-auth","Cookie":"session=fixture-only-cookie"}`}}}),
	}
	for range 2 {
		if _, err := repo.ApplyLocalEvents(t.Context(), failed); err != nil {
			t.Fatal("safe normalization broke replay identity", err)
		}
	}
	stored, err := repo.ListRunEvents(t.Context(), user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(stored)
	t.Logf("control-plane persisted diagnostic projection: %s", encoded)
	if strings.Contains(string(encoded), "fixture-only-") || !strings.Contains(string(encoded), "Gateway 429 quota exhausted fixture") {
		t.Fatal("raw credentials persisted or gateway detail lost")
	}
	router := browserProjectionRouter(&API{store: repo}, user)
	for _, suffix := range []string{"events?view=activity", "events/stream?after_seq=0"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/"+suffix, nil))
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK || strings.Contains(body, "fixture-only-") {
			t.Fatalf("unsafe HTTP projection status=%d", recorder.Code)
		}
		for _, marker := range []string{"Gateway retry fixture", "Gateway 429 quota exhausted fixture", "Compaction gateway 502 fixture"} {
			if !strings.Contains(body, marker) {
				t.Fatalf("HTTP/SSE lost %s", marker)
			}
		}
	}
}
