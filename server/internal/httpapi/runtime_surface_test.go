package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/toolservice"
)

func TestRuntimeSurfaceIssueRunsTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	base := "/v1/issues/" + f.issue.ID + "/runs"
	f.request(http.MethodPost, base, f.issueRunBody("surface question"), "", http.StatusBadRequest)
	run := f.createRun("first")
	if run.IssueAgentInstanceID != f.instance.ID || run.ReadOnly || run.Status != domain.RunQueued {
		t.Fatalf("unexpected admitted run: %+v", run)
	}
	replay := f.request(http.MethodPost, base, f.issueRunBody("surface question"), "first", http.StatusOK)
	if !replay.Meta.Replayed || runtimeSurfaceData[domain.Run](t, replay).ID != run.ID {
		t.Fatal("same request did not replay original run")
	}
	f.request(http.MethodPost, base, f.issueRunBody("different"), "busy-new-key", http.StatusConflict)
	f.finish(run, 1)
	second := f.createRun("second")
	f.finish(second, 1)
	for _, path := range []string{base, "/v1/agent-instances/" + f.instance.ID + "/runs"} {
		page := runtimeSurfaceData[domain.Page[domain.Run]](t, f.request(http.MethodGet, path+"?limit=1", nil, "", http.StatusOK))
		if len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor == "" {
			t.Fatalf("newest-first first page: %+v", page)
		}
		next := runtimeSurfaceData[domain.Page[domain.Run]](t, f.request(http.MethodGet, path+"?limit=1&cursor="+url.QueryEscape(page.NextCursor), nil, "", http.StatusOK))
		if len(next.Items) != 1 || next.Items[0].ID != run.ID || next.NextCursor != "" {
			t.Fatalf("second page duplicated or omitted run: %+v", next)
		}
		for _, query := range []string{"?limit=0", "?limit=201", "?cursor=bad-cursor"} {
			f.request(http.MethodGet, path+query, nil, "", http.StatusBadRequest)
		}
	}
	// A single instance accepts at most one concurrent new Run.
	start := make(chan struct{})
	results := make(chan runtimeSurfaceResponse, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := f.do(http.MethodPost, base, f.issueRunBody("concurrent"), fmt.Sprintf("concurrent-%d", i))
			if err != nil {
				results <- runtimeSurfaceResponse{Status: -1, Raw: []byte(err.Error())}
				return
			}
			results <- res
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	statuses := map[int]int{}
	for result := range results {
		statuses[result.Status]++
	}
	if statuses[http.StatusAccepted] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("concurrent admission statuses=%v", statuses)
	}
}

func TestRuntimeSurfaceRunEventsTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	run := f.createRun("events")
	f.events(run,
		nodeprotocol.RunEvent{Sequence: 1, Type: "run.started", Payload: map[string]any{}},
		nodeprotocol.RunEvent{Sequence: 2, Type: "output.delta", Payload: map[string]any{"text": "surface output"}},
		nodeprotocol.RunEvent{Sequence: 3, Type: "run.finished", Payload: map[string]any{"status": "completed"}},
	)
	base := "/v1/runs/" + run.ID
	read := runtimeSurfaceData[domain.Run](t, f.request(http.MethodGet, base, nil, "", http.StatusOK))
	if read.ID != run.ID || read.Status != domain.RunSucceeded || read.FinishedAt == nil {
		t.Fatalf("event projection: %+v", read)
	}
	page := runtimeSurfaceData[domain.RunEventPage](t, f.request(http.MethodGet, base+"/events?after=1&limit=1", nil, "", http.StatusOK))
	if len(page.Items) != 1 || page.Items[0].Sequence != 2 || page.Items[0].Type != "run.started" || !page.Truncated || page.NextCursor != "2" {
		t.Fatalf("event paging: %+v", page)
	}
	next := runtimeSurfaceData[domain.RunEventPage](t, f.request(http.MethodGet, base+"/events?after="+page.NextCursor+"&view=activity", nil, "", http.StatusOK))
	if len(next.Items) != 2 || next.Items[0].Type != "output.delta" || next.Items[1].Type != "run.finished" || next.Truncated {
		t.Fatalf("after cursor: %+v", next)
	}
	for _, query := range []string{"after=-1", "after=abc", "limit=0", "limit=501"} {
		f.request(http.MethodGet, base+"/events?"+query, nil, "", http.StatusBadRequest)
	}
	f.request(http.MethodGet, base+"/events/stream?after_seq=-1", nil, "", http.StatusBadRequest)
	req, err := f.newRequest(t.Context(), http.MethodGet, base+"/events/stream?after_seq=2", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("terminal SSE: status=%d err=%v", res.StatusCode, err)
	}
	if strings.Count(string(raw), "event:output.delta") != 1 || strings.Count(string(raw), "event:run.finished") != 1 || strings.Contains(string(raw), "event:run.started") {
		t.Fatalf("SSE replay/after: %s", raw)
	}
	// A reconnect after the last event of a terminal Run closes immediately;
	// it must not wait for a heartbeat or a wakeup that will never arrive.
	terminalContext, terminalCancel := context.WithTimeout(t.Context(), time.Second)
	defer terminalCancel()
	terminalRequest, err := f.newRequest(terminalContext, http.MethodGet, base+"/events/stream?after_seq=4", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	terminalResponse, err := f.client.Do(terminalRequest)
	if err != nil {
		t.Fatal(err)
	}
	emptyTail, tailErr := io.ReadAll(terminalResponse.Body)
	terminalResponse.Body.Close()
	if tailErr != nil || len(emptyTail) != 0 {
		t.Fatalf("terminal cursor did not close promptly: tail=%s err=%v", emptyTail, tailErr)
	}
	// Both streams must release subscriptions when the real HTTP client cancels.
	active := f.createRun("active-stream")
	for _, path := range []string{"/v1/runs/" + active.ID + "/events/stream?after_seq=1", "/v1/issues/" + f.issue.ID + "/events"} {
		before := f.runtime.unsubscribed.Load()
		ctx, cancel := context.WithCancel(t.Context())
		req, err := f.newRequest(ctx, http.MethodGet, path, nil, "")
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		response, err := f.client.Do(req)
		if err != nil || response.StatusCode != http.StatusOK {
			cancel()
			t.Fatalf("open stream: response=%v err=%v", response, err)
		}
		if strings.HasSuffix(path, "/events") {
			scanner := bufio.NewScanner(response.Body)
			if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "event:") {
				cancel()
				response.Body.Close()
				t.Fatal("issue stream failed to replay committed events")
			}
		} else {
			f.events(active,
				nodeprotocol.RunEvent{Sequence: 1, Type: "run.started", Payload: map[string]any{}},
				nodeprotocol.RunEvent{Sequence: 2, Type: "output.delta", Payload: map[string]any{"text": "surface-live"}},
			)
			f.runtime.wake <- struct{}{}
			scanner := bufio.NewScanner(response.Body)
			seen := false
			for scanner.Scan() {
				if strings.Contains(scanner.Text(), "surface-live") {
					seen = true
					break
				}
			}
			if !seen {
				cancel()
				response.Body.Close()
				t.Fatal("runtime wake failed to drain newly committed events")
			}
		}
		cancel()
		response.Body.Close()
		deadline := time.Now().Add(2 * time.Second)
		for f.runtime.unsubscribed.Load() == before && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if f.runtime.unsubscribed.Load() != before+1 {
			t.Fatal("client cancellation leaked stream subscription")
		}
	}
}

func TestRuntimeSurfaceRunControlTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	run := f.createRun("controls")
	base := "/v1/runs/" + run.ID
	f.request(http.MethodPost, base+"/stop", map[string]any{"mode": "invalid"}, "", http.StatusBadRequest)
	f.request(http.MethodPost, base+"/steer", map[string]any{"input": "change"}, "", http.StatusBadRequest)
	f.request(http.MethodPost, base+"/steer", map[string]any{"input": "change"}, "queued", http.StatusConflict)
	f.events(run, nodeprotocol.RunEvent{Sequence: 1, Type: "run.started", Payload: map[string]any{}})
	steered := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base+"/steer", map[string]any{"input": "change"}, "steer", http.StatusAccepted))
	replay := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base+"/steer", map[string]any{"input": "change"}, "steer", http.StatusAccepted))
	if replay.Version != steered.Version {
		t.Fatal("steer replay duplicated an event/version")
	}
	f.request(http.MethodPost, base+"/steer", map[string]any{"input": " "}, "empty", http.StatusBadRequest)
	stopped := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base+"/stop", map[string]any{"mode": "graceful"}, "", http.StatusAccepted))
	if stopped.Status != domain.RunCancelling {
		t.Fatalf("stop did not fence Run: %+v", stopped)
	}
	again := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base+"/stop", map[string]any{"mode": "force"}, "", http.StatusAccepted))
	if again.Version != stopped.Version {
		t.Fatal("repeated stop duplicated cancellation")
	}
	for range 2 {
		repeated := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base+"/stop", map[string]any{"mode": "force"}, "", http.StatusAccepted))
		if repeated.Version != stopped.Version || repeated.Status != domain.RunCancelling {
			t.Fatal("force replay changed the accepted cancellation")
		}
	}
	events, err := f.repo.ListRunEvents(t.Context(), f.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancellations, terminals := 0, 0
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatal("stop escalation introduced a public sequence gap")
		}
		if event.Type == "run.cancelling" {
			cancellations++
		}
		if event.Type == "run.finished" || event.Type == "run.completed" || event.Type == "run.error" {
			terminals++
		}
	}
	if cancellations != 1 || terminals != 0 {
		t.Fatalf("stop escalation duplicated lifecycle events: cancelling=%d terminal=%d", cancellations, terminals)
	}
	rows, err := f.repo.db.QueryContext(t.Context(), `SELECT payload FROM command_outbox WHERE command_name='run.stop' AND json_extract(payload,'$.run_id')=?`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]int{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var command nodeprotocol.StopRunPayload
		if json.Unmarshal(raw, &command) != nil {
			t.Fatal("invalid stop command")
		}
		modes[command.Mode]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(modes) != 2 || modes["graceful"] != 1 || modes["force"] != 1 {
		t.Fatalf("force escalation was lost or duplicated: %+v", modes)
	}
	f.request(http.MethodPost, base+"/steer", map[string]any{"input": "late"}, "late", http.StatusConflict)
	f.finish(run, 2)
	terminal := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base+"/stop", map[string]any{"mode": "force"}, "", http.StatusAccepted))
	if terminal.Status != domain.RunCancelled {
		t.Fatalf("terminal stop changed state: %+v", terminal)
	}
	finalEvents, err := f.repo.ListRunEvents(t.Context(), f.user.ID, run.ID)
	if err != nil || len(finalEvents) != len(events)+1 || finalEvents[len(finalEvents)-1].Type != "run.finished" {
		t.Fatalf("terminal stop appended more than the native finish: %+v err=%v", finalEvents, err)
	}
}

func TestRuntimeSurfaceInstanceAndGoalTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	instanceBase := "/v1/issues/" + f.issue.ID + "/agent-instances/" + f.instance.ID
	instances := runtimeSurfaceData[domain.Page[domain.IssueAgentInstance]](t, f.request(http.MethodGet, "/v1/issues/"+f.issue.ID+"/agent-instances", nil, "", http.StatusOK))
	if len(instances.Items) != 1 || instances.Items[0].ID != f.instance.ID {
		t.Fatal("instance listing did not return fixture")
	}
	paused := runtimeSurfaceData[domain.IssueAgentInstance](t, f.request(http.MethodPost, instanceBase+"/pause", map[string]any{"expected_version": f.instance.Version}, "", http.StatusOK))
	if paused.Status != domain.AgentInstancePaused {
		t.Fatal("instance was not paused")
	}
	f.request(http.MethodPost, "/v1/issues/"+f.issue.ID+"/runs", f.issueRunBody("must not execute"), "paused", http.StatusConflict)
	f.request(http.MethodPost, instanceBase+"/resume", map[string]any{"expected_version": f.instance.Version}, "", http.StatusConflict)
	resumed := runtimeSurfaceData[domain.IssueAgentInstance](t, f.request(http.MethodPost, instanceBase+"/resume", map[string]any{"expected_version": paused.Version}, "", http.StatusOK))
	if resumed.Status != domain.AgentInstanceIdle {
		t.Fatal("instance was not resumed")
	}
	goalBase := "/v1/agent-instances/" + f.instance.ID + "/goal"
	empty := runtimeSurfaceData[struct {
		Goal *domain.IssueAgentGoal `json:"goal"`
	}](t, f.request(http.MethodGet, goalBase, nil, "", http.StatusOK))
	if empty.Goal != nil {
		t.Fatal("new instance has goal")
	}
	body := f.issueRunBody("persistent objective")
	body["goal"], body["goal_token_budget"] = true, 10000
	run := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, "/v1/issues/"+f.issue.ID+"/runs", body, "goal", http.StatusAccepted))
	view := runtimeSurfaceData[struct {
		Goal domain.IssueAgentGoal `json:"goal"`
	}](t, f.request(http.MethodGet, goalBase, nil, "", http.StatusOK))
	goal := view.Goal
	if run.GoalID != goal.ID || goal.Status != domain.GoalActive || goal.Objective != "persistent objective" {
		t.Fatalf("goal projection: %+v", goal)
	}
	path := "/v1/goals/" + goal.ID
	pausedGoal := runtimeSurfaceData[domain.IssueAgentGoal](t, f.request(http.MethodPost, path+"/pause", map[string]any{"expected_version": goal.Version}, "", http.StatusOK))
	f.request(http.MethodPost, path+"/resume", map[string]any{"expected_version": pausedGoal.Version}, "", http.StatusConflict)
	f.request(http.MethodPost, path+"/cancel", map[string]any{"expected_version": goal.Version}, "", http.StatusConflict)
	f.finish(run, 1)
	resumedGoal := runtimeSurfaceData[domain.IssueAgentGoal](t, f.request(http.MethodPost, path+"/resume", map[string]any{"expected_version": pausedGoal.Version}, "", http.StatusOK))
	if resumedGoal.Status != domain.GoalActive || resumedGoal.CurrentRunID != "" {
		t.Fatal("goal resume did not reset dispatch phase")
	}
	cancelled := runtimeSurfaceData[domain.IssueAgentGoal](t, f.request(http.MethodPost, path+"/cancel", map[string]any{"expected_version": resumedGoal.Version}, "", http.StatusOK))
	if cancelled.Status != domain.GoalCancelled {
		t.Fatal("goal was not cancelled")
	}
	f.request(http.MethodPost, path+"/resume", map[string]any{"expected_version": cancelled.Version}, "", http.StatusConflict)
}

func TestRuntimeSurfaceConversationTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	graph := f.graph()
	root := graph.Nodes[0]
	base := "/v1/conversation-nodes/" + root.ID
	graphs := runtimeSurfaceData[domain.Page[domain.ConversationGraph]](t, f.request(http.MethodGet, "/v1/projects/"+f.project.ID+"/conversation-graphs", nil, "", http.StatusOK))
	if len(graphs.Items) != 1 || graphs.Items[0].ID != graph.Graph.ID {
		t.Fatal("graph listing missed fixture")
	}
	root = runtimeSurfaceData[domain.ConversationNode](t, f.request(http.MethodGet, base, nil, "", http.StatusOK))
	for _, patch := range []map[string]any{{"width": 299}, {"height": 1601}} {
		patch["expected_version"] = root.Version
		f.request(http.MethodPatch, base, patch, "", http.StatusBadRequest)
	}
	root = runtimeSurfaceData[domain.ConversationNode](t, f.request(http.MethodPatch, base,
		map[string]any{"expected_version": root.Version, "title": "updated node", "width": 600, "height": 700, "position_x": -12, "position_y": 23}, "", http.StatusOK))
	if root.Title != "updated node" || root.Width != 600 || root.PositionX != -12 {
		t.Fatal("node patch did not persist geometry/title")
	}
	f.request(http.MethodPatch, base, map[string]any{"expected_version": root.Version - 1, "title": "stale"}, "", http.StatusConflict)
	f.request(http.MethodPost, base+"/messages", map[string]any{"body": "question"}, "", http.StatusBadRequest)
	message := runtimeSurfaceData[domain.ConversationMessage](t, f.request(http.MethodPost, base+"/messages", map[string]any{"body": "question"}, "message", http.StatusCreated))
	replayed := runtimeSurfaceData[domain.ConversationMessage](t, f.request(http.MethodPost, base+"/messages", map[string]any{"body": "question"}, "message", http.StatusCreated))
	if message.ID != replayed.ID || message.RunID != replayed.RunID {
		t.Fatal("conversation message replay duplicated Run")
	}
	f.request(http.MethodPost, base+"/messages", map[string]any{"body": "different"}, "message", http.StatusConflict)
	f.request(http.MethodPost, base+"/messages", map[string]any{"body": "busy"}, "busy", http.StatusConflict)
	f.request(http.MethodDelete, "/v1/conversation-graphs/"+graph.Graph.ID+"?expected_version=1", nil, "", http.StatusConflict)
	run, err := f.repo.GetRun(t.Context(), f.user.ID, message.RunID)
	if err != nil || !run.ReadOnly || run.ConversationNodeID != root.ID {
		t.Fatalf("graph Run not isolated: %+v %v", run, err)
	}
	command, ok, err := f.repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
	var startPayload nodeprotocol.StartRunPayload
	if err != nil || !ok || command.Name != "run.start" || json.Unmarshal(command.Payload, &startPayload) != nil ||
		!startPayload.ReadOnly || !startPayload.DisableTools || startPayload.ConversationNodeID != root.ID {
		t.Fatalf("graph outbound command lost no-tools/read-only boundary: name=%s err=%v", command.Name, err)
	}
	f.events(run, nodeprotocol.RunEvent{Sequence: 1, Type: "run.started", Payload: map[string]any{}},
		nodeprotocol.RunEvent{Sequence: 2, Type: "output.delta", Payload: map[string]any{"text": "answer"}},
		nodeprotocol.RunEvent{Sequence: 3, Type: "run.finished", Payload: map[string]any{"status": "completed"}})
	messages := runtimeSurfaceData[domain.Page[domain.ConversationMessage]](t, f.request(http.MethodGet, base+"/messages", nil, "", http.StatusOK))
	if len(messages.Items) != 2 || messages.Items[1].Body != "answer" || messages.Items[1].Status != domain.ConversationMessagePublished {
		t.Fatalf("message event projection: %+v", messages.Items)
	}
	branch := runtimeSurfaceData[domain.ConversationNode](t, f.request(http.MethodPost, base+"/fork", map[string]any{"message_id": messages.Items[1].ID, "title": "branch"}, "", http.StatusCreated))
	if branch.ParentID == "" || len(branch.InheritedMessages) != 2 {
		t.Fatal("fork lost immutable prefix")
	}
	f.request(http.MethodDelete, "/v1/conversation-nodes/"+branch.ID+"?expected_version="+fmt.Sprint(branch.Version), nil, "", http.StatusNoContent)
	f.request(http.MethodGet, "/v1/conversation-nodes/"+branch.ID, nil, "", http.StatusNotFound)
	detail := runtimeSurfaceData[domain.ConversationGraphDetail](t, f.request(http.MethodGet, "/v1/conversation-graphs/"+graph.Graph.ID, nil, "", http.StatusOK))
	f.request(http.MethodDelete, "/v1/conversation-graphs/"+graph.Graph.ID+"?expected_version="+fmt.Sprint(detail.Graph.Version), nil, "", http.StatusNoContent)
	for _, path := range []string{"/v1/conversation-graphs/" + graph.Graph.ID, base, base + "/messages", base + "/goal"} {
		f.request(http.MethodGet, path, nil, "", http.StatusNotFound)
	}
}

func TestRuntimeSurfaceConversationGoalsTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	graph := f.graph()
	base := "/v1/conversation-nodes/" + graph.Nodes[0].ID
	before := runtimeSurfaceData[struct {
		Goal *domain.ConversationGoal `json:"goal"`
	}](t, f.request(http.MethodGet, base+"/goal", nil, "", http.StatusOK))
	if before.Goal != nil {
		t.Fatal("empty node has goal")
	}
	body := map[string]any{"objective": "objective", "token_budget": 10000}
	f.request(http.MethodPost, base+"/goals", map[string]any{"objective": "objective", "token_budget": 1}, "bad", http.StatusBadRequest)
	goal := runtimeSurfaceData[domain.ConversationGoal](t, f.request(http.MethodPost, base+"/goals", body, "goal", http.StatusAccepted))
	replay := f.request(http.MethodPost, base+"/goals", body, "goal", http.StatusOK)
	if !replay.Meta.Replayed || runtimeSurfaceData[domain.ConversationGoal](t, replay).ID != goal.ID {
		t.Fatal("goal replay lost identity")
	}
	f.request(http.MethodPost, base+"/goals", map[string]any{"objective": "different", "token_budget": 10000}, "goal", http.StatusConflict)
	f.request(http.MethodPost, base+"/messages", map[string]any{"body": "reserved node"}, "reserved", http.StatusConflict)
	path := base + "/goals/" + goal.ID
	f.request(http.MethodPost, path+"/invalid", map[string]any{"expected_version": goal.Version}, "", http.StatusBadRequest)
	paused := runtimeSurfaceData[domain.ConversationGoal](t, f.request(http.MethodPost, path+"/pause", map[string]any{"expected_version": goal.Version}, "", http.StatusOK))
	f.request(http.MethodPost, path+"/resume", map[string]any{"expected_version": paused.Version}, "", http.StatusConflict)
	run, err := f.repo.GetRun(t.Context(), f.user.ID, goal.CurrentRunID)
	if err != nil {
		t.Fatal(err)
	}
	f.finish(run, 1)
	paused = runtimeSurfaceData[struct {
		Goal domain.ConversationGoal `json:"goal"`
	}](t, f.request(http.MethodGet, base+"/goal", nil, "", http.StatusOK)).Goal
	resumed := runtimeSurfaceData[domain.ConversationGoal](t, f.request(http.MethodPost, path+"/resume", map[string]any{"expected_version": paused.Version}, "", http.StatusOK))
	if resumed.Status != domain.GoalActive {
		t.Fatal("conversation goal did not resume")
	}
	cancelled := runtimeSurfaceData[domain.ConversationGoal](t, f.request(http.MethodPost, path+"/cancel", map[string]any{"expected_version": resumed.Version}, "", http.StatusOK))
	if cancelled.Status != domain.GoalCancelled {
		t.Fatal("conversation goal did not cancel")
	}
	f.request(http.MethodPost, path+"/resume", map[string]any{"expected_version": cancelled.Version}, "", http.StatusConflict)
}

func TestRuntimeSurfaceReadonlyAssistantTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	lumi := runtimeSurfaceData[domain.Agent](t, f.request(http.MethodGet, "/v1/work-assistant", nil, "", http.StatusOK))
	f.request(http.MethodPatch, "/v1/agents/"+lumi.ID, map[string]any{"expected_version": lumi.Version, "agent_service_id": f.service.ID, "model": "surface-test"}, "", http.StatusOK)
	f.request(http.MethodGet, "/v1/work-assistant/sessions?scope=invalid", nil, "", http.StatusBadRequest)
	f.request(http.MethodPost, "/v1/work-assistant/sessions", map[string]any{"scope": "invalid"}, "", http.StatusBadRequest)
	for _, scope := range []domain.AssistantScope{domain.AssistantWorkHistory, domain.AssistantInspirations} {
		session := runtimeSurfaceData[domain.AssistantSession](t, f.request(http.MethodPost, "/v1/work-assistant/sessions", map[string]any{"title": "surface " + string(scope), "scope": scope}, "", http.StatusCreated))
		if session.Scope != scope || session.AgentID != lumi.ID {
			t.Fatal("assistant session lost scope/identity")
		}
		page := runtimeSurfaceData[domain.Page[domain.AssistantSession]](t, f.request(http.MethodGet, "/v1/work-assistant/sessions?scope="+string(scope), nil, "", http.StatusOK))
		if len(page.Items) != 1 || page.Items[0].ID != session.ID {
			t.Fatal("session scope filtering failed")
		}
		base := "/v1/work-assistant/sessions/" + session.ID + "/messages"
		crossScope := map[string]any{"body": "wrong scope"}
		if scope == domain.AssistantWorkHistory {
			crossScope["inspiration_ids"] = []string{"fixture-inspiration"}
		} else {
			crossScope["selected_ids"] = []string{"fixture-work-record"}
		}
		f.request(http.MethodPost, base, crossScope, "cross-scope", http.StatusBadRequest)
		f.request(http.MethodPost, base, map[string]any{"body": "question"}, "", http.StatusBadRequest)
		run := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base, map[string]any{"body": "question"}, "assistant", http.StatusAccepted))
		if !run.ReadOnly || run.IssueID != "" || run.ProjectID != "" || run.AssistantSessionID != session.ID || run.Purpose != "work_assistant" {
			t.Fatalf("assistant escaped global read-only scope: %+v", run)
		}
		command, ok, err := f.repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
		var payload nodeprotocol.StartRunPayload
		if err != nil || !ok || command.Name != "run.start" || json.Unmarshal(command.Payload, &payload) != nil ||
			!payload.ReadOnly || payload.AssistantSessionID != session.ID || payload.IssueID != "" || payload.ProjectID != "" {
			t.Fatalf("assistant outbound command lost global/read-only scope: name=%s err=%v", command.Name, err)
		}
		replay := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base, map[string]any{"body": "question"}, "assistant", http.StatusAccepted))
		if replay.ID != run.ID {
			t.Fatal("assistant replay duplicated run")
		}
		f.request(http.MethodPost, base, map[string]any{"body": "different"}, "assistant", http.StatusConflict)
		f.request(http.MethodPost, base, map[string]any{"body": "busy"}, "new-key", http.StatusConflict)
		f.finish(run, 1)
		messages := runtimeSurfaceData[domain.Page[domain.AssistantMessage]](t, f.request(http.MethodGet, base, nil, "", http.StatusOK))
		if len(messages.Items) != 2 || messages.Items[0].Body != "question" {
			t.Fatalf("assistant messages: %+v", messages.Items)
		}
	}
}

func TestRuntimeSurfaceCommentsMentionsTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	base := "/v1/issues/" + f.issue.ID + "/comments"
	f.request(http.MethodPost, base, map[string]any{"body": " "}, "", http.StatusBadRequest)
	f.request(http.MethodPost, base, map[string]any{"body": "no ghost", "mentioned_agent_ids": []string{"missing-agent"}}, "", http.StatusBadRequest)
	f.request(http.MethodPost, base, map[string]any{"body": "duplicate", "mentioned_agent_ids": []string{f.agent.ID, f.agent.ID}}, "", http.StatusBadRequest)
	comment := runtimeSurfaceData[domain.IssueComment](t, f.request(http.MethodPost, base, map[string]any{"body": "original", "mentioned_agent_ids": []string{f.agent.ID}}, "", http.StatusCreated))
	mentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || len(mentions) != 1 || mentions[0].TargetAgentID != f.agent.ID {
		t.Fatalf("structured mention missing: %+v %v", mentions, err)
	}
	if f.runtime.notifications.Load() != 1 {
		t.Fatal("comment did not notify durable mention consumer exactly once")
	}
	updated := runtimeSurfaceData[domain.IssueComment](t, f.request(http.MethodPatch, "/v1/comments/"+comment.ID, map[string]any{"body": "edited", "expected_version": comment.Version}, "", http.StatusOK))
	f.request(http.MethodPatch, "/v1/comments/"+comment.ID, map[string]any{"body": "stale", "expected_version": comment.Version}, "", http.StatusConflict)
	revisions := runtimeSurfaceData[domain.Page[domain.IssueCommentRevision]](t, f.request(http.MethodGet, "/v1/comments/"+comment.ID+"/revisions", nil, "", http.StatusOK))
	if len(revisions.Items) != 1 || revisions.Items[0].Body != "original" {
		t.Fatalf("revision history lost original: %+v", revisions)
	}
	comments := runtimeSurfaceData[domain.Page[domain.IssueComment]](t, f.request(http.MethodGet, base, nil, "", http.StatusOK))
	if len(comments.Items) != 1 || comments.Items[0].Body != "edited" {
		t.Fatal("invalid mention created a ghost comment or edit was lost")
	}
	f.request(http.MethodDelete, "/v1/comments/"+comment.ID+"?expected_version="+fmt.Sprint(comment.Version), nil, "", http.StatusConflict)
	f.request(http.MethodDelete, "/v1/comments/"+comment.ID+"?expected_version="+fmt.Sprint(updated.Version), nil, "", http.StatusNoContent)
	f.request(http.MethodGet, "/v1/comments/"+comment.ID+"/revisions", nil, "", http.StatusNotFound)
	remaining := runtimeSurfaceData[domain.Page[domain.IssueComment]](t, f.request(http.MethodGet, base, nil, "", http.StatusOK))
	if len(remaining.Items) != 0 {
		t.Fatal("deleted comment still listed")
	}
}

func TestRuntimeSurfaceSteerConflictTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	run := f.createRun("steer-conflict")
	f.events(run, nodeprotocol.RunEvent{Sequence: 1, Type: "run.started", Payload: map[string]any{}})
	base := "/v1/runs/" + run.ID
	f.request(http.MethodPost, base+"/steer", map[string]any{"input": "original"}, "same-key", http.StatusAccepted)
	before := f.request(http.MethodGet, base+"/events", nil, "", http.StatusOK)
	f.request(http.MethodPost, base+"/steer", map[string]any{"input": "different"}, "same-key", http.StatusConflict)
	f.request(http.MethodPost, base+"/steer", map[string]any{"input": "original", "internal": true}, "same-key", http.StatusConflict)
	after := f.request(http.MethodGet, base+"/events", nil, "", http.StatusOK)
	if !reflect.DeepEqual(before.Data, after.Data) {
		t.Fatal("conflicting steer changed event history")
	}
}

func TestRuntimeSurfaceIssueRunRequestFingerprintTCP(t *testing.T) {
	for _, goal := range []bool{false, true} {
		t.Run(fmt.Sprint("goal=", goal), func(t *testing.T) {
			f := newRuntimeSurfaceFixture(t)
			base := "/v1/issues/" + f.issue.ID + "/runs"
			body := f.issueRunBody("original")
			body["goal"] = goal
			if goal {
				body["goal_token_budget"] = 10000
			}
			run := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, base, body, "fingerprint", http.StatusAccepted))
			for name, mutate := range map[string]func(map[string]any){
				"input":    func(b map[string]any) { b["input"] = "different" },
				"target":   func(b map[string]any) { b["issue_agent_instance_id"] = "other-instance" },
				"internal": func(b map[string]any) { b["internal"] = true },
				"goal":     func(b map[string]any) { b["goal"] = !goal },
			} {
				t.Run(name, func(t *testing.T) {
					changed := map[string]any{}
					for k, v := range body {
						changed[k] = v
					}
					mutate(changed)
					response := f.request(http.MethodPost, base, changed, "fingerprint", http.StatusConflict)
					if response.Error == nil || response.Error.Code != "idempotency_conflict" {
						t.Fatalf("wrong replay conflict: %s", response.Raw)
					}
				})
			}
			if goal {
				changed := f.issueRunBody("original")
				changed["goal"], changed["goal_token_budget"] = true, 20000
				f.request(http.MethodPost, base, changed, "fingerprint", http.StatusConflict)
			}
			body["expected_issue_version"] = 999
			replayed := f.request(http.MethodPost, base, body, "fingerprint", http.StatusOK)
			if !replayed.Meta.Replayed || runtimeSurfaceData[domain.Run](t, replayed).ID != run.ID {
				t.Fatal("stale optimistic version prevented exact request replay")
			}
			runs := runtimeSurfaceData[domain.Page[domain.Run]](t, f.request(http.MethodGet, base, nil, "", http.StatusOK))
			if len(runs.Items) != 1 {
				t.Fatal("conflicting retries created extra Run")
			}
		})
	}
}

func TestRuntimeSurfaceSteerAttachmentReplayAfterDeliveryTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	run := f.createRun("attachment")
	f.events(run, nodeprotocol.RunEvent{Sequence: 1, Type: "run.started", Payload: map[string]any{}})
	base := "/v1/runs/" + run.ID + "/steer"
	body := map[string]any{"input": "image", "attachments": []map[string]any{{"type": "image", "mime_type": "image/png", "filename": "fixture.png", "data_url": "data:image/png;base64,iVBORw0KGgo="}}}
	f.request(http.MethodPost, base, body, "image", http.StatusAccepted)
	// Delivery is simulated by the public leased-command API, not a Provider.
	for {
		command, ok, err := f.repo.ClaimLocalCommand(t.Context(), time.Now().UTC(), time.Minute)
		if err != nil || !ok {
			t.Fatalf("steer command not claimable: ok=%v err=%v", ok, err)
		}
		if err := f.repo.CompleteLocalCommand(t.Context(), command.ID, command.ClaimToken, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if command.Name == "run.steer" {
			break
		}
	}
	f.request(http.MethodPost, base, body, "image", http.StatusAccepted)
	changed := map[string]any{"input": "image", "attachments": []map[string]any{{"type": "image", "mime_type": "image/png", "filename": "different.png", "data_url": "data:image/png;base64,iVBORw0KGgo="}}}
	f.request(http.MethodPost, base, changed, "image", http.StatusConflict)
}

func TestRuntimeSurfaceMentionCycleAndSelfMentionTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	other := f.createAgent("surface B")
	base := "/v1/issues/" + f.issue.ID + "/comments"
	first := runtimeSurfaceData[domain.IssueComment](t, f.request(http.MethodPost, base, map[string]any{"body": "activate A", "mentioned_agent_ids": []string{f.agent.ID}}, "", http.StatusCreated))
	activate := func(commentID, target, key string) domain.Run {
		t.Helper()
		mentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, f.issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, mention := range mentions {
			if mention.CommentID != commentID || mention.TargetAgentID != target {
				continue
			}
			run, err := f.repo.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, target, mention.ID, []domain.ContentBlock{{Type: "text", Text: "fixture mention prompt"}}, key)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := f.repo.CreateRunForMention(t.Context(), f.user.ID, f.issue.ID, target, mention.ID, []domain.ContentBlock{{Type: "text", Text: "fixture mention prompt"}}, key)
			if err != nil || replay.ID != run.ID {
				t.Fatal("mention retry duplicated activation")
			}
			f.events(run, nodeprotocol.RunEvent{Sequence: 1, Type: "run.started", Payload: map[string]any{}})
			return run
		}
		t.Fatal("delegation mention missing")
		return domain.Run{}
	}
	a := activate(first.ID, f.agent.ID, "mention-A")
	tools := toolservice.New(f.repo)
	self := tools.Execute(t.Context(), f.computer.ID, nodeprotocol.NodeRequest{RunID: a.ID, Tool: "issue_comment_create", IdempotencyKey: "self", Payload: map[string]any{"body": "forbidden self", "mentioned_agent_ids": []string{f.agent.ID}}})
	if self.Result.OK || self.Result.ErrorCode != "self_mention" {
		t.Fatalf("self mention accepted: %+v", self)
	}
	delegate := func(run domain.Run, target, key string) string {
		t.Helper()
		result := tools.Execute(t.Context(), f.computer.ID, nodeprotocol.NodeRequest{RunID: run.ID, Tool: "issue_comment_create", IdempotencyKey: key, Payload: map[string]any{"body": key, "mentioned_agent_ids": []string{target}}})
		if !result.Result.OK {
			t.Fatalf("indirect cycle rejected: %+v", result)
		}
		return result.Result.Data["comment_id"].(string)
	}
	b := activate(delegate(a, other.ID, "A-to-B"), other.ID, "mention-B")
	a2 := activate(delegate(b, f.agent.ID, "B-to-A"), f.agent.ID, "mention-A2")
	if a.IssueAgentInstanceID == a2.IssueAgentInstanceID || a2.AgentID != a.AgentID {
		t.Fatal("indirect loop did not create independent A conversation")
	}
	comments := runtimeSurfaceData[domain.Page[domain.IssueComment]](t, f.request(http.MethodGet, base, nil, "", http.StatusOK))
	wantBodies := map[string]string{"activate A": domain.IssueCommentPublished, "A-to-B": domain.IssueCommentPublished, "B-to-A": domain.IssueCommentPublished, "[teamva:working]": domain.IssueCommentWorking}
	if len(comments.Items) != len(wantBodies) {
		t.Fatalf("cycle must have three published comments and A2 working placeholder, got %+v", comments.Items)
	}
	for _, comment := range comments.Items {
		if status, ok := wantBodies[comment.Body]; !ok || status != comment.Status {
			t.Fatalf("self mention ghost or unexpected comment: %+v", comment)
		}
	}
	instances := runtimeSurfaceData[domain.Page[domain.IssueAgentInstance]](t, f.request(http.MethodGet, "/v1/issues/"+f.issue.ID+"/agent-instances", nil, "", http.StatusOK))
	if len(instances.Items) != 4 { // one initial idle fixture plus A, B, A2
		t.Fatalf("mention conversation count=%d want=4", len(instances.Items))
	}
}

func TestRuntimeSurfaceConcurrentAdmissionTCP(t *testing.T) {
	for _, family := range []string{"issue", "conversation", "conversation-goal", "assistant"} {
		for _, sameKey := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same-key=%t", family, sameKey), func(t *testing.T) {
				f := newRuntimeSurfaceFixture(t)
				path := "/v1/issues/" + f.issue.ID + "/runs"
				body := f.issueRunBody("concurrent admission")
				createdStatus, replayStatus := http.StatusAccepted, http.StatusOK
				switch family {
				case "conversation", "conversation-goal":
					graph := f.graph()
					path = "/v1/conversation-nodes/" + graph.Nodes[0].ID
					if family == "conversation" {
						path += "/messages"
						body = map[string]any{"body": "concurrent admission"}
						createdStatus, replayStatus = http.StatusCreated, http.StatusCreated
					} else {
						path += "/goals"
						body = map[string]any{"objective": "concurrent admission", "token_budget": 10000}
					}
				case "assistant":
					lumi := runtimeSurfaceData[domain.Agent](t, f.request(http.MethodGet, "/v1/work-assistant", nil, "", http.StatusOK))
					f.request(http.MethodPatch, "/v1/agents/"+lumi.ID, map[string]any{"expected_version": lumi.Version, "agent_service_id": f.service.ID, "model": "surface-test"}, "", http.StatusOK)
					session := runtimeSurfaceData[domain.AssistantSession](t, f.request(http.MethodPost, "/v1/work-assistant/sessions", map[string]any{"title": "concurrent"}, "", http.StatusCreated))
					path = "/v1/work-assistant/sessions/" + session.ID + "/messages"
					body = map[string]any{"body": "concurrent admission"}
					replayStatus = http.StatusAccepted
				}
				results := make(chan runtimeSurfaceResponse, 2)
				start := make(chan struct{})
				for i := range 2 {
					go func(i int) {
						<-start
						key := "concurrent"
						if !sameKey {
							key += fmt.Sprint(i)
						}
						res, err := f.do(http.MethodPost, path, body, key)
						if err != nil {
							res.Status, res.Raw = -1, []byte(err.Error())
						}
						results <- res
					}(i)
				}
				close(start)
				first, second := <-results, <-results
				want := map[int]int{createdStatus: 1}
				if sameKey {
					want[replayStatus]++
				} else {
					want[http.StatusConflict]++
				}
				got := map[int]int{}
				got[first.Status]++
				got[second.Status]++
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("admission statuses=%v want=%v first=%s second=%s", got, want, first.Raw, second.Raw)
				}
				if sameKey {
					var a, b struct {
						ID string `json:"id"`
					}
					if json.Unmarshal(first.Data, &a) != nil || json.Unmarshal(second.Data, &b) != nil || a.ID == "" || a.ID != b.ID {
						t.Fatal("concurrent replay duplicated resource")
					}
				}
			})
		}
	}
}

func TestRuntimeSurfaceInstancePathScopeTCP(t *testing.T) {
	for _, action := range []string{"pause", "resume"} {
		t.Run(action, func(t *testing.T) {
			f := newRuntimeSurfaceFixture(t)
			other, err := f.repo.CreateIssue(t.Context(), f.user.ID, domain.Issue{ProjectID: f.project.ID, Title: "different parent"})
			if err != nil {
				t.Fatal(err)
			}
			instance := f.instance
			if action == "resume" {
				instance = runtimeSurfaceData[domain.IssueAgentInstance](t, f.request(http.MethodPost,
					"/v1/issues/"+f.issue.ID+"/agent-instances/"+instance.ID+"/pause", map[string]any{"expected_version": instance.Version}, "", http.StatusOK))
			}
			f.request(http.MethodPost, "/v1/issues/"+other.ID+"/agent-instances/"+instance.ID+"/"+action,
				map[string]any{"expected_version": instance.Version}, "", http.StatusNotFound)
			after, err := f.repo.GetIssueAgentInstanceByID(t.Context(), f.user.ID, instance.ID)
			if err != nil || !reflect.DeepEqual(instance, after) {
				t.Fatal("wrong-parent request mutated instance")
			}
		})
	}
}

func TestRuntimeSurfaceReadonlyAssistantStopTCP(t *testing.T) {
	f := newRuntimeSurfaceFixture(t)
	lumi := runtimeSurfaceData[domain.Agent](t, f.request(http.MethodGet, "/v1/work-assistant", nil, "", http.StatusOK))
	f.request(http.MethodPatch, "/v1/agents/"+lumi.ID, map[string]any{"expected_version": lumi.Version, "agent_service_id": f.service.ID, "model": "surface-test"}, "", http.StatusOK)
	session := runtimeSurfaceData[domain.AssistantSession](t, f.request(http.MethodPost, "/v1/work-assistant/sessions", map[string]any{"title": "stop"}, "", http.StatusCreated))
	run := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, "/v1/work-assistant/sessions/"+session.ID+"/messages", map[string]any{"body": "question"}, "stop", http.StatusAccepted))
	stopped := runtimeSurfaceData[domain.Run](t, f.request(http.MethodPost, "/v1/runs/"+run.ID+"/stop", map[string]any{"mode": "force"}, "", http.StatusAccepted))
	if stopped.Status != domain.RunCancelling {
		t.Fatal("readonly assistant cannot be stopped")
	}
}

func TestRuntimeSurfaceDeletedParentsHideRunsTCP(t *testing.T) {
	for _, kind := range []string{"issue", "graph"} {
		t.Run(kind, func(t *testing.T) {
			f := newRuntimeSurfaceFixture(t)
			var run domain.Run
			if kind == "issue" {
				run = f.createRun("delete-parent")
				f.finish(run, 1)
				current, err := f.repo.GetIssue(t.Context(), f.user.ID, f.issue.ID)
				if err != nil {
					t.Fatal(err)
				}
				f.request(http.MethodDelete, "/v1/issues/"+f.issue.ID+"?expected_version="+fmt.Sprint(current.Version), nil, "", http.StatusNoContent)
			} else {
				graph := f.graph()
				message := runtimeSurfaceData[domain.ConversationMessage](t, f.request(http.MethodPost, "/v1/conversation-nodes/"+graph.Nodes[0].ID+"/messages", map[string]any{"body": "question"}, "delete-parent", http.StatusCreated))
				var err error
				run, err = f.repo.GetRun(t.Context(), f.user.ID, message.RunID)
				if err != nil {
					t.Fatal(err)
				}
				f.finish(run, 1)
				f.request(http.MethodDelete, "/v1/conversation-graphs/"+graph.Graph.ID+"?expected_version=1", nil, "", http.StatusNoContent)
			}
			for _, suffix := range []string{"", "/events", "/events/stream"} {
				f.request(http.MethodGet, "/v1/runs/"+run.ID+suffix, nil, "", http.StatusNotFound)
			}
			f.request(http.MethodPost, "/v1/runs/"+run.ID+"/stop", map[string]any{"mode": "force"}, "", http.StatusNotFound)
		})
	}
}
