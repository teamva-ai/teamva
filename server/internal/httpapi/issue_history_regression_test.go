package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func issueHistoryFixture(t *testing.T) (*testRepository, domain.User, domain.Issue, domain.IssueAgentInstance, domain.Run) {
	t.Helper()
	repo := newTestRepository(t)
	user, computer, err := repo.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := repo.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{ComputerID: computer.ID, AgentServiceID: service.ID, Name: "history", Status: "active"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "history"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := repo.CreateIssue(t.Context(), user.ID, domain.Issue{ProjectID: project.ID, Title: "history"})
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := repo.EnsureIssueAgentInstance(t.Context(), user.ID, issue.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := repo.CreateRun(t.Context(), user.ID, issue.ID, issue.Version, "history", domain.Run{
		IssueAgentInstanceID: instance.ID, AgentID: agent.ID, Input: []domain.ContentBlock{{Type: "text", Text: "history"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return repo, user, issue, instance, run
}

type silentHistoryEvents struct{ wake chan struct{} }

func (s *silentHistoryEvents) NotifyIssueActivation(string) {}
func (s *silentHistoryEvents) SubscribeIssue(string) (<-chan struct{}, func()) {
	return s.wake, func() {}
}
func (s *silentHistoryEvents) SubscribeRunEvents(string) (<-chan struct{}, func()) {
	return s.wake, func() {}
}

func TestIssueSSEDrainsPersistedTailWithoutAnotherWake(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo, user, issue, _, run := issueHistoryFixture(t)
	for start := 0; start < 300; start += 100 {
		events := make([]nodeprotocol.RunEvent, 100)
		for offset := range events {
			sequence := start + offset + 1
			typ := "provider.tool"
			payload := map[string]any{"state": "completed"}
			if sequence == 300 {
				typ = "run.finished"
				payload = map[string]any{"status": "succeeded"}
			}
			events[offset] = nodeprotocol.RunEvent{ID: fmt.Sprintf("history-event-%03d", sequence), RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: int64(sequence), Type: typ, Payload: payload, OccurredAt: time.Now().UTC()}
		}
		if _, err := repo.ApplyLocalEvents(t.Context(), events); err != nil {
			t.Fatal(err)
		}
	}
	a := &API{store: repo, runtime: &silentHistoryEvents{wake: make(chan struct{})}}
	router := gin.New()
	router.GET("/:issue_id", func(c *gin.Context) { c.Set(userKey, user); a.streamIssueEvents(c) })
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/"+issue.ID, nil)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	frames := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			frames++
			if strings.Contains(line, `"event_type":"run.finished"`) {
				if frames != 301 {
					t.Fatalf("received %d frames before terminal, want 301", frames)
				}
				return
			}
		}
	}
	t.Fatalf("tail was stranded after %d frames: %v", frames, scanner.Err())
}

func TestIssuePromptUsesBoundedRecentHistoryWithoutTruncatingCurrentInstruction(t *testing.T) {
	repo, user, issue, instance, _ := issueHistoryFixture(t)
	for index := 0; index < 105; index++ {
		body := strings.Repeat("旧", 5000)
		if index == 104 {
			body = "latest-comment-marker"
		}
		if _, err := repo.CreateIssueComment(t.Context(), user.ID, domain.IssueComment{
			IssueID: issue.ID, AuthorKind: "user", Body: body,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	api := &API{store: repo}
	prompt, err := api.buildIssuePrompt(t.Context(), user.ID, issue.ID, instance, "current-instruction-marker", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt) > 64<<10 || !utf8.ValidString(prompt) || !strings.Contains(prompt, "latest-comment-marker") ||
		!strings.Contains(prompt, "current-instruction-marker") || !strings.Contains(prompt, "裁剪") {
		t.Fatalf("prompt budget or content invalid: bytes=%d", len(prompt))
	}
	if _, err := api.buildIssuePrompt(t.Context(), user.ID, issue.ID, instance, strings.Repeat("x", (64<<10)+1), false); err == nil {
		t.Fatal("oversized current instruction was silently truncated")
	}
}

func TestIssueSSEIncludesEventsFromRunsOutsideNewest200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo, user, issue, _, oldRun := issueHistoryFixture(t)
	for index := 0; index < 205; index++ {
		id := fmt.Sprintf("newer-history-run-%03d", index)
		stamp := time.Now().UTC().Add(time.Duration(index+1) * time.Second).Format(time.RFC3339Nano)
		if _, err := repo.db.ExecContext(t.Context(), `INSERT INTO runs
			(id,issue_id,project_id,status,input,idempotency_key,created_at,updated_at)
			VALUES(?,?,?,'succeeded','[]',?,?,?)`, id, issue.ID, issue.ProjectID, id, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	a := &API{store: repo, runtime: &silentHistoryEvents{wake: make(chan struct{})}}
	router := gin.New()
	router.GET("/:issue_id", func(c *gin.Context) { c.Set(userKey, user); a.streamIssueEvents(c) })
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/"+issue.ID, nil)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") && strings.Contains(line, oldRun.ID) {
			return
		}
	}
	t.Fatalf("event from older active Run was omitted: %v", scanner.Err())
}

func TestReviseSummarySurvivesMoreThan100SubsequentRuns(t *testing.T) {
	repo, user, issue, instance, original := issueHistoryFixture(t)
	comment, err := repo.CreateIssueComment(t.Context(), user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: "agent", AuthorAgentID: instance.AgentID,
		RunID: original.ID, Body: "original-summary-must-remain-revisable",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 105; index++ {
		id := fmt.Sprintf("subsequent-run-%03d", index)
		stamp := time.Now().UTC().Add(time.Duration(index+1) * time.Second).Format(time.RFC3339Nano)
		if _, err := repo.db.ExecContext(t.Context(), `INSERT INTO runs
			(id,issue_id,issue_agent_instance_id,agent_id,project_id,status,input,idempotency_key,created_at,updated_at)
			VALUES(?,?,?,?,?,'succeeded','[]',?,?,?)`, id, issue.ID, instance.ID, instance.AgentID, issue.ProjectID, id, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	a := &API{store: repo}
	prompt, err := a.buildReviseCommentPrompt(t.Context(), user.ID, issue.ID, instance)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, comment.ID) || !strings.Contains(prompt, comment.Body) {
		t.Fatal("original summary lost after recent run window advanced")
	}
}
