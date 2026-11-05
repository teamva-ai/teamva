package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestWorkHistoryAtomicSnapshotsFiltersAndDeletion(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	ctx := t.Context()
	s := f.repository
	message := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "test-only-context-not-for-work-history")
	fork, err := s.ForkConversationNode(ctx, f.user.ID, graph.Nodes[0].ID, message.ID, domain.ConversationNode{AgentID: f.agent.ID, Title: "分析分支"})
	if err != nil {
		t.Fatal(err)
	}
	q := domain.WorkQuery{IncludeChanges: true, Filter: &domain.WorkCondition{Join: "and", Conditions: []domain.WorkCondition{{Field: "object_type", Operator: "eq", Value: "node"}, {Join: "or", Conditions: []domain.WorkCondition{{Field: "object_id", Operator: "eq", Value: fork.ID}, {Field: "title", Operator: "contains", Value: "不存在"}}}}}}
	page, err := s.QueryWorkRecords(ctx, f.user.ID, q)
	if err != nil || page.Total != 1 {
		t.Fatalf("query count=%d err=%v", page.Total, err)
	}
	record := page.Items[0]
	if record.Refs["fork_message_id"] != message.ID || record.Refs["agent_id"] != f.agent.ID || record.ProjectID != f.project.ID {
		t.Fatal("fork identity missing")
	}
	all, err := s.QueryWorkRecords(ctx, f.user.ID, domain.WorkQuery{IncludeChanges: true, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(all)
	if strings.Contains(string(raw), "test-only-context-not-for-work-history") || strings.Contains(string(raw), "system_prompt") {
		t.Fatal("context copied into work history")
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_work_history BEFORE INSERT ON work_records BEGIN SELECT RAISE(ABORT,'test journal failure');END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, f.user.ID, domain.Project{ComputerID: f.computer.ID, Name: "must-roll-back"}); err == nil {
		t.Fatal("business mutation committed without its history")
	}
	var count int
	_ = s.db.QueryRow(`SELECT count(*) FROM projects WHERE name='must-roll-back'`).Scan(&count)
	if count != 0 {
		t.Fatal("partial business state")
	}
	_, _ = s.db.Exec(`DROP TRIGGER reject_work_history`)
	if _, err = s.QueryWorkRecords(ctx, "another-owner", q); err != nil {
		t.Fatal(err)
	}
	if other, err := s.QueryWorkRecords(ctx, "another-owner", q); err != nil || other.Total != 0 {
		t.Fatal("cross-owner query")
	}
	if _, err = s.GetWorkRecord(ctx, "another-owner", record.ID); err == nil {
		t.Fatal("cross-owner detail")
	}
	for _, bad := range []domain.WorkQuery{{Filter: &domain.WorkCondition{Field: "id) OR 1=1 --", Operator: "eq", Value: "x"}}, {Filter: &domain.WorkCondition{Join: "or"}}, {Search: strings.Repeat("a", 501)}, {From: "yesterday"}, {Limit: 201}} {
		if _, err = s.QueryWorkRecords(ctx, f.user.ID, bad); err == nil {
			t.Fatal("unbounded/invalid query accepted")
		}
	}
	filter, err := s.SaveWorkFilter(ctx, f.user.ID, domain.WorkFilter{Name: "分叉", Query: q})
	if err != nil {
		t.Fatal(err)
	}
	filters, err := s.ListWorkFilters(ctx, f.user.ID)
	if err != nil || len(filters) != 1 || filters[0].ID != filter.ID {
		t.Fatal("saved filter missing")
	}
	if _, err = s.SaveWorkFilter(ctx, "another-owner", filter); err == nil {
		t.Fatal("cross-owner filter overwrite")
	}
	if err = s.DeleteConversationNode(ctx, f.user.ID, fork.ID, fork.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetWorkRecord(ctx, f.user.ID, record.ID); err != nil {
		t.Fatal("delete cascaded into history")
	}
	link := s.ResolveWorkLink(ctx, f.user.ID, "node", fork.ID)
	if !link.Deleted || link.URL != "" {
		t.Fatal("deleted target still linked")
	}
	q = domain.WorkQuery{IncludeChanges: true, Filter: &domain.WorkCondition{Field: "id", Operator: "eq", Value: record.ID}, Until: time.Now().UTC().Format(time.RFC3339Nano)}
	if n, err := s.ClearWorkRecords(ctx, f.user.ID, q); err != nil || n != 1 {
		t.Fatalf("clear %d %v", n, err)
	}
	if _, err = s.GetConversationGraph(ctx, f.user.ID, graph.Graph.ID); err != nil {
		t.Fatal("clear deleted original graph")
	}
}

func TestGeneralAgentAndGlobalAssistantDurability(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	s := f.repository
	ctx := t.Context()
	a, err := s.EnsureGeneralAgent(ctx, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.AgentServiceID != "" || a.CurrentRevisionID != "" || a.SystemRole != "general" {
		t.Fatal("general agent is not a real unconfigured record")
	}
	again, err := s.EnsureGeneralAgent(ctx, f.user.ID)
	if err != nil || again.ID != a.ID {
		t.Fatal("general agent duplicated")
	}
	if err = s.DeleteAgent(ctx, f.user.ID, a.ID, a.Version); err == nil {
		t.Fatal("general agent deleted")
	}
	if _, err = s.db.Exec(`UPDATE agents SET deleted_at='now' WHERE id=?`, a.ID); err == nil {
		t.Fatal("SQL protection bypassed")
	}
	session, err := s.CreateAssistantSession(ctx, f.user.ID, "跨项目回顾", domain.AssistantWorkHistory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SendAssistantMessage(ctx, f.user.ID, session.ID, "ask-one", domain.AssistantMessageRequest{Body: "今天做了什么"}); err == nil {
		t.Fatal("unconfigured assistant ran")
	}
	a, err = s.ConfigureGeneralAgent(ctx, f.user.ID, f.service.ID, "test-model", a.Version)
	if err != nil || a.CurrentRevisionID == "" {
		t.Fatalf("configure %v", err)
	}
	request := domain.AssistantMessageRequest{Body: "今天做了什么", AssistantSelection: domain.AssistantSelection{Query: domain.WorkQuery{Search: "项目"}}}
	run, err := s.SendAssistantMessage(ctx, f.user.ID, session.ID, "ask-one", request)
	if err != nil {
		t.Fatal(err)
	}
	if run.ProjectID != "" || run.AssistantSessionID != session.ID || !run.ReadOnly || run.Purpose != "work_assistant" {
		t.Fatal("assistant created a fake project or writable run")
	}
	var projects int
	if err := s.db.QueryRow(`SELECT count(*) FROM projects WHERE id <> ?`, domain.DefaultProjectID(f.computer.ID)).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Fatal("fake project created")
	}
	replay, err := s.SendAssistantMessage(ctx, f.user.ID, session.ID, "ask-one", request)
	if err != nil || replay.ID != run.ID {
		t.Fatal("replay duplicated run")
	}
	if _, err = s.SendAssistantMessage(ctx, f.user.ID, session.ID, "ask-one", domain.AssistantMessageRequest{Body: "另一个问题"}); err == nil {
		t.Fatal("conflicting idempotency key accepted")
	}
	if _, err = s.SendAssistantMessage(ctx, f.user.ID, session.ID, "ask-two", domain.AssistantMessageRequest{Body: "另一个问题"}); err == nil {
		t.Fatal("concurrent assistant run admitted")
	}
	if _, err = s.AssistantMessages(ctx, "another-owner", session.ID); err == nil {
		t.Fatal("cross-owner chat read")
	}
	var raw string
	if err = s.db.QueryRow(`SELECT payload FROM command_outbox WHERE idempotency_key=?`, "run.start:"+run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var start nodeprotocol.StartRunPayload
	if err = json.Unmarshal([]byte(raw), &start); err != nil {
		t.Fatal(err)
	}
	if !start.ReadOnly || start.ComputerUse || len(start.GitResources) > 0 || start.AssistantSessionID != session.ID || start.ProjectID != "" {
		t.Fatal("unsafe assistant start payload")
	}
	if _, err = s.ListRunEvents(ctx, f.user.ID, run.ID); err != nil {
		t.Fatal("global SSE ownership failed")
	}
	_, err = s.ApplyLocalEvents(ctx, []nodeprotocol.RunEvent{{ID: "assistant-output", RunID: run.ID, LeaseEpoch: 1, Sequence: 1, Type: "output.snapshot", Payload: map[string]any{"provider_id": "codex", "text": "有据可查的回答", "final": true}, OccurredAt: time.Now()}, {ID: "assistant-finished", RunID: run.ID, LeaseEpoch: 1, Sequence: 2, Type: "run.finished", Payload: map[string]any{"status": "succeeded"}, OccurredAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := s.AssistantMessages(ctx, f.user.ID, session.ID)
	if err != nil || len(messages) != 2 || messages[1].Body != "有据可查的回答" {
		t.Fatalf("durable answer: count=%d err=%v", len(messages), err)
	}
	if _, err = s.ExecuteWorkReadTool(ctx, f.user.ID, "issue_comment_create", map[string]any{"body": "not allowed"}); err == nil {
		t.Fatal("assistant mutation tool accepted")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	messages, err = reopened.AssistantMessages(ctx, f.user.ID, session.ID)
	if err != nil || len(messages) != 2 {
		t.Fatal("assistant history lost on restart")
	}
}
