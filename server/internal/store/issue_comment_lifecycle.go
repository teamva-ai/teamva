package store

import (
	"strings"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

// IssueAsyncDelegationPrompt keeps manual, mentioned and scheduled Issue work
// consistent. It is model guidance, not a Run transition or a Goal verdict.
const IssueAsyncDelegationPrompt = `
本轮若向其他 Agent 委派，结构化 @ 是异步委派：使用 issue_comment_create 发表本轮结果与委派说明，确认本次工具提交成功后结束当前 Run；不等对方完成，不轮询等结果，不再调用 issue_comment_create 或 issue_comment_update_own 回写本轮已发布的委派评论。
若需要结果回来继续，委派正文须明确要求对方完成后在结果评论的 mentioned_agent_ids 中显式 @回本 Agent ID；正文 @名字不算激活，回报将激活新 Run。只按明确的回报要求 @回，不要仅因其他 Agent 的评论触发就自动 @回，结果接收者无需无意义回@；允许有实际需要的 A→B→A，仍禁止直接 @自己。
以上仅约束本轮委派后的行为，不限制正常上下文读取或用户显式要求的评论修正；会话总结复用及 Goal 的单条 working→final、独立审核完成门禁不变，结束本轮不代表 Goal 已完成。
`

// withWorkingReplyID makes the durable reply placeholder discoverable to the
// Agent without relying on UI-only state. The tool service still authorizes by
// run scope and replaces the placeholder automatically.
func withWorkingReplyID(input []domain.ContentBlock, commentID string) []domain.ContentBlock {
	result := append([]domain.ContentBlock(nil), input...)
	instruction := "\n\n## 当前工作回复\n回复评论 ID：" + commentID +
		"\n完成本轮工作（或发布异步委派说明）时调用 issue_comment_create；该工具会替换这条正在工作的回复，不要重复发送多条总结。"
	for index := len(result) - 1; index >= 0; index-- {
		if result[index].Type == "text" {
			result[index].Text = strings.TrimSpace(result[index].Text) + instruction
			return result
		}
	}
	return append(result, domain.ContentBlock{Type: "text", Text: strings.TrimSpace(instruction)})
}

// FinalAssistantText extracts the provider's last real assistant answer for
// graph replies and independent goal audits. It never publishes Issue comments.
func FinalAssistantText(events []domain.RunEvent) string {
	accumulator := NewFinalAssistantTextAccumulator(MaxFinalAssistantTextBytes)
	for _, event := range events {
		accumulator.Add(event)
	}
	return accumulator.Text()
}

// FinalAssistantTextAccumulator extracts terminal text without concatenating
// unbounded output.delta streams. At most limit bytes are retained for each
// candidate message, and UTF-8 is never cut in the middle of a rune.
type FinalAssistantTextAccumulator struct {
	limit            int
	preferred        string
	fallback         string
	current          strings.Builder
	currentPreferred bool
	currentActive    bool
	currentID        string
	projectOutput    func(string, map[string]any) provider.OutputUpdate
}

func NewFinalAssistantTextAccumulator(limit int) *FinalAssistantTextAccumulator {
	if limit < 1 || limit > MaxFinalAssistantTextBytes {
		limit = MaxFinalAssistantTextBytes
	}
	return &FinalAssistantTextAccumulator{limit: limit, projectOutput: catalog.NewOutputProjection(MaxTerminalEventScanCount)}
}

func (a *FinalAssistantTextAccumulator) Add(event domain.RunEvent) {
	output := a.projectOutput(event.Type, event.Payload)
	if output.Ignore {
		return
	}
	if output.Replace {
		a.commitCurrent()
		a.current.Reset()
		a.currentPreferred = output.Final
		a.currentActive = true
		a.currentID = output.ID
		a.append(output.Text)
	} else {
		if output.ID != "" && output.ID != a.currentID {
			a.commitCurrent()
			a.current.Reset()
			a.currentID = output.ID
			a.currentPreferred = false
		}
		text := output.Text
		if text == "" {
			return
		}
		if !a.currentActive {
			a.currentActive = true
		}
		a.append(text)
	}
}

func (a *FinalAssistantTextAccumulator) Text() string {
	a.commitCurrent()
	if text := strings.TrimSpace(a.preferred); text != "" {
		return text
	}
	return strings.TrimSpace(a.fallback)
}

func (a *FinalAssistantTextAccumulator) commitCurrent() {
	if !a.currentActive {
		return
	}
	text := a.current.String()
	if text != "" {
		if a.currentPreferred {
			a.preferred = text
		} else {
			a.fallback = text
		}
	}
	a.currentActive = false
}

func (a *FinalAssistantTextAccumulator) append(value string) {
	remaining := a.limit - a.current.Len()
	if remaining <= 0 || value == "" {
		return
	}
	if len(value) > remaining {
		value = value[:remaining]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	a.current.WriteString(value)
}
