package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
)

type workPlanHTTPRuntime struct {
	*runtimeSurfaceNotifier
	calls    atomic.Int64
	generate func(context.Context, nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error)
}

func (r *workPlanHTTPRuntime) GenerateWorkPlan(ctx context.Context, request nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error) {
	r.calls.Add(1)
	return r.generate(ctx, request)
}

func workPlanHTTPItems(items []nodeprotocol.WorkPlanItem) []nodeprotocol.WorkPlanItem {
	result := append([]nodeprotocol.WorkPlanItem(nil), items...)
	for i := range result {
		if result[i].After == nil {
			result[i].After = []string{}
		}
	}
	return result
}

func workPlanHTTPOrganizer(t *testing.T, f *runtimeSurfaceFixture) domain.Agent {
	t.Helper()
	service, err := f.repo.UpsertAgentService(t.Context(), f.user.ID, domain.AgentService{
		ComputerID: f.computer.ID, Provider: "opencode", Status: "ready", LoginStatus: "authenticated",
		Capabilities: map[string]any{"no_tools": true}, CapabilityVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.repo.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: service.ID, Name: "explicit organizer",
		Model: "saved-organizer-model", Status: domain.AgentActive,
	}, "do not inject this style into the planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func workPlanHTTPSetRuntime(t *testing.T, f *runtimeSurfaceFixture, runtime Runtime) {
	t.Helper()
	f.server.Close()
	f.server = httptest.NewServer(newRouterWithOrigins(f.repo, runtime, "work-plan", true, []string{testBrowserOrigin}))
	f.client = f.server.Client()
	t.Cleanup(f.server.Close)
}

func TestIssueCommentSingleMentionRemainsDirectAndMultiRequiresWorkItems(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	other := f.createAgent("B")
	path := "/v1/issues/" + f.issue.ID + "/comments"
	f.request(http.MethodPost, path, map[string]any{"body": "@A work", "mentioned_agent_ids": []string{f.agent.ID}}, "", http.StatusCreated)
	f.request(http.MethodPost, path, map[string]any{"body": "@A @B", "mentioned_agent_ids": []string{f.agent.ID, other.ID}}, "", http.StatusBadRequest)
	f.request(http.MethodPost, path, map[string]any{
		"body": "two activation paths", "mentioned_agent_ids": []string{f.agent.ID},
		"work_items": workPlanHTTPItems([]nodeprotocol.WorkPlanItem{{Key: "a", AgentID: f.agent.ID, Title: "work"}}),
	}, "mixed", http.StatusBadRequest)
	comments, err := f.repo.ListIssueComments(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(comments) != 1 || f.runtime.notifications.Load() != 1 {
		t.Fatalf("single mention changed or rejected requests mutated state: comments=%d notifications=%d err=%v", len(comments), f.runtime.notifications.Load(), err)
	}
}

func TestIssueWorkCommentHTTPAtomicReplayAndImmutableDefinition(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	b, c := f.createAgent("B"), f.createAgent("C")
	items := []nodeprotocol.WorkPlanItem{
		{Key: "a1", AgentID: f.agent.ID, Title: "A initial"},
		{Key: "b1", AgentID: b.ID, Title: "B first"},
		{Key: "b2", AgentID: b.ID, Title: "B second"},
		{Key: "c1", AgentID: c.ID, Title: "C first"},
		{Key: "c2", AgentID: c.ID, Title: "C second"},
		{Key: "c3", AgentID: c.ID, Title: "C third"},
		{Key: "a2", AgentID: f.agent.ID, Title: "A checks B and C", After: []string{"b1", "b2", "c1", "c2", "c3"}},
	}
	path := "/v1/issues/" + f.issue.ID + "/comments"
	body := map[string]any{"work_items": workPlanHTTPItems(items), "mentioned_agent_ids": []string{}}
	f.request(http.MethodPost, path, body, "", http.StatusBadRequest)
	first := runtimeSurfaceData[domain.IssueComment](t, f.request(http.MethodPost, path, body, "confirmed-seven", http.StatusCreated))
	replay := runtimeSurfaceData[domain.IssueComment](t, f.request(http.MethodPost, path, body, "confirmed-seven", http.StatusCreated))
	if first.ID != replay.ID || len(first.WorkItems) != 7 || first.Body == "" {
		t.Fatalf("work batch missing or replay duplicated: first=%+v replay=%+v", first, replay)
	}
	comments := runtimeSurfaceData[domain.Page[domain.IssueComment]](t, f.request(http.MethodGet, path, nil, "", http.StatusOK))
	mentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(comments.Items) != 1 || len(mentions) != 6 {
		t.Fatalf("batch not atomic or wrong dependency release: comments=%d mentions=%d err=%v", len(comments.Items), len(mentions), err)
	}
	changed := append([]nodeprotocol.WorkPlanItem(nil), items...)
	changed[0].Title = "different work"
	f.request(http.MethodPost, path, map[string]any{"work_items": workPlanHTTPItems(changed)}, "confirmed-seven", http.StatusConflict)
	updated := runtimeSurfaceData[domain.IssueComment](t, f.request(http.MethodPatch, "/v1/comments/"+first.ID,
		map[string]any{"body": "edited explanatory text", "expected_version": first.Version}, "", http.StatusOK))
	if len(updated.WorkItems) != 7 || updated.WorkItems[0].Title != "A initial" {
		t.Fatal("editing public text changed the confirmed work definition")
	}
}

func TestIssueWorkPlanHTTPUsesSavedModelAndHasNoBusinessSideEffects(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	organizer := workPlanHTTPOrganizer(t, f)
	runtime := &workPlanHTTPRuntime{runtimeSurfaceNotifier: f.runtime}
	runtime.generate = func(_ context.Context, request nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error) {
		if request.Provider != "opencode" || request.Model != "saved-organizer-model" || request.Input != "ask B to review" ||
			!reflect.DeepEqual(request.Agents, []nodeprotocol.WorkPlanAgent{{ID: f.agent.ID, Name: f.agent.Name}}) {
			t.Errorf("untrusted or wrong generation settings: %+v", request)
		}
		return nodeprotocol.WorkPlanResult{WorkItems: workPlanHTTPItems([]nodeprotocol.WorkPlanItem{{Key: "review", AgentID: f.agent.ID, Title: "review"}})}, nil
	}
	workPlanHTTPSetRuntime(t, f, runtime)
	path := "/v1/issues/" + f.issue.ID + "/work-plan"
	body := map[string]any{"agent_id": organizer.ID, "input": "ask B to review", "agent_ids": []string{f.agent.ID}}
	response := f.request(http.MethodPost, path, body, "", http.StatusOK)
	if response.Header.Get("Cache-Control") != "no-store" || runtime.calls.Load() != 1 {
		t.Fatal("planner not called exactly once or cache enabled")
	}
	comments, err := f.repo.ListIssueComments(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(comments) != 0 {
		t.Fatal("preview created public comments")
	}
	mentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 0 {
		t.Fatal("preview activated mentions")
	}
	runs, err := f.repo.ListRunsByIssue(t.Context(), f.user.ID, f.issue.ID, 10)
	if err != nil || len(runs) != 0 || runtime.notifications.Load() != 0 {
		t.Fatal("preview created or notified execution")
	}
	instances, err := f.repo.ListIssueAgentInstances(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(instances) != 1 || instances[0].ID != f.instance.ID {
		t.Fatal("preview created an independent instance")
	}
}

func TestIssueWorkPlanHTTPRejectsUnsafeOrUntrustedDrafts(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	organizer := workPlanHTTPOrganizer(t, f)
	runtime := &workPlanHTTPRuntime{runtimeSurfaceNotifier: f.runtime}
	runtime.generate = func(context.Context, nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error) {
		return nodeprotocol.WorkPlanResult{}, errors.New("private-provider-error-must-not-leak")
	}
	workPlanHTTPSetRuntime(t, f, runtime)
	path := "/v1/issues/" + f.issue.ID + "/work-plan"
	body := map[string]any{"agent_id": organizer.ID, "input": "review", "agent_ids": []string{f.agent.ID}}
	response := f.request(http.MethodPost, path, body, "", http.StatusServiceUnavailable)
	if strings.Contains(string(response.Raw), "private-provider-error") {
		t.Fatal("raw provider error leaked")
	}
	for _, invalid := range []map[string]any{
		{"agent_id": organizer.ID, "input": " ", "agent_ids": []string{f.agent.ID}},
		{"agent_id": organizer.ID, "input": strings.Repeat("x", 65537), "agent_ids": []string{f.agent.ID}},
		{"agent_id": organizer.ID, "input": strings.Repeat("x", 65536), "agent_ids": []string{f.agent.ID}},
		{"agent_id": organizer.ID, "input": strings.Repeat("<", 12000), "agent_ids": []string{f.agent.ID}},
		{"agent_id": organizer.ID, "input": "review", "agent_ids": []string{f.agent.ID, f.agent.ID}},
		{"agent_id": organizer.ID, "input": "review", "agent_ids": []string{f.agent.ID}, "model": "untrusted-model"},
		{"agent_id": organizer.ID, "input": "review", "agent_ids": []string{}},
	} {
		f.request(http.MethodPost, path, invalid, "", http.StatusBadRequest)
	}
	f.request(http.MethodPost, path, map[string]any{"agent_id": f.agent.ID, "input": "review", "agent_ids": []string{f.agent.ID}}, "", http.StatusUnprocessableEntity)
	if runtime.calls.Load() != 1 {
		t.Fatal("invalid or unsupported planner reached model generation")
	}
	runtime.generate = func(context.Context, nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error) {
		return nodeprotocol.WorkPlanResult{WorkItems: []nodeprotocol.WorkPlanItem{{Key: "outside", AgentID: organizer.ID, Title: "not allowed"}}}, nil
	}
	f.request(http.MethodPost, path, body, "", http.StatusServiceUnavailable)
	runtime.generate = func(context.Context, nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error) {
		return nodeprotocol.WorkPlanResult{WorkItems: []nodeprotocol.WorkPlanItem{{Key: "loop", AgentID: f.agent.ID, Title: "self wait", After: []string{"loop"}}}}, nil
	}
	f.request(http.MethodPost, path, body, "", http.StatusServiceUnavailable)
}

func TestIssueWorkPlanHTTPRequiresCSRFAndConfiguredOwnership(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	organizer := workPlanHTTPOrganizer(t, f)
	runtime := &workPlanHTTPRuntime{runtimeSurfaceNotifier: f.runtime}
	runtime.generate = func(context.Context, nodeprotocol.WorkPlanRequest) (nodeprotocol.WorkPlanResult, error) {
		t.Error("rejected request must not reach generation")
		return nodeprotocol.WorkPlanResult{}, nil
	}
	workPlanHTTPSetRuntime(t, f, runtime)
	path := "/v1/issues/" + f.issue.ID + "/work-plan"
	body := map[string]any{"agent_id": organizer.ID, "input": "review", "agent_ids": []string{f.agent.ID}}
	request, err := f.newRequest(t.Context(), http.MethodPost, path, body, "")
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Del("X-CSRF-Token")
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF accepted: %d", response.StatusCode)
	}
	f.request(http.MethodPost, path, map[string]any{"agent_id": "other-user-agent", "input": "review", "agent_ids": []string{f.agent.ID}}, "", http.StatusNotFound)
	f.request(http.MethodPost, path, map[string]any{"agent_id": organizer.ID, "input": "review", "agent_ids": []string{"other-user-agent"}}, "", http.StatusNotFound)
	f.request(http.MethodPost, "/v1/issues/other-user-issue/work-plan", body, "", http.StatusNotFound)
	_, err = f.repo.UpdateAgent(t.Context(), f.user.ID, organizer.ID, organizer.Version, func(agent *domain.Agent) {
		agent.Status = domain.AgentDisabled
	})
	if err != nil {
		t.Fatal(err)
	}
	f.request(http.MethodPost, path, body, "", http.StatusBadRequest)
	if runtime.calls.Load() != 0 || runtime.notifications.Load() != 0 {
		t.Fatal("authorization rejection triggered model or execution")
	}
}

func TestIssueWorkDependenciesTCPWaitForFiveResultsNotForInitialA(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	b, c := f.createAgent("B"), f.createAgent("C")
	items := []nodeprotocol.WorkPlanItem{
		{Key: "a1", AgentID: f.agent.ID, Title: "independent initial A"},
		{Key: "b1", AgentID: b.ID, Title: "B login"},
		{Key: "b2", AgentID: b.ID, Title: "B permissions"},
		{Key: "c1", AgentID: c.ID, Title: "C page"},
		{Key: "c2", AgentID: c.ID, Title: "C tests"},
		{Key: "c3", AgentID: c.ID, Title: "C errors"},
		{Key: "a2", AgentID: f.agent.ID, Title: "final A review", Instruction: "Check only the five upstream results", After: []string{"b1", "b2", "c1", "c2", "c3"}},
	}
	path := "/v1/issues/" + f.issue.ID + "/comments"
	batch := runtimeSurfaceData[domain.IssueComment](t, f.request(http.MethodPost, path, map[string]any{
		"body": "shared context", "work_items": workPlanHTTPItems(items),
	}, "dependency-tcp", http.StatusCreated))
	if len(batch.WorkItems) != 7 {
		t.Fatal("missing work definition")
	}
	runs := map[string]domain.Run{}
	for _, work := range batch.WorkItems {
		if work.Key == "a2" {
			if work.MentionID != "" || work.RunID != "" {
				t.Fatal("dependent A was activated before prerequisites")
			}
			continue
		}
		run, err := f.repo.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, work.AgentID, work.MentionID,
			[]domain.ContentBlock{{Type: "text", Text: "shared Issue history"}}, "tcp-work-"+work.Key)
		if err != nil {
			t.Fatal(err)
		}
		runs[work.Key] = run
		f.events(run, nodeprotocol.RunEvent{Sequence: 1, Type: "run.started", Payload: map[string]any{}})
	}
	if len(runs) != 6 || runs["b1"].IssueAgentInstanceID == runs["b2"].IssueAgentInstanceID {
		t.Fatal("same-Agent work did not create independent parallel instances")
	}
	tools := toolservice.New(f.repo)
	for _, key := range []string{"b1", "b2", "c1", "c2"} {
		run := runs[key]
		result := tools.Execute(t.Context(), f.computer.ID, nodeprotocol.NodeRequest{
			RunID: run.ID, Tool: "issue_comment_create", IdempotencyKey: "result-" + key,
			Payload: map[string]any{"body": "published-result-" + key},
		})
		if !result.Result.OK {
			t.Fatalf("publish %s: %+v", key, result.Result)
		}
		f.events(run, nodeprotocol.RunEvent{Sequence: 2, Type: "run.finished", Payload: map[string]any{"status": "completed"}})
	}
	if _, err := f.repo.AdvanceIssueWorkItems(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	mentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 6 {
		t.Fatal("partial results released final A")
	}
	last := runs["c3"]
	result := tools.Execute(t.Context(), f.computer.ID, nodeprotocol.NodeRequest{
		RunID: last.ID, Tool: "issue_comment_create", IdempotencyKey: "result-c3", Payload: map[string]any{"body": "published-result-c3"},
	})
	if !result.Result.OK {
		t.Fatalf("publish last result: %+v", result.Result)
	}
	f.events(last, nodeprotocol.RunEvent{Sequence: 2, Type: "run.finished", Payload: map[string]any{"status": "completed"}})
	for range 2 {
		if _, err := f.repo.AdvanceIssueWorkItems(t.Context(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	page := runtimeSurfaceData[domain.Page[domain.IssueComment]](t, f.request(http.MethodGet, path, nil, "", http.StatusOK))
	var final domain.IssueWorkItem
	for _, comment := range page.Items {
		if comment.ID == batch.ID {
			final = comment.WorkItems[6]
		}
	}
	if final.MentionID == "" || final.RunID != "" {
		t.Fatalf("final A not released once prerequisites complete: %+v", final)
	}
	a1 := runtimeSurfaceData[domain.Run](t, f.request(http.MethodGet, "/v1/runs/"+runs["a1"].ID, nil, "", http.StatusOK))
	if a1.Status != domain.RunRunning {
		t.Fatal("initial A must remain independent and still running")
	}
	finalRun, err := f.repo.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, f.agent.ID, final.MentionID,
		[]domain.ContentBlock{{Type: "text", Text: "shared Issue history"}}, "tcp-final-A")
	if err != nil {
		t.Fatal(err)
	}
	var prompt strings.Builder
	for _, block := range finalRun.Input {
		prompt.WriteString(block.Text)
	}
	for _, expected := range []string{"final A review", "Check only the five upstream results", "published-result-b1", "published-result-b2", "published-result-c1", "published-result-c2", "published-result-c3"} {
		if !strings.Contains(prompt.String(), expected) {
			t.Errorf("final work input omitted %q", expected)
		}
	}
	mentions, err = f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 7 {
		t.Fatalf("dependency wakeup duplicated or lost work: mentions=%d err=%v", len(mentions), err)
	}
}

func TestIssueWorkCommentHTTPInvalidBatchNeverPartiallyActivates(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	path := "/v1/issues/" + f.issue.ID + "/comments"
	item := nodeprotocol.WorkPlanItem{Key: "one", AgentID: f.agent.ID, Title: "valid work"}
	many := make([]nodeprotocol.WorkPlanItem, 33)
	for i := range many {
		many[i] = nodeprotocol.WorkPlanItem{Key: fmt.Sprintf("work%d", i), AgentID: f.agent.ID, Title: "work"}
	}
	cases := [][]nodeprotocol.WorkPlanItem{
		{item, item},
		{{Key: "one", AgentID: f.agent.ID, Title: "work", After: []string{"missing"}}},
		{{Key: "one", AgentID: f.agent.ID, Title: "work", After: []string{"one"}}},
		{{Key: "one", AgentID: f.agent.ID, Title: "work", After: []string{"two"}}, {Key: "two", AgentID: f.agent.ID, Title: "other work", After: []string{"one"}}},
		{{Key: "one", AgentID: f.agent.ID, Title: " "}},
		{{Key: "one", AgentID: f.agent.ID, Title: "work", Instruction: strings.Repeat("x", 16385)}},
		{item, {Key: "two", AgentID: "foreign-agent", Title: "not allowed"}},
		many,
	}
	for i, items := range cases {
		f.request(http.MethodPost, path, map[string]any{"body": "invalid batch", "work_items": workPlanHTTPItems(items)}, fmt.Sprintf("invalid-batch-%d", i), http.StatusBadRequest)
	}
	f.request(http.MethodPost, path, map[string]any{"body": "forged status", "work_items": []map[string]any{
		{"key": "one", "agent_id": f.agent.ID, "title": "work", "status": "succeeded"},
	}}, "forged-status", http.StatusBadRequest)
	comments, err := f.repo.ListIssueComments(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(comments) != 0 {
		t.Fatal("invalid batch left partial comments")
	}
	mentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 0 || f.runtime.notifications.Load() != 0 {
		t.Fatal("invalid batch partially activated work")
	}
}

func TestIssueWorkCommentHTTPConcurrentRetryCreatesOneBatch(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	path := "/v1/issues/" + f.issue.ID + "/comments"
	body := map[string]any{"work_items": workPlanHTTPItems([]nodeprotocol.WorkPlanItem{
		{Key: "first", AgentID: f.agent.ID, Title: "first independent work"},
		{Key: "second", AgentID: f.agent.ID, Title: "second independent work"},
	})}
	type result struct {
		response runtimeSurfaceResponse
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			response, err := f.do(http.MethodPost, path, body, "same-concurrent-batch")
			results <- result{response, err}
		}()
	}
	close(start)
	var commentID string
	for range 2 {
		r := <-results
		if r.err != nil || r.response.Status != http.StatusCreated {
			t.Fatalf("concurrent retry: status=%d err=%v response=%s", r.response.Status, r.err, r.response.Raw)
		}
		comment := runtimeSurfaceData[domain.IssueComment](t, r.response)
		if commentID != "" && commentID != comment.ID {
			t.Fatal("concurrent retry duplicated the batch")
		}
		commentID = comment.ID
	}
	comments, err := f.repo.ListIssueComments(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(comments) != 1 {
		t.Fatal("concurrent request duplicated public comment")
	}
	mentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 2 {
		t.Fatalf("concurrent request duplicated or lost work: mentions=%d err=%v", len(mentions), err)
	}
}
