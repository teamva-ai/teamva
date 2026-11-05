package httpapi

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func httpTitleIntentMetadata(t *testing.T, repo *testRepository, id string) map[string]string {
	t.Helper()
	rows, err := repo.db.QueryContext(t.Context(), `SELECT key,value FROM app_metadata`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(key, "scheduled_task_config:") && (strings.Contains(key, id) || strings.Contains(value, id)) {
			result[key] = value
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHTTPTitleIntentDefaultsOptionalPatchAndSameValueFence(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	issue := scheduleSurfaceData[domain.Issue](t, f.call(http.MethodPost, "/v1/projects/"+f.project.ID+"/issues",
		map[string]any{"description": "real issue title seed"}, "", http.StatusCreated, "omitted title creates an automatic Issue placeholder"))
	if issue.Title != "新任务" {
		t.Fatal(issue)
	}
	prior := httpTitleIntentMetadata(t, f.repo, issue.ID)
	issue = scheduleSurfaceData[domain.Issue](t, f.call(http.MethodPatch, "/v1/issues/"+issue.ID,
		map[string]any{"expected_version": issue.Version, "worktree_hint": true}, "", http.StatusOK, "geometry/status-only Issue PATCH preserves automatic title intent"))
	if !reflect.DeepEqual(prior, httpTitleIntentMetadata(t, f.repo, issue.ID)) {
		t.Fatal("non-title Issue PATCH marked title manual")
	}
	f.call(http.MethodPatch, "/v1/issues/"+issue.ID, map[string]any{"expected_version": issue.Version, "title": issue.Title}, "", http.StatusOK,
		"same-value title PATCH explicitly fences delayed title results")
	if reflect.DeepEqual(prior, httpTitleIntentMetadata(t, f.repo, issue.ID)) {
		t.Fatal("Issue handler dropped same-value title intent")
	}

	memoryPath := "/v1/projects/" + f.project.ID + "/memories"
	memory := scheduleSurfaceData[domain.ProjectMemoryEntry](t, f.call(http.MethodPost, memoryPath,
		map[string]any{"key": "optional-title", "kind": "fact", "content": "real memory seed"}, "", http.StatusCreated, "memory creation no longer requires a title"))
	if memory.Title != "新记忆" {
		t.Fatal(memory)
	}
	prior = httpTitleIntentMetadata(t, f.repo, memory.ID)
	memory = scheduleSurfaceData[domain.ProjectMemoryEntry](t, f.call(http.MethodPatch, memoryPath+"/"+memory.ID,
		map[string]any{"expected_version": memory.Version, "content": "updated body without title"}, "", http.StatusOK, "body-only memory PATCH retains stored title without disabling generation"))
	if memory.Title != "新记忆" || !reflect.DeepEqual(prior, httpTitleIntentMetadata(t, f.repo, memory.ID)) {
		t.Fatal("body-only memory PATCH changed the title/intent")
	}
	f.call(http.MethodPatch, memoryPath+"/"+memory.ID, map[string]any{"expected_version": memory.Version, "content": memory.Content, "title": memory.Title}, "", http.StatusOK,
		"same-value memory title still counts as a manual edit")
	if reflect.DeepEqual(prior, httpTitleIntentMetadata(t, f.repo, memory.ID)) {
		t.Fatal("memory handler dropped same-value title intent")
	}

	note := scheduleSurfaceData[domain.Inspiration](t, f.call(http.MethodPost, "/v1/inspirations", map[string]any{}, "title-intent-note", http.StatusCreated,
		"empty inspiration initializes an automatic placeholder"))
	if note.Title != "新灵感" {
		t.Fatal(note)
	}
	note = scheduleSurfaceData[domain.Inspiration](t, f.call(http.MethodPatch, "/v1/inspirations/"+note.ID,
		map[string]any{"expected_version": note.Version, "markdown": "first inspiration body"}, "", http.StatusOK, "first saved Markdown queues the title"))
	prior = httpTitleIntentMetadata(t, f.repo, note.ID)
	note = scheduleSurfaceData[domain.Inspiration](t, f.call(http.MethodPatch, "/v1/inspirations/"+note.ID,
		map[string]any{"expected_version": note.Version, "completed": true}, "", http.StatusOK, "completion does not mark inspiration title manual"))
	if !reflect.DeepEqual(prior, httpTitleIntentMetadata(t, f.repo, note.ID)) {
		t.Fatal("inspiration completion changed title intent")
	}
	f.call(http.MethodPatch, "/v1/inspirations/"+note.ID, map[string]any{"expected_version": note.Version, "title": note.Title}, "", http.StatusOK,
		"same-value inspiration title commits manual intent even without a row change")
	if reflect.DeepEqual(prior, httpTitleIntentMetadata(t, f.repo, note.ID)) {
		t.Fatal("same-value inspiration intent rolled back")
	}

	input := f.input("")
	delete(input, "name")
	task := f.createTask(input)
	if task.Name != "新定时任务" {
		t.Fatal(task)
	}
	prior = httpTitleIntentMetadata(t, f.repo, task.ID)
	input["expected_version"] = task.Version
	task = scheduleSurfaceData[domain.ScheduledTask](t, f.call(http.MethodPatch, "/v1/scheduled-tasks/"+task.ID, input, "", http.StatusOK,
		"schedule PATCH may omit name while retaining other existing validation requirements"))
	if task.Name != "新定时任务" || !reflect.DeepEqual(prior, httpTitleIntentMetadata(t, f.repo, task.ID)) {
		t.Fatal("schedule PATCH without name disabled automatic title")
	}
	input["expected_version"], input["name"] = task.Version, task.Name
	f.call(http.MethodPatch, "/v1/scheduled-tasks/"+task.ID, input, "", http.StatusOK, "same-value schedule name is an explicit manual edit")
	if reflect.DeepEqual(prior, httpTitleIntentMetadata(t, f.repo, task.ID)) {
		t.Fatal("schedule handler dropped same-value name intent")
	}
	input["expected_version"], input["name"] = task.Version+1, " \n "
	f.call(http.MethodPatch, "/v1/scheduled-tasks/"+task.ID, input, "", http.StatusBadRequest, "an explicitly blank schedule PATCH name remains invalid")
}
