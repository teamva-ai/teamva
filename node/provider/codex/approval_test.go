package codex

import (
	"testing"

	adapter "github.com/teamva-ai/teamva/node/provider"
)

func TestInteractiveRunNeverAsksForApproval(t *testing.T) {
	request := adapter.RunRequest{CWD: "/tmp/project", Prompt: "run ls"}
	for name, params := range map[string]map[string]any{
		"thread": threadStartParams(request),
		"turn":   turnStartParams(request, "thread-1"),
	} {
		t.Run(name, func(t *testing.T) {
			if params["approvalPolicy"] != "never" {
				t.Fatalf("interactive approval policy = %v", params["approvalPolicy"])
			}
		})
	}
}

func TestReadOnlyRunCannotEscalateApproval(t *testing.T) {
	request := adapter.RunRequest{CWD: "/tmp/project", Prompt: "scheduled", ReadOnly: true}
	for name, params := range map[string]map[string]any{
		"thread": threadStartParams(request),
		"turn":   turnStartParams(request, "thread-1"),
	} {
		t.Run(name, func(t *testing.T) {
			if params["approvalPolicy"] != "never" {
				t.Fatalf("read-only approval policy = %v", params["approvalPolicy"])
			}
		})
	}
}

func TestWorkAssistantOnlyGrantsPrivateIPCWrites(t *testing.T) {
	r := adapter.RunRequest{CWD: "/private/app/runtime/workspaces/home/session", ReadOnly: true, ToolRequestDir: "/private/app/runtime/workspaces/home/session/.teamva/tool-requests/run"}
	thread, turn := threadStartParams(r), turnStartParams(r, "thread")
	if thread["approvalPolicy"] != "never" || turn["approvalPolicy"] != "never" {
		t.Fatal("assistant can escalate")
	}
	if _, legacy := turn["sandboxPolicy"]; legacy || turn["permissions"] != codexPermissionProfile {
		t.Fatal("assistant must not use legacy implicit-CWD workspaceWrite")
	}
	roots, ok := turn["runtimeWorkspaceRoots"].([]string)
	if !ok || len(roots) != 1 || roots[0] != r.ToolRequestDir {
		t.Fatal("assistant gained business workspace write roots")
	}
}

func TestOnlyCodexApprovalMethodsBecomeApprovalCards(t *testing.T) {
	for _, method := range []string{
		"item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval",
		"execCommandApproval",
		"applyPatchApproval",
	} {
		if !isApprovalRequest(method) {
			t.Fatalf("native approval method %q was not recognized", method)
		}
	}
	for _, method := range []string{
		"item/tool/requestUserInput",
		"item/tool/call",
		"mcpServer/elicitation/request",
		"account/chatgptAuthTokens/refresh",
		"attestation/generate",
	} {
		if isApprovalRequest(method) {
			t.Fatalf("non-approval method %q would create an approval card", method)
		}
	}
}

func TestRetryingCodexErrorIsNotTerminal(t *testing.T) {
	if got := codexErrorEventKind(map[string]any{"willRetry": true}); got != "provider.event" {
		t.Fatalf("retrying error event kind = %q", got)
	}
	if got := codexErrorEventKind(map[string]any{"willRetry": false}); got != "run.error" {
		t.Fatalf("terminal error event kind = %q", got)
	}
}
