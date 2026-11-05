package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func assistantFixture(t *testing.T, scope domain.AssistantScope) (sqliteAgentTestFixture, domain.AssistantSession) {
	t.Helper()
	fixture := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = fixture.repository.Close() })
	agent, err := fixture.repository.EnsureGeneralAgent(t.Context(), fixture.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.ConfigureGeneralAgent(t.Context(), fixture.user.ID, fixture.service.ID, "test", agent.Version); err != nil {
		t.Fatal(err)
	}
	session, err := fixture.repository.CreateAssistantSession(t.Context(), fixture.user.ID, "test", scope)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, session
}

func TestAssistantReplayDoesNotDependOnMutableReferences(t *testing.T) {
	for _, scope := range []domain.AssistantScope{domain.AssistantWorkHistory, domain.AssistantInspirations} {
		t.Run(string(scope), func(t *testing.T) {
			fixture, session := assistantFixture(t, scope)
			s, user := fixture.repository, fixture.user.ID
			request := domain.AssistantMessageRequest{Body: "review"}
			var remove func()
			if scope == domain.AssistantInspirations {
				note, err := s.CreateInspiration(t.Context(), user, "note", domain.Inspiration{Title: "reference"})
				if err != nil {
					t.Fatal(err)
				}
				request.InspirationIDs = []string{note.ID}
				remove = func() {
					if err := s.DeleteInspiration(t.Context(), user, note.ID, note.Version); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				record := domain.WorkRecord{ID: "reference", Kind: "operation", Action: "test", ActorType: "user", Outcome: "succeeded"}
				if err := s.AppendWorkRecord(t.Context(), user, record); err != nil {
					t.Fatal(err)
				}
				request.SelectedIDs = []string{record.ID}
				remove = func() {
					if _, err := s.db.Exec(`DELETE FROM work_records WHERE id=?`, record.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			first, err := s.SendAssistantMessage(t.Context(), user, session.ID, "retry-key", request)
			if err != nil {
				t.Fatal(err)
			}
			remove()
			if _, err := s.db.Exec(`UPDATE agent_services SET status='unavailable' WHERE id=?`, fixture.service.ID); err != nil {
				t.Fatal(err)
			}
			replay, err := s.SendAssistantMessage(t.Context(), user, session.ID, "retry-key", request)
			if err != nil || replay.ID != first.ID {
				t.Fatalf("replay lost after referenced state changed: %v", err)
			}
			request.Body = "different"
			_, err = s.SendAssistantMessage(t.Context(), user, session.ID, "retry-key", request)
			assertDomainCode(t, err, "idempotency_conflict")
		})
	}
}

func TestAssistantCapacityStillAllowsReplay(t *testing.T) {
	fixture, session := assistantFixture(t, domain.AssistantWorkHistory)
	s, user := fixture.repository, fixture.user.ID
	request := domain.AssistantMessageRequest{Body: "original"}
	first, err := s.SendAssistantMessage(t.Context(), user, session.ID, "original", request)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelLocalRun(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := sqliteTimeValue(time.Now())
	for i := 1; i < domain.AssistantMessageLimit/2; i++ {
		runID := fmt.Sprintf("fixture-run-%d", i)
		_, err := tx.Exec(`INSERT INTO runs(id,agent_id,computer_id,assistant_session_id,status,read_only,input,idempotency_key,purpose)
		 VALUES(?,?,?,?,'succeeded',1,'[]',?,'work_assistant')`, runID, session.AgentID, fixture.computer.ID, session.ID, runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, author := range []string{"user", "agent"} {
			_, err := tx.Exec(`INSERT INTO assistant_messages(id,session_id,author_kind,body,run_id,query,created_at)
			 VALUES(?,?,?,'fixture',?,'{"query":{},"selected_ids":null}',?)`, runID+author, session.ID, author, runID, stamp)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	replay, err := s.SendAssistantMessage(t.Context(), user, session.ID, "original", request)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("full conversation rejected valid replay: %v", err)
	}
	_, err = s.SendAssistantMessage(t.Context(), user, session.ID, "new", request)
	assertDomainCode(t, err, "invalid_argument")
	data, err := s.ExecuteWorkReadTool(t.Context(), user, "work_history_read", map[string]any{"kind": "assistant", "id": session.ID, "offset": 490, "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	page := data["page"].(assistantHistoryPage)
	if len(data["messages"].([]domain.AssistantMessage)) != 5 || page.Total != 500 || page.NextOffset != 495 || !page.HasMore {
		t.Fatalf("wrong database pagination: %+v", page)
	}
}

func TestAssistantConcurrentReplayHasOneRunAndMessagePair(t *testing.T) {
	fixture, session := assistantFixture(t, domain.AssistantWorkHistory)
	request := domain.AssistantMessageRequest{Body: "concurrent"}
	var group sync.WaitGroup
	ids := make(chan string, 4)
	for range 4 {
		group.Go(func() {
			run, err := fixture.repository.SendAssistantMessage(t.Context(), fixture.user.ID, session.ID, "one", request)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- run.ID
		})
	}
	group.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("replay created another run")
		}
	}
	messages, err := fixture.repository.AssistantMessages(t.Context(), fixture.user.ID, session.ID)
	if err != nil || len(messages) != 2 {
		t.Fatal("duplicate message pair")
	}
}

func TestAssistantHistoryStrictArgumentsAndDirectMessageRead(t *testing.T) {
	fixture, graph := createConversationFixture(t)
	defer fixture.repository.Close()
	message := appendConversationMessage(t, fixture, graph.Nodes[0].ID, "user", "original body")
	for _, payload := range []map[string]any{
		{"kind": "node", "id": graph.Nodes[0].ID, "offset": "1"},
		{"kind": "node", "id": graph.Nodes[0].ID, "offset": 1.5},
		{"kind": "node", "id": graph.Nodes[0].ID, "limit": 0},
		{"kind": "unknown", "id": message.ID},
		{"kind": "message", "id": message.ID, "user_id": "spoof"},
	} {
		if _, err := fixture.repository.ExecuteWorkReadTool(t.Context(), fixture.user.ID, "work_history_read", payload); err == nil {
			t.Fatal("invalid tool arguments accepted")
		}
	}
	result, err := fixture.repository.ExecuteWorkReadTool(t.Context(), fixture.user.ID, "work_history_read", map[string]any{"kind": "message", "id": message.ID})
	if err != nil || result["message"].(domain.ConversationMessage).Body != message.Body {
		t.Fatalf("direct message lookup failed: %v", err)
	}
	foreign, err := fixture.repository.ExecuteWorkReadTool(t.Context(), "other-user", "work_history_read", map[string]any{"kind": "message", "id": message.ID})
	if err != nil || foreign["unavailable"] != true {
		t.Fatal("cross-owner history leaked")
	}
}

func TestAssistantCurrentSchemaGuards(t *testing.T) {
	fixture, session := assistantFixture(t, domain.AssistantWorkHistory)
	s := fixture.repository
	run, err := s.SendAssistantMessage(t.Context(), fixture.user.ID, session.ID, "guard", domain.AssistantMessageRequest{Body: "guard"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateAssistantSession(t.Context(), fixture.user.ID, "other", domain.AssistantWorkHistory)
	if err != nil {
		t.Fatal(err)
	}
	stamp := sqliteTimeValue(time.Now())
	for _, test := range []struct{ id, sessionID, author, selection string }{
		{"wrong-scope", other.ID, "agent", `{"query":{},"selected_ids":null}`},
		{"duplicate-answer", session.ID, "agent", `{"query":{},"selected_ids":null}`},
		{"invalid-selection", session.ID, "user", `[]`},
	} {
		_, err := s.db.Exec(`INSERT INTO assistant_messages(id,session_id,author_kind,body,run_id,query,created_at) VALUES(?,?,?,'',?,?,?)`, test.id, test.sessionID, test.author, run.ID, test.selection, stamp)
		if err == nil {
			t.Fatalf("invalid message accepted: %s", test.id)
		}
	}
	if _, err := s.db.Exec(`UPDATE assistant_messages SET session_id=? WHERE run_id=? AND author_kind='user'`, other.ID, run.ID); err == nil {
		t.Fatal("message could be moved to another run's conversation")
	}
	if _, err := s.db.Exec(`UPDATE assistant_messages SET query='[]' WHERE run_id=? AND author_kind='user'`, run.ID); err == nil {
		t.Fatal("invalid selection format accepted")
	}
	if _, err := s.ListAssistantSessions(t.Context(), fixture.user.ID, ""); err == nil {
		t.Fatal("repository silently chose a scope")
	}
	if _, err := s.db.Exec(`INSERT INTO agents(id,computer_id,name,status) VALUES('unconfigured-normal',?,'invalid','active')`, fixture.computer.ID); err == nil {
		t.Fatal("ordinary agent without provider accepted")
	}
}
