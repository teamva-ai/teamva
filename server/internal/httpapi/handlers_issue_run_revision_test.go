package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

func finishIssueRevisionHTTPRun(t *testing.T, repo *testRepository, run domain.Run) {
	t.Helper()
	if _, err := repo.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
		ID: run.ID + "-revision-finished", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1,
		Type: "run.finished", Payload: map[string]any{"status": "completed"}, OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
}

func issueRevisionHTTPPayload(t *testing.T, repo *testRepository, runID string) (string, string, nodeprotocol.StartRunPayload) {
	t.Helper()
	var snapshot, raw string
	if err := repo.db.QueryRowContext(t.Context(), `SELECT run.snapshot, command.payload FROM runs run
		JOIN command_outbox command ON command.command_name = 'run.start' AND json_extract(command.payload, '$.run_id') = run.id
		WHERE run.id = ?`, runID).Scan(&snapshot, &raw); err != nil {
		t.Fatal(err)
	}
	var payload nodeprotocol.StartRunPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return snapshot, raw, payload
}

func TestIssueRunHTTPFollowupUsesSavedAgentConfigurationWithoutChangingHistory(t *testing.T) {
	repo, user, issue, instance, old := issueHistoryFixture(t)
	finishIssueRevisionHTTPRun(t, repo, old)
	old, err := repo.GetRun(t.Context(), user.ID, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldSnapshot, oldRaw, oldPayload := issueRevisionHTTPPayload(t, repo, old.ID)
	if old.Model != "" || oldPayload.Model != "" {
		t.Fatal("fixture must reproduce an old Run with an unset model")
	}
	router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repo, router)
	agent, err := repo.GetAgent(t.Context(), user.ID, instance.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.UpsertAgentService(t.Context(), user.ID, domain.AgentService{
		ComputerID: agent.ComputerID, Provider: "opencode", Status: "ready", LoginStatus: "authenticated",
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := repo.UpsertSkillPackage(t.Context(), domain.SkillPackage{Slug: "followup-skill", Name: "Follow-up skill"})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := repo.InstallComputerSkillPackageVersion(t.Context(), user.ID, agent.ComputerID, pkg.ID,
		domain.SkillPackageVersion{CommitSHA: "0123456789abcdef0123456789abcdef01234567", ContentDigest: "sha256:followup",
			Manifest: map[string]any{"files": []any{"SKILL.md"}}, FileCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	patch := map[string]any{"expected_version": agent.Version, "agent_service_id": service.ID,
		"model": "deepseek/deepseek-flash", "system_prompt": "saved follow-up prompt",
		"skill_version_ids": []string{installed.SkillPackageVersionID}}
	current := decodeData[domain.Agent](t, request(t, router, http.MethodPatch, "/v1/agents/"+agent.ID, patch, auth, http.StatusOK))
	// A stale configuration save must not publish a different current revision.
	request(t, router, http.MethodPatch, "/v1/agents/"+agent.ID, patch, auth, http.StatusConflict)
	before, err := repo.GetIssueAgentInstanceByID(t.Context(), user.ID, instance.ID)
	if err != nil || before.AgentRevisionID != instance.AgentRevisionID {
		t.Fatalf("saving Agent must not rewrite historical instance eagerly: %+v err=%v", before, err)
	}
	if _, err := repo.CreateIssueComment(t.Context(), user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser, Body: "new mention",
	}, []string{agent.ID}); err != nil {
		t.Fatal(err)
	}
	mentions, err := repo.ListIssueCommentMentions(t.Context(), user.ID, issue.ID)
	if err != nil || len(mentions) != 1 {
		t.Fatalf("mentions=%+v err=%v", mentions, err)
	}
	newer, err := repo.CreateRunForMention(t.Context(), user.ID, issue.ID, agent.ID, mentions[0].ID,
		[]domain.ContentBlock{{Type: "text", Text: "new activation"}}, "revision-new-mention")
	if err != nil {
		t.Fatal(err)
	}
	auth["Idempotency-Key"] = "revision-followup"
	body := map[string]any{"issue_agent_instance_id": instance.ID, "input": "继续", "expected_issue_version": issue.Version}
	path := "/v1/issues/" + issue.ID + "/runs"
	next := decodeData[domain.Run](t, request(t, router, http.MethodPost, path, body, auth, http.StatusAccepted))
	if next.IssueAgentInstanceID != instance.ID || next.IssueAgentInstanceID == newer.IssueAgentInstanceID ||
		next.IssueAgentGenerationID == old.IssueAgentGenerationID || next.Model != "deepseek/deepseek-flash" || next.AgentServiceID != service.ID {
		t.Fatalf("HTTP follow-up used wrong instance/config/generation: %+v", next)
	}
	_, _, payload := issueRevisionHTTPPayload(t, repo, next.ID)
	if payload.Provider != "opencode" || payload.Model != next.Model || payload.SystemPrompt != "saved follow-up prompt" ||
		len(payload.Skills) != 1 || payload.Skills[0].Slug != "followup-skill" || payload.NativeSessionID != "" {
		t.Fatalf("start payload ignored saved configuration: %+v", payload)
	}
	after, err := repo.GetIssueAgentInstanceByID(t.Context(), user.ID, instance.ID)
	if err != nil || after.AgentRevisionID != current.CurrentRevisionID || after.Version != before.Version+1 {
		t.Fatalf("instance did not atomically advance: %+v err=%v", after, err)
	}
	// The older fixture was admitted internally, with no original HTTP request
	// identity. Do not impersonate it as a replay of a different user message.
	// Its history remains readable and immutable, while a genuine HTTP-admitted
	// request still replays during a busy Run with a stale optimistic version.
	auth["Idempotency-Key"] = "history"
	body["expected_issue_version"] = 999
	unverifiable := request(t, router, http.MethodPost, path, body, auth, http.StatusConflict)
	if unverifiable.Error == nil || unverifiable.Error.Code != "idempotency_conflict" {
		t.Fatalf("internal key must not impersonate HTTP request: %+v", unverifiable.Error)
	}
	historical := decodeData[domain.Run](t, request(t, router, http.MethodGet, "/v1/runs/"+old.ID, nil, auth, http.StatusOK))
	if !reflect.DeepEqual(historical, old) {
		t.Fatalf("historical read changed: %+v", historical)
	}
	auth["Idempotency-Key"] = "revision-followup"
	replayed := decodeData[domain.Run](t, request(t, router, http.MethodPost, path, body, auth, http.StatusOK))
	if !reflect.DeepEqual(replayed, next) {
		t.Fatalf("verified HTTP replay changed: %+v", replayed)
	}
	snapshot, raw, _ := issueRevisionHTTPPayload(t, repo, old.ID)
	if snapshot != oldSnapshot || raw != oldRaw {
		t.Fatal("HTTP follow-up rewrote historical run snapshot or outbox")
	}
}

func TestIssueRunHTTPConfigurationRefreshCannotBypassFences(t *testing.T) {
	for _, mode := range []string{"busy", "paused", "closed", "version_conflict", "goal"} {
		t.Run(mode, func(t *testing.T) {
			repo, user, issue, instance, old := issueHistoryFixture(t)
			if mode != "busy" {
				finishIssueRevisionHTTPRun(t, repo, old)
			}
			expected, code := issue.Version, "target_busy"
			switch mode {
			case "paused":
				current, err := repo.GetIssueAgentInstanceByID(t.Context(), user.ID, instance.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.UpdateIssueAgentInstance(t.Context(), user.ID, current.ID, current.Version,
					func(i *domain.IssueAgentInstance) { i.Status = domain.AgentInstancePaused }); err != nil {
					t.Fatal(err)
				}
				code = "agent_instance_paused"
			case "closed":
				updated, err := repo.UpdateIssue(t.Context(), user.ID, issue.ID, issue.Version,
					func(i *domain.Issue) { i.Status = "closed" })
				if err != nil {
					t.Fatal(err)
				}
				expected, code = updated.Version, "issue_not_open"
			case "version_conflict":
				expected++
				code = "version_conflict"
			case "goal":
				if _, _, _, err := repo.CreateIssueAgentGoalRun(t.Context(), user.ID, issue.ID, instance.ID, issue.Version,
					"frozen-goal", "frozen objective", nil, domain.Run{AgentID: instance.AgentID,
						Input: []domain.ContentBlock{{Type: "text", Text: "goal work"}}}); err != nil {
					t.Fatal(err)
				}
				code = "goal_already_exists"
			}
			agent, err := repo.GetAgent(t.Context(), user.ID, instance.AgentID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.CreateAgentRevision(t.Context(), user.ID, domain.AgentRevision{
				AgentID: agent.ID, AgentServiceID: agent.AgentServiceID, Model: "deepseek/deepseek-flash", SystemPrompt: "saved prompt",
			}, nil); err != nil {
				t.Fatal(err)
			}
			before, err := repo.GetIssueAgentInstanceByID(t.Context(), user.ID, instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			router := newRouterWithOrigins(repo, nil, "test", true, []string{testBrowserOrigin})
			auth := testLoginHeaders(t, repo, router)
			auth["Idempotency-Key"] = "fenced-followup"
			response := request(t, router, http.MethodPost, "/v1/issues/"+issue.ID+"/runs",
				map[string]any{"issue_agent_instance_id": instance.ID, "input": "继续", "expected_issue_version": expected}, auth, http.StatusConflict)
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Recorder.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != code {
				t.Fatalf("wrong fence: %s err=%v", response.Recorder.Body.String(), err)
			}
			after, err := repo.GetIssueAgentInstanceByID(t.Context(), user.ID, instance.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed HTTP follow-up advanced configuration: %+v err=%v", after, err)
			}
		})
	}
}
