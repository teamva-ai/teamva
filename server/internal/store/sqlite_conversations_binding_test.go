package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func secondConversationAgent(t *testing.T, f sqliteExecutionFixture) domain.Agent {
	t.Helper()
	agent, err := f.repository.CreateConfiguredAgent(t.Context(), f.user.ID, domain.Agent{
		ComputerID: f.computer.ID, AgentServiceID: f.service.ID, Name: "另一 Agent", Model: f.agent.Model, Status: domain.AgentActive,
	}, "新 Agent 提示词", nil)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func conversationBinding(node domain.ConversationNode) domain.ConversationAgentBinding {
	return domain.ConversationAgentBinding{ExpectedAgentID: node.AgentID, ExpectedAgentRevisionID: node.AgentRevisionID}
}

func assertConversationUnchanged(t *testing.T, f sqliteExecutionFixture, before domain.ConversationGraphDetail) {
	t.Helper()
	after, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, before.Graph.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed request changed graph: %v\nbefore=%+v\nafter=%+v", err, before, after)
	}
}

func conversationAutoTitleMode(t *testing.T, f sqliteExecutionFixture, kind, id string) (string, string) {
	t.Helper()
	var mode, seed string
	err := f.repository.db.QueryRow(`SELECT COALESCE(json_extract(value,'$.mode'),''),COALESCE(json_extract(value,'$.seed'),'') FROM app_metadata WHERE key=?`, autoTitleKey(kind, id)).Scan(&mode, &seed)
	if err != nil {
		t.Fatal(err)
	}
	return mode, seed
}

func TestConversationEmptyGraphAndAtomicRootInitialization(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	graph, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID}, domain.ConversationNode{})
	if err != nil {
		t.Fatal(err)
	}
	if graph.Graph.Title != "新对话" || graph.Graph.Version != 1 || graph.Graph.NodeCount != 0 || graph.Nodes == nil || len(graph.Nodes) != 0 || graph.Messages == nil || graph.Goals == nil {
		t.Fatalf("empty graph=%+v", graph)
	}
	if mode, _ := conversationAutoTitleMode(t, f, "conversation_graph", graph.Graph.ID); mode != "awaiting" {
		t.Fatalf("mode=%s", mode)
	}
	_, err = f.repository.InitializeConversationGraphRoot(t.Context(), "other-user", graph.Graph.ID, f.agent.ID, 1)
	assertConversationError(t, err, "not_found")
	_, err = f.repository.InitializeConversationGraphRoot(t.Context(), f.user.ID, graph.Graph.ID, "missing-agent", 1)
	assertConversationError(t, err, "not_found")
	assertConversationUnchanged(t, f, graph)
	renamed, err := f.repository.UpdateConversationGraphTitle(t.Context(), f.user.ID, graph.Graph.ID, 1, "手工图标题")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.InitializeConversationGraphRoot(t.Context(), f.user.ID, graph.Graph.ID, f.agent.ID, 1)
	assertConversationError(t, err, "version_conflict")
	var wg sync.WaitGroup
	results := make(chan domain.ConversationGraphDetail, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			detail, err := f.repository.InitializeConversationGraphRoot(t.Context(), f.user.ID, graph.Graph.ID, f.agent.ID, renamed.Version)
			results <- detail
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rootID string
	for detail := range results {
		if len(detail.Nodes) != 1 || detail.Graph.NodeCount != 1 || detail.Graph.Version != renamed.Version+1 || detail.Nodes[0].Title != "新节点" || detail.Nodes[0].AgentRevisionID != f.instance.AgentRevisionID {
			t.Fatalf("root=%+v", detail)
		}
		if rootID != "" && rootID != detail.Nodes[0].ID {
			t.Fatal("concurrent root duplication")
		}
		rootID = detail.Nodes[0].ID
	}
	if mode, _ := conversationAutoTitleMode(t, f, "conversation_node", rootID); mode != "awaiting" {
		t.Fatalf("root title mode=%s", mode)
	}
	other := secondConversationAgent(t, f)
	_, err = f.repository.InitializeConversationGraphRoot(t.Context(), f.user.ID, graph.Graph.ID, other.ID, renamed.Version)
	assertConversationError(t, err, "root_already_exists")
}

func TestConversationCompetingRootAgentsHaveOneWinner(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	other := secondConversationAgent(t, f)
	graph, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID}, domain.ConversationNode{})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{f.agent.ID, other.ID} {
		go func() {
			<-start
			_, err := f.repository.InitializeConversationGraphRoot(t.Context(), f.user.ID, graph.Graph.ID, id, 1)
			errs <- err
		}()
	}
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err == nil {
			wins++
		} else {
			assertConversationError(t, err, "root_already_exists")
		}
	}
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
	detail, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
	if err != nil || len(detail.Nodes) != 1 || detail.Graph.Version != 2 {
		t.Fatalf("graph=%+v %v", detail, err)
	}
}

func TestConversationSwitchAgentPreservesHistoryAndForkBinding(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	other := secondConversationAgent(t, f)
	root := graph.Nodes[0]
	oldRun := finishConversationForkTurn(t, f, root.ID, "old-turn", "旧问题", "旧回答", "completed")
	messages, err := f.repository.ListConversationMessages(t.Context(), f.user.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, err = f.repository.GetConversationNode(t.Context(), f.user.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, root.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID })
	if err != nil || changed.AgentID != other.ID || changed.AgentRevisionID != other.CurrentRevisionID || changed.Version != root.Version+1 {
		t.Fatalf("changed=%+v %v", changed, err)
	}
	actualMessages, _ := f.repository.ListConversationMessages(t.Context(), f.user.ID, root.ID)
	actualRun, _ := f.repository.GetRun(t.Context(), f.user.ID, oldRun.ID)
	if !reflect.DeepEqual(messages, actualMessages) || !reflect.DeepEqual(oldRun, actualRun) {
		t.Fatal("switch rewrote historical message/run ownership")
	}
	if _, err := f.repository.CreateAgentRevision(t.Context(), f.user.ID, domain.AgentRevision{AgentID: other.ID, AgentServiceID: f.service.ID, Model: other.Model, SystemPrompt: "later revision"}, nil); err != nil {
		t.Fatal(err)
	}
	noop, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, changed.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID })
	if err != nil || !reflect.DeepEqual(changed, noop) {
		t.Fatal("same Agent silently upgraded revision/version")
	}
	child, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, root.ID, messages[1].ID, domain.ConversationNode{})
	if err != nil || child.Title != "新分支" || child.AgentID != changed.AgentID || child.AgentRevisionID != changed.AgentRevisionID {
		t.Fatalf("fork=%+v %v", child, err)
	}
	parent, err := f.repository.GetConversationNode(t.Context(), f.user.ID, child.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	if !parent.Locked || parent.AgentID != other.ID || parent.AgentRevisionID != changed.AgentRevisionID {
		t.Fatal("frozen parent lost binding")
	}
	_, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, parent.ID, parent.Version, func(n *domain.ConversationNode) { n.AgentID = f.agent.ID; n.Title = "must roll back" })
	assertConversationError(t, err, "node_locked")
	parent, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, parent.ID, parent.Version, func(n *domain.ConversationNode) { n.Title = "历史可重命名"; n.TitleEdited = true })
	if err != nil || parent.Title != "历史可重命名" || !parent.Locked {
		t.Fatalf("locked rename=%+v %v", parent, err)
	}
	noop, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, parent.ID, parent.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID })
	if err != nil || noop.Version != parent.Version {
		t.Fatal("locked same-Agent no-op rejected")
	}
	message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, child.ID, "new-turn", "后续问题", conversationBinding(child))
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.repository.GetRun(t.Context(), f.user.ID, message.RunID)
	var revisionID string
	if err := f.repository.db.QueryRow(`SELECT json_extract(snapshot,'$.agent_revision_id') FROM runs WHERE id=?`, message.RunID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	if err != nil || run.AgentID != other.ID || revisionID != changed.AgentRevisionID {
		t.Fatalf("future run=%+v %v", run, err)
	}
}

func TestConversationSwitchAgentRejectsBusyAndEveryUnfinishedGoal(t *testing.T) {
	for _, status := range []string{"queued", "starting", "running", "waiting_approval", "cancelling"} {
		t.Run("run-"+status, func(t *testing.T) {
			f, graph := createConversationFixture(t)
			defer f.repository.Close()
			other := secondConversationAgent(t, f)
			message, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, graph.Nodes[0].ID, "busy", "question")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.repository.db.Exec(`UPDATE runs SET status=? WHERE id=?`, status, message.RunID); err != nil {
				t.Fatal(err)
			}
			before, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, before.Nodes[0].ID, before.Nodes[0].Version, func(n *domain.ConversationNode) { n.AgentID = other.ID; n.Title = "not committed" })
			assertConversationError(t, err, "target_busy")
			assertConversationUnchanged(t, f, before)
			noop, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, before.Nodes[0].ID, before.Nodes[0].Version, func(n *domain.ConversationNode) { n.AgentID = before.Nodes[0].AgentID })
			if err != nil || noop.Version != before.Nodes[0].Version {
				t.Fatal("busy same-Agent no-op")
			}
			_, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, noop.ID, noop.Version, func(n *domain.ConversationNode) { n.Title = "运行中改标题" })
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.repository.UpdateConversationGraphTitle(t.Context(), f.user.ID, graph.Graph.ID, before.Graph.Version, "运行中改图标题")
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, status := range []string{"active", "paused", "blocked", "budget_reached"} {
		t.Run("goal-"+status, func(t *testing.T) {
			f, graph := createConversationFixture(t)
			defer f.repository.Close()
			other := secondConversationAgent(t, f)
			root := graph.Nodes[0]
			// Deliberately place a terminal goal after the unfinished one. Checking
			// only the latest projection would incorrectly allow a binding change.
			for i, s := range []string{status, "completed"} {
				if _, err := f.repository.db.Exec(`INSERT INTO conversation_goals(id,node_id,agent_revision_id,objective,idempotency_key,status,phase,max_work_runs,created_at,updated_at) VALUES (?,?,?,?,?,?, 'settled',5,?,?)`, newID("cgol"), root.ID, root.AgentRevisionID, "objective", newID("key"), s, sqliteTimeValue(root.CreatedAt.AddDate(0, 0, i)), sqliteTimeValue(root.CreatedAt)); err != nil {
					t.Fatal(err)
				}
			}
			before, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, root.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID; n.Title = "not committed" })
			assertConversationError(t, err, "goal_already_exists")
			assertConversationUnchanged(t, f, before)
		})
	}
}

func TestConversationBindingReplayPrecedesCurrentBindingAndLockedChecks(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	other := secondConversationAgent(t, f)
	root := graph.Nodes[0]
	binding := conversationBinding(root)
	first, _, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "bound-turn", "question", binding)
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.repository.GetRun(t.Context(), f.user.ID, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	applySQLiteGoalEvents(t, f.repository, run, nodeprotocol.RunEvent{Type: "run.started"}, nodeprotocol.RunEvent{Type: "run.finished", Payload: map[string]any{"status": "completed"}})
	root, err = f.repository.GetConversationNode(t.Context(), f.user.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, root.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID })
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
	if err != nil {
		t.Fatal(err)
	}
	replay, hit, err := f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "bound-turn", "question", binding)
	if err != nil || !hit || replay.ID != first.ID {
		t.Fatalf("historical replay=%v %v", hit, err)
	}
	_, _, err = f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "bound-turn", "question", conversationBinding(changed))
	assertConversationError(t, err, "idempotency_conflict")
	_, _, err = f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "stale-new", "question", binding)
	assertConversationError(t, err, "agent_binding_conflict")
	_, _, err = f.repository.CreateConversationGoal(t.Context(), f.user.ID, root.ID, "stale-goal", "objective", nil, binding)
	assertConversationError(t, err, "agent_binding_conflict")
	assertConversationUnchanged(t, f, before)
	if _, err := f.repository.db.Exec(`UPDATE conversation_nodes SET locked=1 WHERE id=?`, root.ID); err != nil {
		t.Fatal(err)
	}
	_, hit, err = f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "bound-turn", "question", binding)
	if err != nil || !hit {
		t.Fatalf("locked replay=%v %v", hit, err)
	}
}

func TestConversationGoalBindingFingerprintAndHistoricalReplay(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	other := secondConversationAgent(t, f)
	root := graph.Nodes[0]
	binding := conversationBinding(root)
	first, _, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, root.ID, "bound-goal", "objective", nil, binding)
	if err != nil {
		t.Fatal(err)
	}
	completeConversationGoalRun(t, f, first, "done", 1)
	if _, err := f.repository.db.Exec(`UPDATE conversation_goals SET status='completed',phase='settled' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	root, err = f.repository.GetConversationNode(t.Context(), f.user.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, root.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID })
	if err != nil {
		t.Fatal(err)
	}
	replay, hit, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, root.ID, "bound-goal", "objective", nil, binding)
	if err != nil || !hit || replay.ID != first.ID {
		t.Fatalf("goal replay=%v %v", hit, err)
	}
	_, _, err = f.repository.CreateConversationGoal(t.Context(), f.user.ID, root.ID, "bound-goal", "objective", nil, conversationBinding(changed))
	assertConversationError(t, err, "idempotency_conflict")
	_, _, err = f.repository.CreateConversationGoal(t.Context(), f.user.ID, root.ID, "bound-goal", "objective", nil)
	assertConversationError(t, err, "idempotency_conflict")
	if _, err := f.repository.db.Exec(`UPDATE conversation_nodes SET locked=1 WHERE id=?`, root.ID); err != nil {
		t.Fatal(err)
	}
	_, hit, err = f.repository.CreateConversationGoal(t.Context(), f.user.ID, root.ID, "bound-goal", "objective", nil, binding)
	if err != nil || !hit {
		t.Fatalf("locked goal replay=%v %v", hit, err)
	}
}

func TestConversationAgentChangeSendRaceHasNoMisdirectedWork(t *testing.T) {
	for _, goalMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "message", true: "goal"}[goalMode], func(t *testing.T) {
			f, graph := createConversationFixture(t)
			defer f.repository.Close()
			other := secondConversationAgent(t, f)
			root := graph.Nodes[0]
			start := make(chan struct{})
			done := make(chan error, 2)
			go func() {
				<-start
				_, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, root.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID })
				done <- err
			}()
			go func() {
				<-start
				var err error
				if goalMode {
					_, _, err = f.repository.CreateConversationGoal(t.Context(), f.user.ID, root.ID, "race", "objective", nil, conversationBinding(root))
				} else {
					_, _, err = f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "race", "question", conversationBinding(root))
				}
				done <- err
			}()
			close(start)
			success := 0
			for i := 0; i < 2; i++ {
				if err := <-done; err == nil {
					success++
				} else {
					var de *domain.Error
					if !errors.As(err, &de) || de.Code != "version_conflict" && de.Code != "agent_binding_conflict" {
						t.Fatalf("race err=%v", err)
					}
				}
			}
			if success != 1 {
				t.Fatalf("success=%d", success)
			}
			var count, wrong int
			if err := f.repository.db.QueryRow(`SELECT count(*),COALESCE(SUM(CASE WHEN agent_id<>? OR json_extract(snapshot,'$.agent_revision_id')<>? THEN 1 ELSE 0 END),0) FROM runs WHERE conversation_node_id=?`, root.AgentID, root.AgentRevisionID, root.ID).Scan(&count, &wrong); err != nil {
				t.Fatal(err)
			}
			if wrong != 0 || count > 1 {
				t.Fatal("misdirected/duplicated work")
			}
		})
	}
}

func TestConversationTitleIntentAndFirstContentAreTransactional(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	graph, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID}, domain.ConversationNode{AgentID: f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	root := graph.Nodes[0]
	// Layout-only changes must not revoke future automatic naming.
	root, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, root.Version, func(n *domain.ConversationNode) { n.PositionX = 123 })
	if err != nil {
		t.Fatal(err)
	}
	if mode, _ := conversationAutoTitleMode(t, f, "conversation_node", root.ID); mode != "awaiting" {
		t.Fatalf("layout title mode=%s", mode)
	}
	if _, err := f.repository.db.Exec(`CREATE TRIGGER reject_bound_titles BEFORE INSERT ON app_metadata WHEN NEW.key LIKE 'auto_title:v1:%' BEGIN SELECT RAISE(ABORT,'title write failed'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "retryable", "首条问题", conversationBinding(root))
	if err == nil {
		t.Fatal("ignored title transaction failure")
	}
	var runs int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM runs WHERE conversation_node_id=?`, root.ID).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("partial run=%d %v", runs, err)
	}
	if _, err := f.repository.db.Exec(`DROP TRIGGER reject_bound_titles`); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.repository.SendConversationMessage(t.Context(), f.user.ID, root.ID, "retryable", "首条问题", conversationBinding(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ kind, id string }{{"conversation_node", root.ID}, {"conversation_graph", graph.Graph.ID}} {
		if mode, seed := conversationAutoTitleMode(t, f, target.kind, target.id); mode != "queued" || seed != "首条问题" {
			t.Fatalf("queued mode=%s seed=%q", mode, seed)
		}
	}
	root, err = f.repository.GetConversationNode(t.Context(), f.user.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, root.Version, func(n *domain.ConversationNode) { n.TitleEdited = true })
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.repository.UpdateConversationGraphTitle(t.Context(), f.user.ID, graph.Graph.ID, graph.Graph.Version, graph.Graph.Title)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ kind, id string }{{"conversation_node", root.ID}, {"conversation_graph", graph.Graph.ID}} {
		if mode, seed := conversationAutoTitleMode(t, f, target.kind, target.id); mode != "manual" || seed != "" {
			t.Fatalf("manual mode=%s seed=%q", mode, seed)
		}
	}
	encoded, _ := json.Marshal(root)
	if strings.Contains(string(encoded), "TitleEdited") || strings.Contains(string(encoded), "title_edited") {
		t.Fatal("title intent leaked to JSON")
	}
}

func TestConversationExplicitTitlesDoNotInitializeAutoStateAndForkOnlyInitializesChild(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	var states int
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM app_metadata WHERE key IN (?,?)`, autoTitleKey("conversation_graph", graph.Graph.ID), autoTitleKey("conversation_node", graph.Nodes[0].ID)).Scan(&states); err != nil || states != 0 {
		t.Fatalf("explicit titles initialized auto state: %d %v", states, err)
	}
	root := graph.Nodes[0]
	boundary := appendConversationMessage(t, f, root.ID, "agent", "boundary")
	child, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, root.ID, boundary.ID, domain.ConversationNode{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRow(`SELECT count(*) FROM app_metadata WHERE key IN (?,?,?)`, autoTitleKey("conversation_node", root.ID), autoTitleKey("conversation_node", child.ParentID), autoTitleKey("conversation_node", child.ID)).Scan(&states); err != nil || states != 1 {
		t.Fatalf("fork initialized non-child state: %d %v", states, err)
	}
	if mode, _ := conversationAutoTitleMode(t, f, "conversation_node", child.ID); mode != "awaiting" {
		t.Fatal("child title not awaiting content")
	}
}

func TestConversationGoalQueuesInitialObjectiveOnlyOnce(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	graph, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID}, domain.ConversationNode{AgentID: f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	goal, _, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, graph.Nodes[0].ID, "first-objective", "最先的目标", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ kind, id string }{{"conversation_graph", graph.Graph.ID}, {"conversation_node", graph.Nodes[0].ID}} {
		if mode, seed := conversationAutoTitleMode(t, f, target.kind, target.id); mode != "queued" || seed != "最先的目标" {
			t.Fatalf("goal title seed %s/%s", mode, seed)
		}
	}
	if _, _, err := f.repository.CreateConversationGoal(t.Context(), f.user.ID, graph.Nodes[0].ID, "first-objective", "最先的目标", nil); err != nil {
		t.Fatal(err)
	}
	completeConversationGoalRun(t, f, goal, "work", 1)
	goal = advanceConversationGoalTest(t, f, goal.NodeID)
	completeConversationGoalRun(t, f, goal, `{"verdict":"continue","note":"next"}`, 1)
	advanceConversationGoalTest(t, f, goal.NodeID)
	for _, target := range []struct{ kind, id string }{{"conversation_graph", graph.Graph.ID}, {"conversation_node", graph.Nodes[0].ID}} {
		if mode, seed := conversationAutoTitleMode(t, f, target.kind, target.id); mode != "queued" || seed != "最先的目标" {
			t.Fatalf("audit/continuation replaced goal seed %s/%s", mode, seed)
		}
	}
}

func TestConversationInvalidAgentPatchRollsBackAllFieldsAndTitleIntent(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	other := secondConversationAgent(t, f)
	graph, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID}, domain.ConversationNode{AgentID: f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	root := graph.Nodes[0]
	for _, test := range []struct {
		name, agentID string
		version       int64
		setup         func()
		code          string
	}{
		{name: "missing", agentID: "missing", version: root.Version, code: "not_found"},
		{name: "empty", agentID: "", version: root.Version, code: "invalid_argument"},
		{name: "stale", agentID: other.ID, version: root.Version + 1, code: "version_conflict"},
		{name: "disabled", agentID: other.ID, version: root.Version, setup: func() {
			if _, err := f.repository.db.Exec(`UPDATE agents SET status='disabled' WHERE id=?`, other.ID); err != nil {
				t.Fatal(err)
			}
		}, code: "invalid_argument"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.setup != nil {
				test.setup()
			}
			_, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, test.version, func(n *domain.ConversationNode) {
				n.AgentID = test.agentID
				n.Title = "bad change"
				n.TitleEdited = true
				n.PositionX = 500
			})
			assertConversationError(t, err, test.code)
			assertConversationUnchanged(t, f, graph)
			if mode, _ := conversationAutoTitleMode(t, f, "conversation_node", root.ID); mode != "awaiting" {
				t.Fatal("failed patch changed automatic title ownership")
			}
		})
	}
	_, err = f.repository.UpdateConversationNode(t.Context(), "other-user", root.ID, root.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID })
	assertConversationError(t, err, "not_found")
}

func TestConversationTargetAgentOwnershipCheckedInsideEveryWrite(t *testing.T) {
	for _, foreignUser := range []bool{false, true} {
		t.Run(map[bool]string{false: "other-computer", true: "other-user"}[foreignUser], func(t *testing.T) {
			f, graph := createConversationFixture(t)
			defer f.repository.Close()
			other := secondConversationAgent(t, f)
			ownerID := f.user.ID
			if foreignUser {
				ownerID = newID("usr")
				if _, err := f.repository.db.Exec(`INSERT INTO users(id,email,display_name) VALUES (?,?,?)`, ownerID, ownerID+"@example.test", "other"); err != nil {
					t.Fatal(err)
				}
			}
			computerID := newID("cmp")
			if _, err := f.repository.db.Exec(`INSERT INTO computers(id,user_id,name,platform,architecture,fingerprint,status) VALUES (?,?,'other','test','test',?,'online')`, computerID, ownerID, computerID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.repository.db.Exec(`UPDATE agents SET computer_id=? WHERE id=?`, computerID, other.ID); err != nil {
				t.Fatal(err)
			}
			code := "invalid_argument"
			if foreignUser {
				code = "not_found"
			}
			_, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, graph.Nodes[0].ID, graph.Nodes[0].Version, func(n *domain.ConversationNode) { n.AgentID = other.ID; n.Title = "must roll back" })
			assertConversationError(t, err, code)
			assertConversationUnchanged(t, f, graph)
			boundary := appendConversationMessage(t, f, graph.Nodes[0].ID, "agent", "boundary")
			before, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, graph.Graph.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.repository.ForkConversationNode(t.Context(), f.user.ID, graph.Nodes[0].ID, boundary.ID, domain.ConversationNode{AgentID: other.ID})
			assertConversationError(t, err, code)
			assertConversationUnchanged(t, f, before)
			empty, err := f.repository.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID}, domain.ConversationNode{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.repository.InitializeConversationGraphRoot(t.Context(), f.user.ID, empty.Graph.ID, other.ID, empty.Graph.Version)
			assertConversationError(t, err, code)
			assertConversationUnchanged(t, f, empty)
			var countBefore, countAfter int
			if err := f.repository.db.QueryRow(`SELECT count(*) FROM conversation_graphs`).Scan(&countBefore); err != nil {
				t.Fatal(err)
			}
			_, err = f.repository.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID}, domain.ConversationNode{AgentID: other.ID})
			assertConversationError(t, err, code)
			if err := f.repository.db.QueryRow(`SELECT count(*) FROM conversation_graphs`).Scan(&countAfter); err != nil || countAfter != countBefore {
				t.Fatal("failed creation left graph")
			}
		})
	}
}

func TestConversationDefaultForkAgentRacesWithBindingChange(t *testing.T) {
	f, graph := createConversationFixture(t)
	defer f.repository.Close()
	other := secondConversationAgent(t, f)
	root := graph.Nodes[0]
	boundary := appendConversationMessage(t, f, root.ID, "agent", "boundary")
	start := make(chan struct{})
	updateResult := make(chan error, 1)
	forkResult := make(chan domain.ConversationNode, 1)
	forkError := make(chan error, 1)
	go func() {
		<-start
		_, err := f.repository.UpdateConversationNode(t.Context(), f.user.ID, root.ID, root.Version, func(n *domain.ConversationNode) { n.AgentID = other.ID })
		updateResult <- err
	}()
	go func() {
		<-start
		child, err := f.repository.ForkConversationNode(t.Context(), f.user.ID, root.ID, boundary.ID, domain.ConversationNode{})
		forkResult <- child
		forkError <- err
	}()
	close(start)
	if err := <-forkError; err != nil {
		t.Fatal(err)
	}
	child := <-forkResult
	if err := <-updateResult; err != nil {
		assertConversationError(t, err, "version_conflict")
	}
	source, err := f.repository.GetConversationNode(t.Context(), f.user.ID, root.ID)
	if err != nil || child.AgentID != source.AgentID || child.AgentRevisionID != source.AgentRevisionID {
		t.Fatalf("fork inherited stale binding: child=%+v source=%+v err=%v", child, source, err)
	}
}
