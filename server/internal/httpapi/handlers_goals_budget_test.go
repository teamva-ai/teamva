package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestIssueGoalHTTPResumeCannotBypassExhaustedBudget(t *testing.T) {
	for _, status := range []string{domain.GoalBudgetReached, domain.GoalPaused, domain.GoalBlocked} {
		t.Run(status, func(t *testing.T) {
			repo := newTestRepository(t)
			router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
			auth := testLoginHeaders(t, repo, router)
			auth["Accept-Language"] = "zh-CN"
			user, computer, err := repo.LocalIdentity()
			if err != nil {
				t.Fatal(err)
			}
			service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{
				ComputerID: computer.ID, Provider: "codex", Status: "ready", LoginStatus: "authenticated",
				Capabilities: map[string]any{"models": []string{"test"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			const privateFixture = "fixture-private-goal-objective-do-not-echo"
			agent, err := repo.CreateConfiguredAgent(t.Context(), user.ID, domain.Agent{
				ComputerID: computer.ID, AgentServiceID: service.ID, Name: "budget guard agent",
				Model: "test", Status: domain.AgentActive,
			}, "fixture-private-system-prompt", nil)
			if err != nil {
				t.Fatal(err)
			}
			project, err := repo.CreateProject(t.Context(), user.ID, domain.Project{ComputerID: computer.ID, Name: "budget guard"})
			if err != nil {
				t.Fatal(err)
			}
			issue, err := repo.CreateIssue(t.Context(), user.ID, domain.Issue{ProjectID: project.ID, Title: "budget guard"})
			if err != nil {
				t.Fatal(err)
			}
			instance, _, err := repo.EnsureIssueAgentInstance(t.Context(), user.ID, issue.ID, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			empty := decodeData[struct {
				Goal *domain.IssueAgentGoal `json:"goal"`
			}](t, request(t, router, http.MethodGet, "/v1/agent-instances/"+instance.ID+"/goal", nil, auth, http.StatusOK))
			if empty.Goal != nil {
				t.Fatal("new instance unexpectedly has a Goal")
			}
			budget := int64(60_000)
			used := int64(139_950)
			work, goal, _, err := repo.CreateIssueAgentGoalRun(t.Context(), user.ID, issue.ID, instance.ID,
				issue.Version, "budget-http-create", privateFixture, &budget, domain.Run{
					AgentID: agent.ID, IssueAgentInstanceID: instance.ID,
					Input: []domain.ContentBlock{{Type: "text", Text: "fixture-private-work-input"}},
				})
			if err != nil {
				t.Fatal(err)
			}
			if status == domain.GoalPaused {
				if _, err := repo.PauseIssueAgentGoal(t.Context(), user.ID, goal.ID, goal.Version); err != nil {
					t.Fatal(err)
				}
			}
			terminalStatus := "failed"
			if status == domain.GoalBudgetReached {
				terminalStatus = "completed"
			}
			events := []nodeprotocol.RunEvent{{Type: "run.finished", Payload: map[string]any{"status": terminalStatus}}}
			if status == domain.GoalBudgetReached {
				events = append([]nodeprotocol.RunEvent{{Type: "usage.updated", Payload: map[string]any{"total_tokens": used}}}, events...)
			}
			for i := range events {
				events[i].ID = work.ID + "-fixture-" + string(rune('a'+i))
				events[i].RunID, events[i].LeaseEpoch, events[i].Sequence = work.ID, work.LeaseEpoch, int64(i+1)
				events[i].OccurredAt = time.Now().UTC()
			}
			if _, err := repo.ApplyLocalEvents(t.Context(), events); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.AdvanceLocalGoals(t.Context(), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if status != domain.GoalBudgetReached {
				if _, err := repo.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
					ID: work.ID + "-late-usage", RunID: work.ID, LeaseEpoch: work.LeaseEpoch, Sequence: 2,
					Type: "usage.updated", Payload: map[string]any{"total_tokens": used}, OccurredAt: time.Now().UTC(),
				}}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := repo.GetIssueAgentGoal(t.Context(), user.ID, goal.ID)
			if err != nil || before.Status != status || before.TokensUsed != used {
				t.Fatalf("fixture Goal=%+v err=%v", before, err)
			}
			instanceBefore, err := repo.GetIssueAgentInstanceByID(t.Context(), user.ID, instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			current := decodeData[struct {
				Goal domain.IssueAgentGoal `json:"goal"`
			}](t, request(t, router, http.MethodGet, "/v1/agent-instances/"+instance.ID+"/goal", nil, auth, http.StatusOK))
			if current.Goal.TokensUsed != used || current.Goal.Version != before.Version {
				t.Fatalf("HTTP late-usage projection=%+v", current.Goal)
			}
			path := "/v1/goals/" + goal.ID
			for range 2 {
				response := request(t, router, http.MethodPost, path+"/resume",
					map[string]any{"expected_version": before.Version}, auth, http.StatusConflict)
				var envelope struct {
					Error struct {
						Code    string                     `json:"code"`
						Details map[string]json.RawMessage `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Recorder.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Error.Code != "goal_budget_reached" || len(envelope.Error.Details) != 2 {
					t.Fatalf("unsafe/incorrect budget error: %s", response.Recorder.Body.String())
				}
				for key, want := range map[string]int64{"token_budget": budget, "tokens_used": used} {
					var number int64
					if err := json.Unmarshal(envelope.Error.Details[key], &number); err != nil || number != want {
						t.Fatalf("numeric detail %s=%s, want %d", key, envelope.Error.Details[key], want)
					}
				}
				for _, private := range []string{privateFixture, "fixture-private-system-prompt", "fixture-private-work-input"} {
					if strings.Contains(response.Recorder.Body.String(), private) {
						t.Fatal("budget error leaked private fixture input")
					}
				}
			}
			after, err := repo.GetIssueAgentGoal(t.Context(), user.ID, goal.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused resume changed Goal: before=%+v after=%+v err=%v", before, after, err)
			}
			instanceAfter, err := repo.GetIssueAgentInstanceByID(t.Context(), user.ID, instance.ID)
			if err != nil || !reflect.DeepEqual(instanceBefore, instanceAfter) {
				t.Fatalf("refused resume changed instance: before=%+v after=%+v err=%v", instanceBefore, instanceAfter, err)
			}
			var runs, starts, goals int
			if err := repo.db.QueryRowContext(t.Context(), `SELECT
				(SELECT count(*) FROM runs),
				(SELECT count(*) FROM command_outbox WHERE command_name = 'run.start'),
				(SELECT count(*) FROM issue_agent_goals)
			`).Scan(&runs, &starts, &goals); err != nil {
				t.Fatal(err)
			}
			if runs != 1 || starts != 1 || goals != 1 {
				t.Fatalf("refused resume created work: runs=%d starts=%d goals=%d", runs, starts, goals)
			}
			request(t, router, http.MethodPost, path+"/cancel", map[string]any{"expected_version": before.Version}, auth, http.StatusOK)
		})
	}
}
