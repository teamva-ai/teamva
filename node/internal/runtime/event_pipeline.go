package runtime

import (
	"context"
	"sort"

	adapter "github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

type safeExecutionError struct{ cause error }

func (e *safeExecutionError) Error() string {
	return nodeprotocol.SanitizeDiagnosticText(e.cause.Error())
}
func (e *safeExecutionError) Unwrap() error { return e.cause }

func durableDiagnosticEvent(event adapter.Event) adapter.Event {
	if event.Type == "run.error" || event.Type == "run.finished" || event.Type == "run.completed" || event.Type == "context.compaction" ||
		event.Payload["willRetry"] == true || event.Payload["kind"] == "error" {
		event.Payload = nodeprotocol.NormalizeDiagnosticPayload(event.Payload)
		// Retry notifications are observable diagnostics, never execution terminals.
		if event.Type == "run.error" && event.Payload["willRetry"] == true {
			event.Type = "provider.event"
			event.Payload["kind"] = "error"
		}
	}
	return redactInlineImageDataFromEvent(event)
}

// Access is serialized with the Run's terminal mutex. A Run terminal must never
// precede compaction settlement: clients close their SSE at the first terminal.
type compactionTracker map[string]map[string]any

func (c compactionTracker) observe(event adapter.Event) {
	if event.Type != "context.compaction" {
		return
	}
	id, _ := event.Payload["id"].(string)
	if id == "" {
		id = event.ID
	}
	if id == "" {
		id = "context"
	}
	status, _ := event.Payload["status"].(string)
	if status == "running" || status == "started" || status == "pending" {
		c[id] = event.Payload
	} else {
		delete(c, id)
	}
}

func (c compactionTracker) settle(ctx context.Context, terminal adapter.Event, appendEvent func(context.Context, adapter.Event) error) error {
	status := catalog.TerminalStatus(terminal.Payload)
	settled := "failed"
	if terminal.Type != "run.error" && (status == "completed" || status == "succeeded" || status == "interrupted" || status == "cancelled") {
		settled = "cancelled"
	}
	ids := make([]string, 0, len(c))
	for id := range c {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		payload := map[string]any{"id": id, "status": settled, "settled_by": "run_terminal", "provider_id": terminal.Payload["provider_id"]}
		if message, _ := terminal.Payload["message"].(string); message != "" {
			payload["message"] = message
		}
		if err := appendEvent(ctx, adapter.Event{Type: "context.compaction", Payload: payload}); err != nil {
			return err
		}
		delete(c, id)
	}
	return nil
}
