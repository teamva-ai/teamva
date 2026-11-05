package store

import (
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestFinalAssistantTextPrefersFinalAnswerAfterSteer(t *testing.T) {
	events := []domain.RunEvent{
		{Type: "provider.item", Payload: map[string]any{"item": map[string]any{"type": "agentMessage", "phase": "commentary", "text": ""}}},
		{Type: "output.delta", Payload: map[string]any{"text": "先检查"}},
		{Type: "provider.item", Payload: map[string]any{"item": map[string]any{"type": "userMessage", "text": "立即停止"}}},
		{Type: "provider.item", Payload: map[string]any{"item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": ""}}},
		{Type: "output.delta", Payload: map[string]any{"text": "干预指令"}},
		{Type: "output.delta", Payload: map[string]any{"text": "已收到"}},
		{Type: "provider.item", Payload: map[string]any{"item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": "干预指令已收到"}}},
	}
	for index := range events {
		events[index].Payload["provider_id"] = "codex"
	}
	if got := FinalAssistantText(events); got != "干预指令已收到" {
		t.Fatalf("FinalAssistantText() = %q", got)
	}
}

func TestOpenCodeConversationFinalReplyExcludesReasoningAndUserText(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, graph.Nodes[0].ID, "stream-final", "问题")
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.repository.GetRun(t.Context(), f.user.ID, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	part := func(id, kind, messageID, text string) nodeprotocol.RunEvent {
		return nodeprotocol.RunEvent{Type: "provider.event", Payload: map[string]any{
			"provider_id": "opencode",
			"part":        map[string]any{"id": id, "type": kind, "messageID": messageID, "text": text},
		}}
	}
	applySQLiteGoalEvents(t, f.repository, run,
		nodeprotocol.RunEvent{Type: "provider.event", Payload: map[string]any{"provider_id": "opencode", "info": map[string]any{"id": "user", "role": "user"}}},
		part("input", "text", "user", "用户提示词不可当作答案"),
		part("thought", "reasoning", "assistant", ""),
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"provider_id": "opencode", "part_id": "thought", "text": "先分析问题"}},
		part("thought", "reasoning", "assistant", "先分析问题"),
		part("reply", "text", "assistant", ""),
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"provider_id": "opencode", "part_id": "reply", "text": "这是"}},
		nodeprotocol.RunEvent{Type: "output.delta", Payload: map[string]any{"provider_id": "opencode", "part_id": "reply", "text": "最终答案"}},
		part("reply", "text", "assistant", "这是最终答案"),
		nodeprotocol.RunEvent{Type: "run.completed", Payload: map[string]any{"status": "completed"}},
	)
	view, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 2 || view.Messages[1].Body != "这是最终答案" || view.Messages[1].Status != "published" {
		t.Fatalf("wrong final response: %+v", view.Messages)
	}
}

func TestFinalAssistantTextIsStreamingAndByteBounded(t *testing.T) {
	accumulator := NewFinalAssistantTextAccumulator(1025)
	accumulator.Add(domain.RunEvent{Type: "provider.item", Payload: map[string]any{
		"provider_id": "codex",
		"item":        map[string]any{"type": "agentMessage", "phase": "final_answer"},
	}})
	for index := 0; index < 100; index++ {
		accumulator.Add(domain.RunEvent{Type: "output.delta", Payload: map[string]any{
			"provider_id": "codex",
			"text":        strings.Repeat("界", 100),
		}})
	}
	text := accumulator.Text()
	if len(text) > 1025 || !utf8.ValidString(text) || text == "" {
		t.Fatalf("terminal text bytes=%d valid=%t", len(text), utf8.ValidString(text))
	}
}
