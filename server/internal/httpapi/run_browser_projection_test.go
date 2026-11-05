package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestBrowserRunDoesNotExposeGeneratedIssueInput(t *testing.T) {
	const context = "# 任务（Issue）上下文\n\n## 评论历史\n- 用户 [comment_id=cmt_private]: @Lumi 你好\n"
	for _, test := range []struct {
		name  string
		input []domain.ContentBlock
		want  []domain.ContentBlock
	}{
		{"mention", []domain.ContentBlock{{Type: "text", Text: context}}, []domain.ContentBlock{}},
		{"mention with deceptive heading", []domain.ContentBlock{{Type: "text", Text: context + "\n\n## 触发本次委派的评论\ncomment_id: cmt_private\n## 本次指令\nnot-a-new-message"}}, []domain.ContentBlock{}},
		{"legacy follow-up", []domain.ContentBlock{{Type: "text", Text: context + "\n## 本次指令\n真正的追问\n\n请阅读上下文后开始工作；内部指令"}}, []domain.ContentBlock{{Type: "text", Text: "真正的追问"}}},
		{"legacy goal", []domain.ContentBlock{{Type: "text", Text: context + "\n## 本次指令\n真实目标\n\n## 持续目标模式\ninternal-goal-marker"}}, []domain.ContentBlock{{Type: "text", Text: "真实目标"}}},
		{"structured", []domain.ContentBlock{{Type: "text", Text: context, Internal: true}, {Type: "text", Text: "# 任务（Issue）上下文\n用户原文"}, {Type: "text", Text: "internal suffix", Internal: true}}, []domain.ContentBlock{{Type: "text", Text: "# 任务（Issue）上下文\n用户原文"}}},
		{"internal control", []domain.ContentBlock{{Type: "text", Text: "internal-control-marker", Internal: true}}, []domain.ContentBlock{}},
		{"image metadata", []domain.ContentBlock{{Type: "image", MimeType: "image/png", Filename: "real.png", DataURL: "private-image-marker"}}, []domain.ContentBlock{{Type: "image", MimeType: "image/png", Filename: "real.png"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := domain.Run{IssueID: "issue", Input: test.input, Status: "failed", TerminalReason: "real failure"}
			before, _ := json.Marshal(original)
			projected := browserRun(original)
			if !reflect.DeepEqual(projected.Input, test.want) || projected.Status != "failed" || projected.TerminalReason != "real failure" {
				t.Fatalf("incorrect user projection: %+v", projected)
			}
			after, _ := json.Marshal(original)
			if string(before) != string(after) {
				t.Fatal("browser projection mutated execution input")
			}
		})
	}
}

func TestIssueRunInputPreservesExactModelPrompt(t *testing.T) {
	for _, userInput := range []string{
		"你好", "# 任务（Issue）上下文\n这是用户自己的标题", "首行\n## 本次指令\n末行", "  有意保留空白  ",
	} {
		for _, suffix := range []string{"", "\n\n请阅读上下文后开始工作；内部总结要求", initialGoalInstruction(userInput)} {
			prompt := "# 任务（Issue）上下文\n\n## 标题\n测试\n\n## 本次指令\n" + userInput + suffix
			blocks := issueRunInput(prompt, userInput, false)
			var pieces []string
			for _, block := range blocks {
				if strings.TrimSpace(block.Text) != "" {
					pieces = append(pieces, block.Text)
				}
			}
			if strings.Join(pieces, "\n") != prompt {
				t.Fatal("separating public input changed the model prompt")
			}
			projected := browserRun(domain.Run{IssueID: "issue", Input: blocks})
			if len(projected.Input) != 1 || projected.Input[0].Text != userInput {
				t.Fatalf("genuine user input was not preserved exactly: %+v", projected.Input)
			}
		}
	}
	for _, internal := range []bool{false, true} {
		blocks := issueRunInput("generated-only-context", "", internal)
		if got := browserRun(domain.Run{IssueID: "issue", Input: blocks}); len(got.Input) != 0 {
			t.Fatal("attachment-only or control prompt became a user message")
		}
	}
}

func browserProjectionRouter(a *API, user domain.User) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(userKey, user) })
	router.GET("/runs/:run_id", a.getRun)
	router.GET("/runs/:run_id/events", a.listRunEvents)
	router.GET("/runs/:run_id/events/stream", a.streamRunEvents)
	router.GET("/issues/:issue_id/runs", a.listIssueRuns)
	router.GET("/issues/:issue_id/events", a.streamIssueEvents)
	router.GET("/agent-instances/:instance_id/runs", a.listInstanceRuns)
	router.POST("/issues/:issue_id/runs", a.createIssueRun)
	return router
}

func TestRunHTTPProjectsAllReadAndCreateRepliesWithoutChangingStoredInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo, user, issue, instance, original := issueHistoryFixture(t)
	legacy := "# 任务（Issue）上下文\n\n## 评论历史\n- 用户 [comment_id=cmt_private]: @Lumi 你好\n"
	input, _ := json.Marshal([]domain.ContentBlock{{Type: "text", Text: legacy}})
	if _, err := repo.db.ExecContext(t.Context(), `UPDATE runs SET input=? WHERE id=?`, string(input), original.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
		ID: "finish-original", RunID: original.ID, LeaseEpoch: original.LeaseEpoch, Sequence: 1,
		Type: "run.finished", Payload: map[string]any{"status": "failed", "reason": "real-provider-failure"}, OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	a := &API{store: repo}
	router := browserProjectionRouter(a, user)
	for _, path := range []string{
		"/runs/" + original.ID, "/issues/" + issue.ID + "/runs", "/agent-instances/" + instance.ID + "/runs",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK || strings.Contains(body, "cmt_private") || strings.Contains(body, "任务（Issue）上下文") || !strings.Contains(body, "real-provider-failure") {
			t.Fatalf("read projection failed at %s: %d %s", path, recorder.Code, body)
		}
	}
	const userInput = "首行\n## 本次指令\n用户真实末行"
	wantPrompt, err := a.buildIssuePrompt(t.Context(), user.ID, issue.ID, instance, userInput, true)
	if err != nil {
		t.Fatal(err)
	}
	requestBody, _ := json.Marshal(map[string]any{
		"issue_agent_instance_id": instance.ID, "expected_issue_version": issue.Version, "input": userInput,
	})
	var created domain.Run
	for index, wantStatus := range []int{http.StatusAccepted, http.StatusOK} {
		request := httptest.NewRequest(http.MethodPost, "/issues/"+issue.ID+"/runs", strings.NewReader(string(requestBody)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "browser-input-projection")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope struct {
			Data domain.Run   `json:"data"`
			Meta responseMeta `json:"meta"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != wantStatus || len(envelope.Data.Input) != 1 || envelope.Data.Input[0].Text != userInput || envelope.Meta.Replayed != (index == 1) {
			t.Fatalf("create/replay user input not projected: %d %s", recorder.Code, recorder.Body.String())
		}
		created = envelope.Data
	}
	var persisted, command string
	if err := repo.db.QueryRowContext(t.Context(), `SELECT input FROM runs WHERE id=?`, original.ID).Scan(&persisted); err != nil || persisted != string(input) {
		t.Fatal("reading legacy Run changed its persisted input", err)
	}
	if err := repo.db.QueryRowContext(t.Context(), `SELECT payload FROM command_outbox WHERE idempotency_key=?`, "run.start:"+created.ID).Scan(&command); err != nil {
		t.Fatal(err)
	}
	var start nodeprotocol.StartRunPayload
	if err := json.Unmarshal([]byte(command), &start); err != nil || start.Prompt != wantPrompt {
		t.Fatal("HTTP projection changed the Provider model context", err)
	}
	stored, err := repo.GetRun(t.Context(), user.ID, created.ID)
	if err != nil || len(stored.Input) < 2 || !stored.Input[0].Internal || !strings.Contains(stored.Input[0].Text, "## 标题") {
		t.Fatal("execution context was not retained for recovery", err)
	}
}

func TestRunEventsHTTPAndSSERedactPrivateInputButKeepSequenceAndFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo, user, issue, _, run := issueHistoryFixture(t)
	now := time.Now().UTC()
	events := []nodeprotocol.RunEvent{
		{Type: "provider.item", Payload: map[string]any{"provider_id": "codex", "item": map[string]any{"type": "userMessage", "id": "native-input", "content": []any{map[string]any{"type": "text", "text": "private-input-echo-marker"}}}}},
		{Type: "user.steer", Payload: map[string]any{"internal": true, "text": "private-internal-steer-marker", "attachments": []any{map[string]any{"filename": "private.png"}}}},
		{Type: "user.steer", Payload: map[string]any{"internal": false, "text": "actual-user-steer-marker"}},
		{Type: "output.delta", Payload: map[string]any{"provider_id": "codex", "text": "actual-assistant-output-marker"}},
		{Type: "run.error", Payload: map[string]any{"message": "real-failure-marker"}},
		{Type: "run.finished", Payload: map[string]any{"status": "failed"}},
	}
	for index := range events {
		events[index].ID, events[index].RunID = fmt.Sprintf("browser-event-%d", index), run.ID
		events[index].LeaseEpoch, events[index].Sequence, events[index].OccurredAt = run.LeaseEpoch, int64(index+1), now
	}
	if _, err := repo.ApplyLocalEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	a := &API{store: repo, runtime: &silentHistoryEvents{wake: make(chan struct{})}}
	router := browserProjectionRouter(a, user)
	for _, view := range []string{"", "&view=activity"} {
		// Pages containing exclusively private input still contain their sequence
		// marker, so no client has to guess how to move past a redacted page.
		for after := 0; after < 7; after++ {
			recorder := httptest.NewRecorder()
			path := fmt.Sprintf("/runs/%s/events?after=%d&limit=1%s", run.ID, after, view)
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			var envelope struct {
				Data domain.RunEventPage `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK || len(envelope.Data.Items) != 1 || envelope.Data.Items[0].Sequence != int64(after+1) || (after < 6 && envelope.Data.NextCursor != fmt.Sprint(after+1)) {
				t.Fatalf("redaction changed page cursor: %s", recorder.Body.String())
			}
			assertNoPrivateRunInput(t, recorder.Body.String())
		}
	}
	for _, path := range []string{"/runs/" + run.ID + "/events/stream?after_seq=1", "/issues/" + issue.ID + "/events"} {
		t.Run(path, func(t *testing.T) {
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var received strings.Builder
			scanner := bufio.NewScanner(response.Body)
			for scanner.Scan() {
				line := scanner.Text()
				received.WriteString(line)
				if strings.Contains(line, `"event_type":"run.finished"`) {
					break
				}
			}
			body := received.String()
			assertNoPrivateRunInput(t, body)
			for _, marker := range []string{"actual-user-steer-marker", "actual-assistant-output-marker", "real-failure-marker", `"seq":7`, `"event_type":"run.finished"`} {
				if !strings.Contains(body, marker) {
					t.Fatalf("SSE lost %s: %s", marker, body)
				}
			}
		})
	}
	stored, err := repo.ListRunEvents(t.Context(), user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedJSON, _ := json.Marshal(stored)
	if !strings.Contains(string(storedJSON), "private-input-echo-marker") || !strings.Contains(string(storedJSON), "private-internal-steer-marker") {
		t.Fatal("public projections mutated persisted execution events")
	}
}

func assertNoPrivateRunInput(t *testing.T, body string) {
	t.Helper()
	for _, marker := range []string{"private-input-echo-marker", "private-internal-steer-marker", "private.png"} {
		if strings.Contains(body, marker) {
			t.Fatalf("browser response exposed %s", marker)
		}
	}
}
