package httpapi

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSchedulePromptAsyncDelegationContractTCP(t *testing.T) {
	for _, mode := range []string{"new", "existing"} {
		t.Run(mode, func(t *testing.T) {
			f := newScheduleSurfaceFixture(t)
			input := f.input("async delegation prompt contract")
			input["issue_mode"] = mode
			if mode == "existing" {
				issue := f.createIssue(f.project.ID, "async existing Issue")
				input["issue_id"] = issue.ID
			}
			task := f.createTask(input)
			trigger := scheduleSurfaceData[domain.ScheduledTaskRun](t, f.call(http.MethodPost,
				"/v1/scheduled-tasks/"+task.ID+"/run-now", nil, "async-prompt-contract", http.StatusAccepted,
				"persist the actual scheduled Issue prompt without starting a Provider or model"))
			run, err := f.repo.GetRun(t.Context(), f.user.ID, trigger.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var prompt strings.Builder
			for _, block := range run.Input {
				prompt.WriteString(block.Text)
			}
			assertIssueAsyncDelegationPrompt(t, prompt.String())
			for _, required := range []string{"first instruction", "second instruction", "当前工作回复"} {
				if !strings.Contains(prompt.String(), required) {
					t.Errorf("scheduled prompt lost %q", required)
				}
			}
		})
	}
}

func TestSchedulePromptContractRejectsMissingAndInvalidBlocksTCP(t *testing.T) {
	text := func(value any) map[string]any { return map[string]any{"type": "text", "text": value} }
	work := text("work")
	for _, test := range []struct {
		name   string
		prompt any
	}{
		{name: "first block missing text masked by valid tail", prompt: []any{map[string]any{"type": "text"}, work}},
		{name: "middle block missing text", prompt: []any{work, map[string]any{"type": "text"}, work}},
		{name: "last block missing text", prompt: []any{work, map[string]any{"type": "text"}}},
		{name: "first block null text", prompt: []any{text(nil), work}},
		{name: "last block null text", prompt: []any{work, text(nil)}},
		{name: "numeric text", prompt: []any{work, text(1)}},
		{name: "boolean text", prompt: []any{text(false), work}},
		{name: "array text", prompt: []any{work, text([]string{})}},
		{name: "object text", prompt: []any{text(map[string]any{}), work}},
		{name: "missing type", prompt: []any{map[string]any{"text": "instruction"}, work}},
		{name: "null type", prompt: []any{work, map[string]any{"type": nil, "text": "instruction"}}},
		{name: "numeric type", prompt: []any{map[string]any{"type": 1, "text": "instruction"}, work}},
		{name: "empty type", prompt: []any{work, map[string]any{"type": "", "text": "instruction"}}},
		{name: "image type", prompt: []any{map[string]any{"type": "image", "text": "instruction"}, work}},
		{name: "null block", prompt: []any{work, nil}},
		{name: "scalar block", prompt: []any{"instruction", work}},
		{name: "unknown block field", prompt: []any{map[string]any{"type": "text", "text": "instruction", "unexpected": false}, work}},
		{name: "null internal", prompt: []any{map[string]any{"type": "text", "text": "instruction", "internal": nil}, work}},
		{name: "string internal", prompt: []any{work, map[string]any{"type": "text", "text": "instruction", "internal": "true"}}},
		{name: "numeric internal", prompt: []any{map[string]any{"type": "text", "text": "instruction", "internal": 1}, work}},
		{name: "empty image mime field", prompt: []any{map[string]any{"type": "text", "text": "instruction", "mime_type": ""}, work}},
		{name: "image mime field", prompt: []any{map[string]any{"type": "text", "text": "instruction", "mime_type": "image/png"}, work}},
		{name: "empty image filename field", prompt: []any{work, map[string]any{"type": "text", "text": "instruction", "filename": ""}}},
		{name: "empty image data field", prompt: []any{map[string]any{"type": "text", "text": "instruction", "data_url": ""}, work}},
		{name: "embedded image data remains forbidden", prompt: []any{text("data:image/png;base64,aGVsbG8="), work}},
		{name: "all explicit empty texts", prompt: []any{text(""), text("")}},
		{name: "all whitespace texts", prompt: []any{text(" \t"), text("\n ")}},
	} {
		for _, operation := range []string{"create", "patch"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				f := newScheduleSurfaceFixture(t)
				current := f.createTask(f.input("R8 unchanged task"))
				beforeTasks, beforeIssues, beforeHistory := f.tasks(f.project.ID), f.issues(f.project.ID), f.history(current.ID)
				body := f.input("R8 must not partially persist")
				body["prompt"] = test.prompt
				method, path := http.MethodPost, "/v1/projects/"+f.project.ID+"/scheduled-tasks"
				if operation == "patch" {
					method, path, body["expected_version"] = http.MethodPatch, "/v1/scheduled-tasks/"+current.ID, current.Version
				}
				scheduleSurfaceError(t, f.call(method, path, body, "", http.StatusBadRequest,
					"R8 each prompt element must independently provide string type/text and no extra fields; reject "+test.name+" before any write"), "invalid_argument")
				f.assertTaskUnchanged(current)
				if !reflect.DeepEqual(beforeTasks, f.tasks(f.project.ID)) || !reflect.DeepEqual(beforeIssues, f.issues(f.project.ID)) || !reflect.DeepEqual(beforeHistory, f.history(current.ID)) {
					t.Fatal("invalid prompt partially wrote a task, version, profile, Issue, or trigger")
				}
			})
		}
	}
}

func TestSchedulePromptContractInternalBooleanRoundTripTCP(t *testing.T) {
	for _, test := range []struct {
		name  string
		value bool
	}{
		{name: "true", value: true},
		{name: "false", value: false},
	} {
		for _, operation := range []string{"create", "patch"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				f := newScheduleSurfaceFixture(t)
				body := f.input("R8 internal boolean is part of the public text block")
				body["prompt"] = []map[string]any{
					{"type": "text", "text": "", "internal": test.value},
					{"type": "text", "text": "work", "internal": !test.value},
				}
				want := []domain.ContentBlock{
					{Type: "text", Text: "", Internal: test.value},
					{Type: "text", Text: "work", Internal: !test.value},
				}
				method, path, status, version := http.MethodPost, "/v1/projects/"+f.project.ID+"/scheduled-tasks", http.StatusCreated, int64(1)
				if operation == "patch" {
					current := f.createTask(f.input("R8 existing task before internal boolean edit"))
					body["expected_version"] = current.Version
					method, path, status, version = http.MethodPatch, "/v1/scheduled-tasks/"+current.ID, http.StatusOK, current.Version+1
				}
				reply := f.call(method, path, body, "", status,
					"R8 corrective: TextContentBlock allows internal true/false; preserve each original boolean and explicit empty text on "+operation)
				task := scheduleSurfaceData[domain.ScheduledTask](t, reply)
				if task.Version != version || reply.Meta.ResourceVersion != version || !reflect.DeepEqual(task.Prompt, want) {
					t.Fatalf("internal boolean/empty text round trip mismatch: task=%+v want=%+v", task, want)
				}
				f.assertTaskUnchanged(task)
				items := f.tasks(f.project.ID)
				if len(items) != 1 || !reflect.DeepEqual(items[0].Prompt, want) || len(f.history(task.ID)) != 0 || len(f.issues(f.project.ID)) != 0 {
					t.Fatal("internal boolean metadata round trip lost flags or created duplicate/execution records")
				}
			})
		}
	}
}

func TestSchedulePromptContractExplicitEmptyBlockRemainsValidTCP(t *testing.T) {
	for _, test := range []struct {
		name  string
		texts []string
	}{
		{name: "explicit empty first block", texts: []string{"", "work"}},
		{name: "explicit empty middle block", texts: []string{"work", "", "more work"}},
		{name: "explicit empty last block", texts: []string{"work", ""}},
		{name: "whitespace block and genuine instruction", texts: []string{" \t\n", "work"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newScheduleSurfaceFixture(t)
			// Maps intentionally preserve explicit text:"" on the wire. Using
			// domain.ContentBlock here would omit its empty string during marshal
			// and would test missing text instead of this legal empty value.
			prompt := make([]map[string]any, len(test.texts))
			want := make([]domain.ContentBlock, len(test.texts))
			for index, value := range test.texts {
				prompt[index] = map[string]any{"type": "text", "text": value}
				want[index] = domain.ContentBlock{Type: "text", Text: value}
			}
			body := f.input("R8 explicit empty text is valid")
			body["prompt"] = prompt
			created := f.createTask(body)
			if !reflect.DeepEqual(created.Prompt, want) {
				t.Fatalf("create lost explicit empty block/order: got=%+v want=%+v", created.Prompt, want)
			}
			f.assertTaskUnchanged(created)
			body["expected_version"], body["name"] = created.Version, "R8 explicit empty text remains valid on edit"
			reply := f.call(http.MethodPatch, "/v1/scheduled-tasks/"+created.ID, body, "", http.StatusOK,
				"R8 explicit empty per-block text remains valid when total prompt has a nonblank instruction; preserve every block and increment version exactly once")
			updated := scheduleSurfaceData[domain.ScheduledTask](t, reply)
			if updated.Version != created.Version+1 || reply.Meta.ResourceVersion != updated.Version || !reflect.DeepEqual(updated.Prompt, want) {
				t.Fatalf("PATCH changed legal empty prompt semantics: got=%+v want=%+v", updated, want)
			}
			f.assertTaskUnchanged(updated)
			if len(f.tasks(f.project.ID)) != 1 || len(f.history(created.ID)) != 0 || len(f.issues(f.project.ID)) != 0 {
				t.Fatal("valid metadata prompt request created duplicate tasks or execution side effects")
			}
		})
	}
}
