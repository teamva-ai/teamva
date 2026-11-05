package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Real public HTTP/SSE + temporary app.db, with an explicit Runtime outbox
// fixture. Native sink fencing itself is covered by node/runtime force tests.
func TestForceStopPublicSSEMatchesPersistedHistoryWithCompactionSettlement(t *testing.T) {
	for _, mode := range []string{"force", "graceful", "graceful-force"} {
		t.Run(mode, func(t *testing.T) { testStopPublicSSEMatchesHistory(t, mode) })
	}
}

func testStopPublicSSEMatchesHistory(t *testing.T, mode string) {
	repo, user, _, _, run := issueHistoryFixture(t)
	now := time.Now().UTC()
	event := func(seq int64, kind string, payload map[string]any) nodeprotocol.RunEvent {
		return nodeprotocol.RunEvent{ID: "force-event-" + string(rune('0'+seq)), RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: seq, Type: kind, Payload: payload, OccurredAt: now.Add(time.Duration(seq) * time.Millisecond)}
	}
	initial := []nodeprotocol.RunEvent{
		event(1, "run.started", map[string]any{"provider_id": "opencode"}),
		event(2, "provider.event", map[string]any{"provider_id": "opencode", "kind": "error", "willRetry": true, "message": "Gateway 429 fixture; API_KEY=fixture-only-stop"}),
		event(3, "context.compaction", map[string]any{"provider_id": "opencode", "id": "compact", "status": "running"}),
	}
	if _, err := repo.ApplyLocalEvents(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	router := browserProjectionRouter(&API{store: repo}, user)
	router.POST("/runs/:run_id/stop", (&API{store: repo}).stopRun)
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/runs/"+run.ID+"/events/stream", nil)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	observed := make(chan []domain.RunEvent, 1)
	go func() {
		var items []domain.RunEvent
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if !strings.HasPrefix(scanner.Text(), "data:") {
				continue
			}
			var item domain.RunEvent
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "data:"))), &item) != nil {
				continue
			}
			items = append(items, item)
			if item.Type == "run.finished" {
				observed <- items
				return
			}
		}
		observed <- items
	}()
	initialMode := "force"
	if mode != "force" {
		initialMode = "graceful"
	}
	stop, err := server.Client().Post(server.URL+"/runs/"+run.ID+"/stop", "application/json", strings.NewReader(`{"mode":"`+initialMode+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	stop.Body.Close()
	if stop.StatusCode != http.StatusAccepted {
		t.Fatalf("public stop status=%d", stop.StatusCode)
	}
	var startID string
	if err := repo.db.QueryRowContext(t.Context(), `SELECT id FROM command_outbox WHERE command_name='run.start' AND json_extract(payload,'$.run_id')=?`, run.ID).Scan(&startID); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute, startID)
	if err != nil || !ok || claim.Name != "run.stop" {
		t.Fatalf("stop claim name=%s ok=%v err=%v", claim.Name, ok, err)
	}
	var payload nodeprotocol.StopRunPayload
	if json.Unmarshal(claim.Payload, &payload) != nil || payload.Mode != initialMode {
		t.Fatal("public force mode was discarded before Runtime delivery")
	}
	if initialMode == "graceful" {
		if err := repo.CompleteLocalCommand(t.Context(), claim.ID, claim.ClaimToken, json.RawMessage(`{"interrupted":true,"terminal_pending":true}`)); err != nil {
			t.Fatal(err)
		}
		current, err := repo.LocalRun(t.Context(), run.ID)
		if err != nil || current.Status != domain.RunCancelling || current.FinishedAt != nil {
			t.Fatalf("graceful request ACK incorrectly published a terminal before Run exit: status=%s finished=%v err=%v", current.Status, current.FinishedAt, err)
		}
		pending, err := repo.ListRunEvents(t.Context(), user.ID, run.ID)
		if err != nil || len(pending) != 5 || pending[3].Type != "context.compaction" || pending[3].Payload["status"] != "running" || pending[4].Type != "run.cancelling" {
			t.Fatalf("graceful ACK lost active compaction or closed history: %+v err=%v", pending, err)
		}
		completed, err := repo.GetLocalCommand(t.Context(), claim.ID)
		if err != nil || completed.Status != "completed" || completed.ClaimToken != "" || !strings.Contains(string(completed.Result), `"terminal_pending":true`) {
			t.Fatalf("request receipt was not durably acknowledged/fenced: %+v err=%v", completed, err)
		}
		if mode == "graceful-force" {
			for range 2 {
				response, err := server.Client().Post(server.URL+"/runs/"+run.ID+"/stop", "application/json", strings.NewReader(`{"mode":"force"}`))
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusAccepted {
					t.Fatal("force escalation was rejected")
				}
			}
			claim, ok, err = repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute, startID)
			if err != nil || !ok || json.Unmarshal(claim.Payload, &payload) != nil || payload.Mode != "force" {
				t.Fatalf("pending graceful receipt blocked force escalation: ok=%v mode=%s err=%v", ok, payload.Mode, err)
			}
		}
	}
	settlement := []nodeprotocol.RunEvent{
		event(4, "context.compaction", map[string]any{"provider_id": "opencode", "id": "compact", "status": "cancelled", "settled_by": "run_terminal"}),
		event(5, "run.finished", map[string]any{"provider_id": "opencode", "status": "cancelled", "reason": "interrupted", "message": "Run force-stopped by user"}),
	}
	for range 2 {
		cursors, err := repo.ApplyLocalEvents(t.Context(), settlement)
		if err != nil || len(cursors) != 1 || cursors[0].Sequence != 5 {
			t.Fatalf("settlement replay/ACK was not contiguous: %+v err=%v", cursors, err)
		}
	}
	if mode != "graceful" {
		if err := repo.CompleteLocalCommand(t.Context(), claim.ID, claim.ClaimToken, json.RawMessage(`{"interrupted":true}`)); err != nil {
			// Terminal relation cleanup deliberately fences obsolete in-flight control
			// commands. Their late result must not regain a claim or synthesize a row.
			obsolete, readErr := repo.GetLocalCommand(t.Context(), claim.ID)
			if readErr != nil || obsolete.Status != "failed" || obsolete.LastError != "run_terminal" {
				t.Fatalf("unexpected stop-result fencing: %+v err=%v", obsolete, err)
			}
		}
	}
	var wire []domain.RunEvent
	select {
	case wire = <-observed:
	case <-ctx.Done():
		t.Fatal("public stream did not terminate")
	}
	persisted, err := repo.ListRunEvents(t.Context(), user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wire, persisted) {
		t.Fatalf("stream/history diverged: wire=%+v persisted=%+v", wire, persisted)
	}
	terminals := 0
	for index, item := range persisted {
		if item.Sequence != int64(index+1) {
			t.Fatal("public sequence gap")
		}
		if item.Type == "run.finished" {
			terminals++
		}
	}
	if terminals != 1 || len(persisted) != 7 || persisted[5].Type != "context.compaction" || persisted[5].Payload["status"] != "cancelled" {
		t.Fatalf("terminal preceded/missed compaction settlement: %+v", persisted)
	}
	encoded, _ := json.Marshal(persisted)
	t.Logf("public SSE = app.db history: %s", encoded)
	if strings.Contains(string(encoded), "fixture-only-stop") {
		t.Fatal("diagnostic canary reached persisted or public stream")
	}
}
