package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func nativeApprovalFixture(t *testing.T, mutate func(map[string]any)) rpcMessage {
	t.Helper()
	params := map[string]any{"threadId": "thread", "turnId": "turn", "serverName": "amqa-local", "mode": "form", "message": "Allow amqa-local to run qa_echo?",
		"_meta":           map[string]any{"codex_approval_kind": "mcp_tool_call", "persist": []string{"session"}, "tool_params": map[string]any{"text": "fixture"}},
		"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}}
	if mutate != nil {
		mutate(params)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return rpcMessage{ID: json.RawMessage(`"native-approval"`), Method: "mcpServer/elicitation/request", Params: encoded}
}

func TestOnlyNativeMCPToolApprovalFormsUseBinaryApprovalUI(t *testing.T) {
	message := nativeApprovalFixture(t, nil)
	if _, ok := nativeMCPToolApproval(message); !ok {
		t.Fatal("actual native MCP tool approval not recognized")
	}
	for _, mutate := range []func(map[string]any){
		func(p map[string]any) { delete(p, "_meta") },
		func(p map[string]any) { p["_meta"] = map[string]any{"codex_approval_kind": "browser_auth"} },
		func(p map[string]any) { p["mode"] = "url" },
		func(p map[string]any) { p["mode"] = "openai/userVerification" },
		func(p map[string]any) {
			p["requestedSchema"] = map[string]any{"type": "object", "properties": map[string]any{"password": map[string]any{"type": "string"}}}
		},
		func(p map[string]any) {
			p["requestedSchema"] = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{"answer"}}
		},
	} {
		if _, ok := nativeMCPToolApproval(nativeApprovalFixture(t, mutate)); ok {
			t.Fatal("non-approval elicitation was silently reduced to approve/deny")
		}
	}
	for _, tc := range []struct {
		decision, scope, action string
		meta                    any
	}{
		{"approved", "once", "accept", nil}, {"denied", "once", "decline", nil},
		{"approved", "session", "accept", map[string]any{"persist": "session"}},
		{"approved", "always", "accept", nil},
	} {
		got := approvalResult(message, adapter.ApprovalDecision{Decision: tc.decision, Scope: tc.scope})
		want := map[string]any{"action": tc.action, "content": nil, "_meta": tc.meta}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("native elicitation response=%v want=%v", got, want)
		}
	}
}

func TestNativeMCPApprovalUsesExistingSinkAndCannotCrossRunOrThread(t *testing.T) {
	for _, tc := range []struct {
		name         string
		readOnly     bool
		thread, turn string
		want         int
	}{
		{"interactive", false, "thread", "turn", 1}, {"readonly", true, "thread", "turn", 0},
		{"foreign-thread", false, "other", "turn", 0}, {"foreign-turn", false, "thread", "other", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := New("unused")
			count := 0
			client.active = &runState{ctx: t.Context(), threadID: tc.thread, turnID: tc.turn, request: adapter.RunRequest{RunID: "run", ReadOnly: tc.readOnly}, sink: func(_ context.Context, event adapter.Event) error {
				count++
				if event.Type != "approval.requested" || event.Payload["action_kind"] != "mcpServer/elicitation/request" || event.Payload["request_id"] != `"native-approval"` {
					t.Fatal("approval did not use the existing Runtime event contract")
				}
				return nil
			}}
			message := nativeApprovalFixture(t, nil)
			client.handleServerRequest(message, nil)
			if count != tc.want || len(client.approvals) != tc.want {
				t.Fatal("MCP approval ownership/read-only boundary failed")
			}
			if tc.want == 1 {
				stale := message
				stale.ID = json.RawMessage(`"stale-epoch"`)
				client.handleServerRequest(stale, nil, 1)
				if count != 1 || len(client.approvals) != 1 {
					t.Fatal("stale native process published a new approval")
				}
				if err := client.DecideApproval(t.Context(), adapter.ApprovalDecision{RunID: "other-run", RequestID: `"native-approval"`, Decision: "approved"}); err == nil {
					t.Fatal("foreign Run approved this request")
				}
				if len(client.approvals) != 1 {
					t.Fatal("foreign Run consumed the real pending approval")
				}
			}
		})
	}
}
