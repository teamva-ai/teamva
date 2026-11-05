package store

import (
	"encoding/json"
	"strings"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestInspirationsPersistOnlyMarkdownAndRemainGlobal(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	s := f.repository
	ctx := t.Context()
	p, err := s.CreateProject(ctx, f.user.ID, domain.Project{ComputerID: f.computer.ID, Name: "灵感项目"})
	if err != nil {
		t.Fatal(err)
	}
	markdown := "# 思考\n\n**重要** 与 [链接](https://example.test)\n\n- [ ] 未完成\n\n```go\nprintln(1)\n```\n\n正文独有的测试标记"
	n, err := s.CreateInspiration(ctx, f.user.ID, "idea-one", domain.Inspiration{ProjectID: p.ID, Title: "想法", Markdown: markdown})
	if err != nil {
		t.Fatal(err)
	}
	if n.Markdown != markdown || n.Version != 1 || n.Completed || n.CompletedAt != nil {
		t.Fatal("Markdown or initial state changed")
	}
	unchanged, err := s.UpdateInspiration(ctx, f.user.ID, n.ID, n.Version, domain.InspirationPatch{Markdown: &markdown})
	if err != nil || unchanged.Version != n.Version || !unchanged.UpdatedAt.Equal(n.UpdatedAt) {
		t.Fatal("no-op save changed the note version")
	}
	var format, stored string
	if err = s.db.QueryRow(`SELECT typeof(markdown),markdown FROM inspirations WHERE id=?`, n.ID).Scan(&format, &stored); err != nil || format != "text" || stored != markdown {
		t.Fatal("body is not canonical Markdown TEXT")
	}
	replay, err := s.CreateInspiration(ctx, f.user.ID, "idea-one", domain.Inspiration{ProjectID: p.ID, Title: "想法", Markdown: markdown})
	if err != nil || replay.ID != n.ID {
		t.Fatal("create retry duplicated note")
	}
	if _, err = s.CreateInspiration(ctx, f.user.ID, "idea-one", domain.Inspiration{Title: "不同内容"}); err == nil {
		t.Fatal("idempotency conflict accepted")
	}
	if _, err = s.GetInspiration(ctx, "other-user", n.ID); err == nil {
		t.Fatal("foreign note read")
	}
	if _, err = s.UpdateInspiration(ctx, "other-user", n.ID, n.Version, domain.InspirationPatch{}); err == nil {
		t.Fatal("foreign note write")
	}
	groups, err := s.InspirationGroups(ctx, f.user.ID, domain.InspirationQuery{})
	if err != nil || len(groups) != 3 {
		t.Fatalf("groups %d %v", len(groups), err)
	}
	groupIDs := map[string]bool{}
	for _, group := range groups {
		groupIDs[group.ProjectID] = true
	}
	if !groupIDs[""] || !groupIDs[p.ID] || !groupIDs[domain.DefaultProjectID(f.computer.ID)] {
		t.Fatalf("expected ungrouped, ordinary and built-in project groups: %+v", groups)
	}
	page, err := s.ListInspirations(ctx, f.user.ID, domain.InspirationQuery{Search: "正文独有"})
	if err != nil || page.Total != 1 || page.Items[0].ID != n.ID {
		t.Fatal("Markdown search missing note")
	}
	yes := true
	n, err = s.UpdateInspiration(ctx, f.user.ID, n.ID, n.Version, domain.InspirationPatch{Completed: &yes})
	if err != nil || !n.Completed || n.CompletedAt == nil || n.Markdown != markdown {
		t.Fatal("completion changed content or lacks timestamp")
	}
	if _, err = s.UpdateInspiration(ctx, f.user.ID, n.ID, 1, domain.InspirationPatch{Completed: &yes}); err == nil {
		t.Fatal("stale save overwrote note")
	}
	no := false
	n, err = s.UpdateInspiration(ctx, f.user.ID, n.ID, n.Version, domain.InspirationPatch{Completed: &no})
	if err != nil || n.Completed || n.CompletedAt != nil {
		t.Fatal("reopen did not clear completion")
	}
	if _, err = s.db.Exec(`UPDATE projects SET deleted_at='2026-09-16T00:00:00Z',version=version+1 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetInspiration(ctx, f.user.ID, n.ID); err != nil || got.Markdown != markdown {
		t.Fatal("project deletion removed global note")
	}
	groups, err = s.InspirationGroups(ctx, f.user.ID, domain.InspirationQuery{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range groups {
		if g.ProjectID == p.ID && g.ProjectDeleted && g.Total == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("deleted project grouping lost")
	}
	empty := ""
	n, err = s.UpdateInspiration(ctx, f.user.ID, n.ID, n.Version, domain.InspirationPatch{ProjectID: &empty})
	if err != nil || n.ProjectID != "" {
		t.Fatal("note cannot move to ungrouped")
	}
	work, err := s.QueryWorkRecords(ctx, f.user.ID, domain.WorkQuery{IncludeChanges: true, Search: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(work)
	if strings.Contains(string(raw), "正文独有") {
		t.Fatal("Markdown leaked into work records")
	}
	if err = s.DeleteInspiration(ctx, f.user.ID, n.ID, n.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetInspiration(ctx, f.user.ID, n.ID); err == nil {
		t.Fatal("deleted note is visible")
	}
	if link := s.ResolveWorkLink(ctx, f.user.ID, "inspiration", n.ID); !link.Deleted {
		t.Fatal("deleted note still linked")
	}
}

func TestInspirationAssistantReadsNotesWithoutWritePermission(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	s := f.repository
	ctx := t.Context()
	n, err := s.CreateInspiration(ctx, f.user.ID, "idea", domain.Inspiration{Title: "便签", Markdown: "# 原始 Markdown\n\n需要讨论的内容"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.EnsureGeneralAgent(ctx, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfigureGeneralAgent(ctx, f.user.ID, f.service.ID, "test-model", a.Version); err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateAssistantSession(ctx, f.user.ID, "灵感对话", domain.AssistantInspirations)
	if err != nil || session.Scope != "inspirations" {
		t.Fatal("wrong assistant scope")
	}
	workSessions, err := s.ListAssistantSessions(ctx, f.user.ID, domain.AssistantWorkHistory)
	if err != nil || len(workSessions) != 0 {
		t.Fatal("idea chats polluted work history chats")
	}
	request := domain.AssistantMessageRequest{Body: "帮我整理", AssistantSelection: domain.AssistantSelection{InspirationIDs: []string{n.ID}, InspirationQuery: &domain.InspirationQuery{Status: "all"}}}
	run, err := s.SendAssistantMessage(ctx, f.user.ID, session.ID, "ask", request)
	if err != nil || run.ProjectID != "" || !run.ReadOnly {
		t.Fatalf("global readonly run %v", err)
	}
	var raw string
	if err = s.db.QueryRow(`SELECT payload FROM command_outbox WHERE idempotency_key=?`, "run.start:"+run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload nodeprotocol.StartRunPayload
	if json.Unmarshal([]byte(raw), &payload) != nil || !strings.Contains(payload.SystemPrompt, "inspiration_get") || !strings.Contains(payload.Prompt, n.ID) {
		t.Fatal("assistant cannot discover selected note")
	}
	if strings.Contains(payload.Prompt, n.Markdown) {
		t.Fatal("whole note was copied instead of referenced")
	}
	updated := "新的 Markdown"
	if _, err = s.UpdateInspiration(ctx, f.user.ID, n.ID, n.Version, domain.InspirationPatch{Markdown: &updated}); err != nil {
		t.Fatal(err)
	}
	replay, err := s.SendAssistantMessage(ctx, f.user.ID, session.ID, "ask", request)
	if err != nil || replay.ID != run.ID {
		t.Fatal("retry changed because note was edited")
	}
	result, err := s.ExecuteWorkReadTool(ctx, f.user.ID, "inspiration_get", map[string]any{"id": n.ID})
	if err != nil || result["inspiration"].(domain.Inspiration).Markdown != updated {
		t.Fatal("assistant did not read saved Markdown")
	}
	if _, err = s.ExecuteWorkReadTool(ctx, "other-user", "inspiration_get", map[string]any{"id": n.ID}); err == nil {
		t.Fatal("assistant read another user's note")
	}
	if _, err = s.ExecuteWorkReadTool(ctx, f.user.ID, "inspiration_update", map[string]any{"id": n.ID}); err == nil {
		t.Fatal("assistant got note write tool")
	}
}

func TestInspirationLimitsAndAtomicHistory(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	defer f.repository.Close()
	s := f.repository
	if _, err := s.CreateInspiration(t.Context(), f.user.ID, "too-large", domain.Inspiration{Title: "大", Markdown: strings.Repeat("x", 262145)}); err == nil {
		t.Fatal("oversized Markdown accepted")
	}
	if _, err := s.CreateInspiration(t.Context(), f.user.ID, "foreign-project", domain.Inspiration{Title: "错", ProjectID: "not-owned"}); err == nil {
		t.Fatal("foreign project assignment accepted")
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_inspiration_history BEFORE INSERT ON work_records WHEN NEW.object_type='inspiration' BEGIN SELECT RAISE(ABORT,'test history failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInspiration(t.Context(), f.user.ID, "atomic", domain.Inspiration{Title: "回滚"}); err == nil {
		t.Fatal("note committed without history")
	}
	var count int
	_ = s.db.QueryRow(`SELECT count(*) FROM inspirations`).Scan(&count)
	if count != 0 {
		t.Fatal("partial note persisted")
	}
}
