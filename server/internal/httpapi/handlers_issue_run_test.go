package httpapi

import (
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func assertIssueAsyncDelegationPrompt(t *testing.T, prompt string) {
	t.Helper()
	for _, required := range []string{
		"本轮若向其他 Agent 委派", "结构化 @ 是异步委派", "确认本次工具提交成功后结束当前 Run",
		"不等对方完成", "不轮询等结果", "不再调用 issue_comment_create 或 issue_comment_update_own 回写本轮已发布的委派评论",
		"委派说明即为本轮结果", "若需要结果回来继续",
		"issue_comment_create", "issue_comment_update_own", "结果评论的 mentioned_agent_ids", "显式 @回本 Agent ID",
		"正文 @名字不算激活", "新 Run", "只按明确的回报要求", "结果接收者无需无意义回@",
		"允许有实际需要的 A→B→A", "不限制正常上下文读取或用户显式要求的评论修正",
		"独立审核完成门禁不变", "每个 Agent 会话只维护一条总结评论", "append_new=true",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("actual Issue prompt missing %q", required)
		}
	}
}

func TestIssuePromptAsyncDelegationContract(t *testing.T) {
	repo, user, issue, instance, _ := issueHistoryFixture(t)
	api := &API{store: repo}
	prompt, err := api.buildIssuePrompt(t.Context(), user.ID, issue.ID, instance, "delegate-contract-current-instruction", true)
	if err != nil {
		t.Fatal(err)
	}
	assertIssueAsyncDelegationPrompt(t, prompt)
	if !strings.Contains(prompt, "delegate-contract-current-instruction") {
		t.Fatal("async rules replaced the genuine user instruction")
	}
}

func TestIssueGoalInstructionKeepsWorkingSummaryAndIndependentGate(t *testing.T) {
	prompt := initialGoalInstruction("goal-contract-objective")
	for _, required := range []string{
		"goal-contract-objective", "issue_comment_create 更新当前目标的工作进度", "复用同一条工作评论",
		"不要自行终止持续目标", "系统会独立决定继续、完成或阻塞",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("Goal prompt missing %q", required)
		}
	}
}

func TestLatestRevisableCommentStaysInsideAgentInstance(t *testing.T) {
	comments := []domain.IssueComment{
		{
			Versioned:  domain.Versioned{ID: "comment-first", Version: 2},
			AuthorKind: domain.IssueCommentAuthorAgent, AuthorAgentID: "agent-1",
			RunID: "run-instance-1", Status: domain.IssueCommentPublished,
		},
		{
			Versioned:  domain.Versioned{ID: "comment-other-session", Version: 4},
			AuthorKind: domain.IssueCommentAuthorAgent, AuthorAgentID: "agent-1",
			RunID: "run-instance-2", Status: domain.IssueCommentPublished,
		},
		{
			Versioned:  domain.Versioned{ID: "comment-latest", Version: 7},
			AuthorKind: domain.IssueCommentAuthorAgent, AuthorAgentID: "agent-1",
			RunID: "run-instance-1-followup", Status: domain.IssueCommentPublished,
		},
	}
	runs := []domain.Run{
		{Versioned: domain.Versioned{ID: "run-instance-1"}},
		{Versioned: domain.Versioned{ID: "run-instance-1-followup"}},
	}

	comment, ok := latestRevisableComment(comments, runs, "agent-1")
	if !ok || comment.ID != "comment-latest" {
		t.Fatalf("comment=%+v ok=%t", comment, ok)
	}
}

func TestLatestRevisableCommentRejectsWorkingAndOtherAgentComments(t *testing.T) {
	comments := []domain.IssueComment{
		{
			Versioned:  domain.Versioned{ID: "working", Version: 1},
			AuthorKind: domain.IssueCommentAuthorAgent, AuthorAgentID: "agent-1",
			RunID: "run-1", Status: domain.IssueCommentWorking,
		},
		{
			Versioned:  domain.Versioned{ID: "other-agent", Version: 1},
			AuthorKind: domain.IssueCommentAuthorAgent, AuthorAgentID: "agent-2",
			RunID: "run-1", Status: domain.IssueCommentPublished,
		},
	}
	if comment, ok := latestRevisableComment(comments, []domain.Run{{Versioned: domain.Versioned{ID: "run-1"}}}, "agent-1"); ok {
		t.Fatalf("unexpected comment=%+v", comment)
	}
}

func TestReviseCommentInstructionTargetsExactVersionAndForbidsAppend(t *testing.T) {
	prompt := reviseCommentInstruction(domain.IssueComment{
		Versioned: domain.Versioned{ID: "comment-42", Version: 9},
		Body:      "原评论",
	})
	for _, expected := range []string{
		"comment_id: comment-42",
		"expected_version: 9",
		"issue_comment_update_own",
		"不要调用 issue_comment_create",
		"原评论",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q: %s", expected, prompt)
		}
	}
}
