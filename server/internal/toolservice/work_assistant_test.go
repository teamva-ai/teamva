package toolservice

import (
	"path/filepath"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

func TestWorkAssistantAuthorizesFromPersistedRunAndRejectsWrites(t *testing.T) {
	s, err := store.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	user, computer, err := s.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := s.UpsertAgentService(t.Context(), user.ID, domain.AgentService{ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated", Capabilities: map[string]any{}, CapabilityVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.EnsureGeneralAgent(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfigureGeneralAgent(t.Context(), user.ID, provider.ID, "test-model", agent.Version); err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateAssistantSession(t.Context(), user.ID, "test session", domain.AssistantWorkHistory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.SendAssistantMessage(t.Context(), user.ID, session.ID, "test-idempotency", domain.AssistantMessageRequest{Body: "test question"})
	if err != nil {
		t.Fatal(err)
	}
	service := New(s)
	request := nodeprotocol.NodeRequest{RunID: run.ID, RequestID: "read", Tool: "work_records_query", Payload: map[string]any{"user_id": "different-owner", "include_changes": true}}
	if service.Execute(t.Context(), computer.ID, request).Result.OK {
		t.Fatal("unknown identity field accepted")
	}
	request.Payload = map[string]any{"include_changes": true}
	result := service.Execute(t.Context(), computer.ID, request)
	if !result.Result.OK {
		t.Fatalf("read denied: %s", result.Result.ErrorCode)
	}
	for _, tool := range []string{"issue_comment_create", "issue_comment_update_own", "work_records_clear", "unknown"} {
		request.Tool = tool
		if service.Execute(t.Context(), computer.ID, request).Result.OK {
			t.Fatal("assistant acquired a mutation tool")
		}
	}
	request.Tool = "work_records_query"
	if service.Execute(t.Context(), "other-computer", request).Result.OK {
		t.Fatal("computer boundary bypassed")
	}
	if err = s.CancelLocalRun(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if service.Execute(t.Context(), computer.ID, request).Result.OK {
		t.Fatal("terminal assistant run could still query")
	}
}
