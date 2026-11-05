package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

func TestFeatureFlowSQLite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testFeatureFlow(t, newTestRepository(t))
}

func testFeatureFlow(t *testing.T, repository *testRepository) {
	t.Helper()
	if err := store.EnsureBuiltinSkillPackages(t.Context(), repository); err != nil {
		t.Fatalf("seed built-in skills: %v", err)
	}
	router := newRouterWithOrigins(repository, &resourceOpenTestRelay{}, "test", true, []string{testBrowserOrigin})

	auth := testLoginHeaders(t, repository, router)
	req := func(method, path string, body any, headers map[string]string, expectedStatus int) testResponse {
		t.Helper()
		merged := map[string]string{}
		for key, value := range auth {
			merged[key] = value
		}
		for key, value := range headers {
			merged[key] = value
		}
		return request(t, router, method, path, body, merged, expectedStatus)
	}
	computer := createTestComputer(t, repository, auth, "Feature Mac")
	project := decodeData[struct {
		ID string `json:"id"`
	}](t, req(http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computer.ID,
		"name":        "Feature Project",
	}, nil, http.StatusCreated))
	{
		// Fixture for a successful local Provider probe.
		if _, err := repository.UpsertAgentService(t.Context(), testLocalUserID(repository, t), domain.AgentService{
			ComputerID:        computer.ID,
			Provider:          "opencode",
			ProviderVersion:   "test",
			LoginStatus:       "authenticated",
			Status:            "ready",
			Capabilities:      map[string]any{"models": []string{"opencode/model-x"}},
			CapabilityVersion: 1,
		}); err != nil {
			t.Fatalf("provision agent service: %v", err)
		}
	}

	testMemoryFlow(t, router, project.ID, auth)
	testSkillBindingDetailsFlow(t, router, repository, auth)
	testPolicyAndScheduleFlow(t, router, repository, project.ID, auth)
	testCollaborationAndApprovalFlow(t, router, repository, project.ID, auth)
}

func testSkillBindingDetailsFlow(t *testing.T, router http.Handler, repository *testRepository, auth map[string]string) {
	t.Helper()
	req := func(method, path string, body any, expectedStatus int) testResponse {
		t.Helper()
		return request(t, router, method, path, body, auth, expectedStatus)
	}

	services := decodeData[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, req(http.MethodGet, "/v1/computers/"+computerID(t, repository)+"/agent-services", nil, http.StatusOK))
	packages := decodeData[domain.Page[domain.SkillPackage]](t,
		req(http.MethodGet, "/v1/skill-packages", nil, http.StatusOK))
	if len(services.Items) == 0 || len(packages.Items) == 0 {
		t.Fatal("expected an Agent service and built-in Skill package")
	}
	computerID := computerID(t, repository)
	installed := decodeData[domain.ComputerSkillPackage](t, req(http.MethodPost,
		"/v1/computers/"+computerID+"/skill-packages", map[string]any{
			"skill_package_id": packages.Items[0].ID,
		}, http.StatusCreated))
	if installed.SkillPackageID != packages.Items[0].ID || installed.SkillPackageVersionID == "" {
		t.Fatalf("unexpected installed Skill: %+v", installed)
	}
	installedPage := decodeData[domain.Page[domain.ComputerSkillPackage]](t, req(http.MethodGet,
		"/v1/computers/"+computerID+"/skill-packages", nil, http.StatusOK))
	if len(installedPage.Items) != 1 || installedPage.Items[0].ID != installed.ID {
		t.Fatalf("installed Skills = %+v, want installation %s", installedPage.Items, installed.ID)
	}

	agent := decodeData[domain.Agent](t, req(http.MethodPost,
		"/v1/computers/"+computerID+"/agents", map[string]any{
			"agent_service_id":  services.Items[0].ID,
			"name":              "Skill detail test",
			"system_prompt":     "preserve this prompt",
			"skill_version_ids": []string{installed.SkillPackageVersionID},
		}, http.StatusCreated))
	response := decodeData[struct {
		Skills        []domain.AgentRevisionSkillPackage `json:"skills"`
		SkillPackages map[string]domain.SkillPackage     `json:"skill_packages"`
	}](t, req(http.MethodGet, "/v1/agent-revisions/"+agent.CurrentRevisionID, nil, http.StatusOK))
	if len(response.Skills) != 1 {
		t.Fatalf("revision skill count = %d, want 1", len(response.Skills))
	}
	versionID := response.Skills[0].SkillPackageVersionID
	detail, ok := response.SkillPackages[versionID]
	if !ok || detail.ID != packages.Items[0].ID || detail.Description != packages.Items[0].Description {
		t.Fatalf("bound Skill detail mismatch: version=%s detail=%+v", versionID, detail)
	}
	updatedAgent := decodeData[domain.Agent](t, req(http.MethodPatch, "/v1/agents/"+agent.ID, map[string]any{
		"model": "opencode/model-y", "expected_version": agent.Version,
	}, http.StatusOK))
	updatedRevision := decodeData[struct {
		Revision domain.AgentRevision               `json:"revision"`
		Skills   []domain.AgentRevisionSkillPackage `json:"skills"`
	}](t, req(http.MethodGet, "/v1/agent-revisions/"+updatedAgent.CurrentRevisionID, nil, http.StatusOK))
	if updatedRevision.Revision.SystemPrompt != "preserve this prompt" || len(updatedRevision.Skills) != 1 ||
		updatedRevision.Skills[0].SkillPackageVersionID != installed.SkillPackageVersionID {
		t.Fatalf("partial Agent update lost revision data: %+v", updatedRevision)
	}
	blockedUninstall := req(http.MethodDelete, "/v1/computers/"+computerID+"/skill-packages/"+installed.ID+
		"?expected_version="+fmt.Sprint(installed.Version), nil, http.StatusConflict)
	if blockedUninstall.Error == nil || blockedUninstall.Error.Code != "skill_in_use" {
		t.Fatalf("bound Skill uninstall must expose its actionable code: %+v", blockedUninstall.Error)
	}
}

func testMemoryFlow(t *testing.T, router http.Handler, projectID string, auth map[string]string) {
	req := func(method, path string, body any, headers map[string]string, expectedStatus int) testResponse {
		t.Helper()
		merged := map[string]string{}
		for key, value := range auth {
			merged[key] = value
		}
		for key, value := range headers {
			merged[key] = value
		}
		return request(t, router, method, path, body, merged, expectedStatus)
	}
	path := "/v1/projects/" + projectID + "/memories"
	req(http.MethodGet, path, nil, nil, http.StatusOK)
	created := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, path, map[string]any{
		"key": "architecture", "kind": "decision", "title": "架构", "content": "SQLite 是权威",
	}, nil, http.StatusCreated))
	duplicate := req(http.MethodPost, path, map[string]any{
		"key": "architecture", "kind": "decision", "title": "重复", "content": "重复",
	}, nil, http.StatusConflict)
	if duplicate.Error == nil || duplicate.Error.Code != "memory_key_conflict" {
		t.Fatalf("expected memory_key_conflict")
	}
	updated := decodeData[struct {
		Version int64 `json:"version"`
	}](t, req(http.MethodPatch, path+"/"+created.ID, map[string]any{
		"title": "架构权威", "content": "SQLite 是唯一本地权威", "expected_version": created.Version,
	}, nil, http.StatusOK))
	versions := decodeData[struct {
		Items []domain.ProjectMemoryVersion `json:"items"`
	}](t, req(http.MethodGet, path+"/"+created.ID+"/versions", nil, nil, http.StatusOK))
	if len(versions.Items) != 2 {
		t.Fatalf("expected two memory versions, got %d", len(versions.Items))
	}
	search := decodeData[domain.ProjectSearchPage](t, req(
		http.MethodGet, "/v1/projects/"+projectID+"/search?q="+url.QueryEscape("唯一本地权威"),
		nil, nil, http.StatusOK,
	))
	if len(search.Items) != 1 || search.Items[0].Kind != domain.ProjectSearchMemory || search.Items[0].MemoryID != created.ID {
		t.Fatalf("unexpected project search response: %+v", search)
	}
	req(http.MethodPost, path+"/"+created.ID+"/archive", map[string]any{
		"expected_version": updated.Version,
	}, nil, http.StatusNoContent)
	archived := decodeData[struct {
		Version int64 `json:"version"`
	}](t, req(http.MethodGet, path+"/"+created.ID, nil, nil, http.StatusOK))
	req(http.MethodPost, path+"/"+created.ID+"/restore", map[string]any{
		"expected_version": archived.Version,
	}, nil, http.StatusOK)
}

func testPolicyAndScheduleFlow(t *testing.T, router http.Handler, repository *testRepository, projectID string, auth map[string]string) {
	req := func(method, path string, body any, headers map[string]string, expectedStatus int) testResponse {
		t.Helper()
		merged := map[string]string{}
		for key, value := range auth {
			merged[key] = value
		}
		for key, value := range headers {
			merged[key] = value
		}
		return request(t, router, method, path, body, merged, expectedStatus)
	}
	policyPath := "/v1/projects/" + projectID + "/policy-rules"
	policy := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, policyPath, map[string]any{
		"action_kind": "filesystem.write", "effect": "require_approval",
		"conditions": map[string]any{"path_prefix": "src/"},
	}, nil, http.StatusCreated))
	req(http.MethodGet, policyPath, nil, nil, http.StatusOK)
	disabled := false
	updated := decodeData[struct {
		Version int64 `json:"version"`
	}](t, req(http.MethodPatch, "/v1/policy-rules/"+policy.ID, map[string]any{
		"enabled": disabled, "expected_version": policy.Version,
	}, nil, http.StatusOK))
	req(http.MethodDelete, "/v1/policy-rules/"+policy.ID+"?expected_version="+itoa(updated.Version), nil, nil, http.StatusNoContent)

	preview := req(http.MethodPost, "/v1/scheduled-tasks/preview", map[string]any{
		"schedule_kind": "cron", "schedule_expression": "0 9 * * 1-5",
		"timezone": "Asia/Shanghai", "count": 3,
	}, nil, http.StatusOK)
	previewData := decodeData[struct {
		Times []time.Time `json:"times"`
	}](t, preview)
	if len(previewData.Times) != 3 {
		t.Fatalf("expected three preview times")
	}
	// 先建 Agent（供计划任务选择）。
	computer := computerID(t, repository)
	services := decodeData[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, req(http.MethodGet, "/v1/computers/"+computer+"/agent-services", nil, nil, http.StatusOK))
	if len(services.Items) == 0 {
		t.Fatal("expected fixture agent service for schedule flow")
	}
	scheduleAgent := decodeData[struct {
		ID string `json:"id"`
	}](t, req(http.MethodPost, "/v1/computers/"+computer+"/agents", map[string]any{
		"agent_service_id": services.Items[0].ID, "name": "计划任务 Agent",
	}, nil, http.StatusCreated))

	task := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/projects/"+projectID+"/scheduled-tasks", map[string]any{
		"name": "工作日检查", "schedule_kind": "cron", "schedule_expression": "0 9 * * 1-5",
		"timezone": "Asia/Shanghai", "prompt": []map[string]any{{"type": "text", "text": "检查项目"}},
		"agent_id": scheduleAgent.ID,
	}, nil, http.StatusCreated))
	req(http.MethodGet, "/v1/projects/"+projectID+"/scheduled-tasks", nil, nil, http.StatusOK)
	req(http.MethodGet, "/v1/scheduled-tasks/"+task.ID, nil, nil, http.StatusOK)
	task = decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPatch, "/v1/scheduled-tasks/"+task.ID, map[string]any{
		"name": "工作日检查（更新）", "schedule_kind": "cron", "schedule_expression": "5 9 * * 1-5",
		"timezone": "Asia/Shanghai", "prompt": []map[string]any{{"type": "text", "text": "检查项目"}},
		"agent_id":           scheduleAgent.ID,
		"concurrency_policy": "forbid", "misfire_policy": "skip", "expected_version": task.Version,
	}, nil, http.StatusOK))
	task = decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/pause", map[string]any{
		"expected_version": task.Version,
	}, nil, http.StatusOK))
	task = decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/resume", map[string]any{
		"expected_version": task.Version,
	}, nil, http.StatusOK))
	runPath := "/v1/scheduled-tasks/" + task.ID + "/run-now"
	first := req(http.MethodPost, runPath, map[string]any{}, map[string]string{"Idempotency-Key": "manual-1"}, http.StatusAccepted)
	second := req(http.MethodPost, runPath, map[string]any{}, map[string]string{"Idempotency-Key": "manual-1"}, http.StatusOK)
	if first.Meta.Replayed || !second.Meta.Replayed {
		t.Fatalf("scheduled run idempotency metadata is incorrect")
	}
	req(http.MethodGet, "/v1/scheduled-tasks/"+task.ID+"/runs", nil, nil, http.StatusOK)
}

func testCollaborationAndApprovalFlow(t *testing.T, router http.Handler, repository *testRepository, projectID string, auth map[string]string) {
	req := func(method, path string, body any, headers map[string]string, expectedStatus int) testResponse {
		t.Helper()
		merged := map[string]string{}
		for key, value := range auth {
			merged[key] = value
		}
		for key, value := range headers {
			merged[key] = value
		}
		return request(t, router, method, path, body, merged, expectedStatus)
	}

	// 创建 Issue。
	issue := decodeData[testIssue](t, req(http.MethodPost, "/v1/projects/"+projectID+"/issues", map[string]any{
		"title": "测试任务", "description": "任务描述", "worktree_hint": false,
	}, nil, http.StatusCreated))
	req(http.MethodGet, "/v1/issues/"+issue.ID, nil, nil, http.StatusOK)
	req(http.MethodGet, "/v1/projects/"+projectID+"/issues", nil, nil, http.StatusOK)
	req(http.MethodGet, "/v1/issues/"+issue.ID+"/comments", nil, nil, http.StatusOK)
	req(http.MethodPatch, "/v1/issues/"+issue.ID, map[string]any{
		"status": "completed", "expected_version": issue.Version,
	}, nil, http.StatusBadRequest)
	updated := decodeData[struct {
		Version int64 `json:"version"`
	}](t, req(http.MethodPatch, "/v1/issues/"+issue.ID, map[string]any{
		"status": "resolved", "expected_version": issue.Version,
	}, nil, http.StatusOK))
	issue.Version = updated.Version

	// 人类评论（无 @）。
	comment := decodeData[struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}](t, req(http.MethodPost, "/v1/issues/"+issue.ID+"/comments", map[string]any{
		"body": "人类评论",
	}, nil, http.StatusCreated))
	req(http.MethodGet, "/v1/issues/"+issue.ID+"/agent-instances", nil, nil, http.StatusOK)

	// 评论修订。
	edited := decodeData[struct {
		Version int64 `json:"version"`
	}](t, req(http.MethodPatch, "/v1/comments/"+comment.ID, map[string]any{
		"body": "人类评论（已修正）", "expected_version": comment.Version,
	}, nil, http.StatusOK))
	req(http.MethodGet, "/v1/comments/"+comment.ID+"/revisions", nil, nil, http.StatusOK)

	// 删除评论：用户评论可以删除，版本不匹配时拒绝。
	req(http.MethodDelete, "/v1/comments/"+comment.ID+"?expected_version="+itoa(comment.Version), nil, nil, http.StatusConflict)
	req(http.MethodDelete, "/v1/comments/"+comment.ID+"?expected_version="+itoa(edited.Version), nil, nil, http.StatusNoContent)
	req(http.MethodDelete, "/v1/comments/"+comment.ID+"?expected_version="+itoa(edited.Version), nil, nil, http.StatusNotFound)
	listed := decodeData[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, req(http.MethodGet, "/v1/issues/"+issue.ID+"/comments", nil, nil, http.StatusOK))
	for _, item := range listed.Items {
		if item.ID == comment.ID {
			t.Fatal("deleted comment still listed")
		}
	}

	// Agent 评论（无对应实例应拒绝）。
	req(http.MethodPost, "/v1/issues/"+issue.ID+"/comments", map[string]any{
		"body": "幽灵 Agent 评论", "author_kind": "agent", "author_agent_id": "agt_missing",
	}, nil, http.StatusBadRequest)

	// approval 仍走 Run 关联：先建 Agent 并激活一个 run。
	agentService := decodeData[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, req(http.MethodGet, "/v1/computers/"+computerID(t, repository)+"/agent-services", nil, nil, http.StatusOK))
	if len(agentService.Items) == 0 {
		t.Fatal("expected fixture agent service")
	}
	agent := decodeData[struct {
		ID string `json:"id"`
	}](t, req(http.MethodPost, "/v1/computers/"+computerID(t, repository)+"/agents", map[string]any{
		"agent_service_id": agentService.Items[0].ID, "name": "审批测试",
	}, nil, http.StatusCreated))
	instance, _, err := repository.EnsureIssueAgentInstance(t.Context(), testLocalUserID(repository, t), issue.ID, agent.ID)
	if err != nil {
		t.Fatalf("create approval test session: %v", err)
	}

	// User HTTP cannot impersonate even a valid Agent with an active instance.
	req(http.MethodPost, "/v1/issues/"+issue.ID+"/comments", map[string]any{
		"body": "Agent 补全", "author_kind": "agent", "author_agent_id": agent.ID,
	}, nil, http.StatusBadRequest)
	// Seed the trusted Agent-side store path to verify user edit/delete policy.
	agentComment, err := repository.CreateIssueComment(t.Context(), testLocalUserID(repository, t), domain.IssueComment{
		IssueID: issue.ID, Body: "Agent 补全", AuthorKind: domain.IssueCommentAuthorAgent, AuthorAgentID: agent.ID,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rejected := req(http.MethodPatch, "/v1/comments/"+agentComment.ID, map[string]any{
		"body": "篡改 Agent 评论", "expected_version": agentComment.Version,
	}, nil, http.StatusForbidden)
	if rejected.Error == nil || rejected.Error.Code != "not_own_comment" {
		t.Fatalf("Agent comment edit was not rejected: %+v", rejected)
	}
	req(http.MethodDelete, "/v1/comments/"+agentComment.ID+"?expected_version="+itoa(agentComment.Version), nil, nil, http.StatusNoContent)
	// Status CRUD above intentionally resolved this Issue. Reopen it explicitly
	// before the approval-flow fixture starts execution; non-open Issues cannot
	// admit new Runs under the execution emergency-stop boundary.
	reopened := decodeData[struct {
		Version int64 `json:"version"`
	}](t, req(http.MethodPatch, "/v1/issues/"+issue.ID, map[string]any{
		"status": "open", "expected_version": issue.Version,
	}, nil, http.StatusOK))
	issue.Version = reopened.Version
	activation := decodeData[struct {
		ID    string                `json:"id"`
		Input []domain.ContentBlock `json:"input"`
	}](t, req(http.MethodPost, "/v1/issues/"+issue.ID+"/runs", map[string]any{
		"issue_agent_instance_id": instance.ID, "input": "干活",
		"attachments": []map[string]any{{
			"type": "image", "mime_type": "image/png", "filename": "capture.png",
			"data_url": "data:image/png;base64,iVBORw0KGgo=",
		}},
		"expected_issue_version": issue.Version,
	}, map[string]string{"Idempotency-Key": "feature-approval-run"}, http.StatusAccepted))
	if len(activation.Input) != 2 || activation.Input[1].Filename != "capture.png" || activation.Input[1].DataURL != "" {
		t.Fatalf("run response leaked or lost attachment metadata: %+v", activation.Input)
	}

	approval, err := repository.CreateApproval(t.Context(), domain.Approval{
		ProjectID: projectID, IssueID: issue.ID, RunID: activation.ID,
		ActionKind: "filesystem.write", ActionDigest: "sha256:test",
		NormalizedAction: map[string]any{"kind": "filesystem.write", "target": "src/main.go"},
		Risk:             "medium", ExpiresAt: time.Now().UTC().Add(time.Hour),
		ProviderApprovalRef: "provider-approval-test",
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}
	req(http.MethodGet, "/v1/approvals?status=pending", nil, nil, http.StatusOK)
	req(http.MethodGet, "/v1/approvals/"+approval.ID, nil, nil, http.StatusOK)
	wrong := req(http.MethodPost, "/v1/approvals/"+approval.ID+"/decisions", map[string]any{
		"expected_version": approval.Version, "action_digest": "sha256:changed", "decision": "approved",
	}, nil, http.StatusConflict)
	if wrong.Error == nil || wrong.Error.Code != "approval_action_changed" {
		t.Fatal("changed approval action must be rejected")
	}
	req(http.MethodPost, "/v1/approvals/"+approval.ID+"/decisions", map[string]any{
		"expected_version": approval.Version, "action_digest": approval.ActionDigest,
		"decision": "approved", "scope": "once",
	}, nil, http.StatusOK)
}

type testIssue struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

func itoa(value int64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = digits[value%10]
		value /= 10
	}
	return string(buffer[position:])
}

// 空标题允许创建：任务可以先作为占位存在，再通过点击标题补全；描述创建后只读。
func TestCreateIssueAllowsEmptyTitle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	if err := store.EnsureBuiltinSkillPackages(t.Context(), repository); err != nil {
		t.Fatalf("seed built-in skills: %v", err)
	}
	router := newRouterWithOrigins(repository, &resourceOpenTestRelay{}, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "Untitled Mac")
	project := decodeData[struct {
		ID string `json:"id"`
	}](t, request(t, router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computer.ID, "name": "Untitled Project",
	}, auth, http.StatusCreated))
	type issueRecord struct {
		ID          string `json:"id"`
		Version     int64  `json:"version"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	issue := decodeData[issueRecord](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/issues", map[string]any{
		"title": "", "description": "",
	}, auth, http.StatusCreated))
	if issue.Title != "新任务" || issue.Description != "" {
		t.Fatalf("untitled issue = %+v", issue)
	}
	updated := decodeData[issueRecord](t, request(t, router, http.MethodPatch, "/v1/issues/"+issue.ID, map[string]any{
		"title": "补上标题", "expected_version": issue.Version,
	}, auth, http.StatusOK))
	if updated.Title != "补上标题" || updated.Description != "" || updated.Version != issue.Version+1 {
		t.Fatalf("updated issue = %+v", updated)
	}
}

func TestIssueDescriptionIsReadOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newTestRepository(t)
	router := newRouterWithOrigins(repository, nil, "test", true, []string{testBrowserOrigin})
	auth := testLoginHeaders(t, repository, router)
	computer := createTestComputer(t, repository, auth, "Read-only Description Mac")
	project := decodeData[domain.Project](t, request(t, router, http.MethodPost, "/v1/projects", map[string]any{
		"computer_id": computer.ID, "name": "Read-only Description Project",
	}, auth, http.StatusCreated))
	issue := decodeData[domain.Issue](t, request(t, router, http.MethodPost, "/v1/projects/"+project.ID+"/issues", map[string]any{
		"title": "原始标题", "description": "创建时的描述", "worktree_hint": false,
	}, auth, http.StatusCreated))
	if issue.Description != "创建时的描述" {
		t.Fatalf("created issue lost description: %+v", issue)
	}
	path := "/v1/issues/" + issue.ID
	assertUnchanged := func(t *testing.T, want domain.Issue) {
		t.Helper()
		got := decodeData[domain.Issue](t, request(t, router, http.MethodGet, path, nil, auth, http.StatusOK))
		if got != want {
			t.Fatalf("persisted issue = %+v, want unchanged %+v", got, want)
		}
	}
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "replacement", value: "不允许替换描述"},
		{name: "same_value", value: issue.Description},
		{name: "empty", value: ""},
		{name: "null", value: nil},
		{name: "object", value: map[string]any{"text": "不允许替换描述"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected := request(t, router, http.MethodPatch, path, map[string]any{
				"description": test.value, "title": "不能部分保存的标题", "status": "closed",
				"worktree_hint": true, "expected_version": issue.Version,
			}, auth, http.StatusBadRequest)
			if rejected.Error == nil || rejected.Error.Code != "invalid_argument" {
				t.Fatalf("description update was not rejected: %+v", rejected.Error)
			}
			assertUnchanged(t, issue)
		})
	}

	updated := decodeData[domain.Issue](t, request(t, router, http.MethodPatch, path, map[string]any{
		"title": "  点击标题修改  ", "expected_version": issue.Version,
	}, auth, http.StatusOK))
	if updated.Title != "点击标题修改" || updated.Description != issue.Description || updated.Version != issue.Version+1 ||
		updated.Status != issue.Status || updated.WorktreeHint != issue.WorktreeHint {
		t.Fatalf("title-only update = %+v", updated)
	}
	assertUnchanged(t, updated)
	conflict := request(t, router, http.MethodPatch, path, map[string]any{
		"title": "过期标题", "expected_version": issue.Version,
	}, auth, http.StatusConflict)
	if conflict.Error == nil || conflict.Error.Code != "version_conflict" {
		t.Fatalf("stale update = %+v, want version_conflict", conflict.Error)
	}
	assertUnchanged(t, updated)

	for _, status := range []string{domain.IssueResolved, domain.IssueClosed, domain.IssueOpen} {
		previous := updated
		updated = decodeData[domain.Issue](t, request(t, router, http.MethodPatch, path, map[string]any{
			"status": status, "worktree_hint": !previous.WorktreeHint, "expected_version": previous.Version,
		}, auth, http.StatusOK))
		if updated.Status != status || updated.WorktreeHint == previous.WorktreeHint || updated.Title != previous.Title ||
			updated.Description != issue.Description || updated.Version != previous.Version+1 {
			t.Fatalf("status/worktree update = %+v", updated)
		}
		assertUnchanged(t, updated)
	}
}
