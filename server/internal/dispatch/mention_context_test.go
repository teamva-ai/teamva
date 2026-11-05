package dispatch

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

func TestMentionPromptBoundsHistoryAndRetainsOldActivationSource(t *testing.T) {
	repo, err := store.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repo.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{ComputerID: computer.ID, AgentServiceID: service.ID, Name: "mention context", Status: "active"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "mention context"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repo.CreateIssue(t.Context(), user.ID, domain.Issue{ProjectID: project.ID, Title: strings.Repeat("题", 10_000), Description: strings.Repeat("描", 40_000)})
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := repo.CreateIssueComment(t.Context(), user.ID, domain.IssueComment{IssueID: issue.ID, AuthorKind: "user", Body: "original-activation-marker"}, []string{agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 105; index++ {
		if _, err := repo.CreateIssueComment(t.Context(), user.ID, domain.IssueComment{IssueID: issue.ID, AuthorKind: "user", Body: strings.Repeat("新", 5_000)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	local := &Local{repository: repo}
	if worked, err := local.processMention(t.Context()); err != nil || !worked {
		t.Fatalf("mention worked=%t err=%v", worked, err)
	}
	runs, err := repo.ListRunsByIssue(t.Context(), user.ID, issue.ID, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	prompt := runs[0].Input[0].Text
	if len(prompt) > 128<<10 || !utf8.ValidString(prompt) || !strings.Contains(prompt, trigger.ID) ||
		!strings.Contains(prompt, "original-activation-marker") || !strings.Contains(prompt, "裁剪") {
		t.Fatalf("mention lost source or exceeded prompt budget: bytes=%d", len(prompt))
	}
	for _, required := range []string{
		"本轮若向其他 Agent 委派", "结构化 @ 是异步委派", "确认本次工具提交成功后结束当前 Run",
		"不等对方完成", "不轮询等结果", "不再调用 issue_comment_create 或 issue_comment_update_own 回写本轮已发布的委派评论",
		"委派说明即为本轮结果", "若需要结果回来继续",
		"结果评论的 mentioned_agent_ids", "显式 @回本 Agent ID", "正文 @名字不算激活", "新 Run",
		"只按明确的回报要求", "结果接收者无需无意义回@", "允许有实际需要的 A→B→A",
		"不限制正常上下文读取或用户显式要求的评论修正", "独立审核完成门禁不变",
		"每个 Agent 会话只维护一条总结评论", "append_new=true", "当前工作回复",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("persisted mention prompt missing %q", required)
		}
	}
}
