package httpapi

import (
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestScheduleSurfaceCalendarsRoundTripTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	if len(f.tasks(f.project.ID)) != 0 {
		t.Fatal("new project unexpectedly has schedules")
	}
	issue := f.createIssue(f.project.ID, "existing calendar target")
	for _, test := range []struct {
		name       string
		kind       string
		expression string
		zone       string
		times      []string
		days       []int
		existing   bool
	}{
		{name: "daily pairs and duplicates", kind: "daily", zone: "Asia/Shanghai", times: []string{"17:45", "08:15", "17:45"}},
		{name: "weekly weekdays and duplicates", kind: "weekly", zone: "America/New_York", times: []string{"17:45", "08:15", "08:15"}, days: []int{5, 0, 1, 5}, existing: true},
		{name: "once RFC3339 offset", kind: "once", expression: "2040-06-01T12:34:56+08:00", zone: "Asia/Shanghai"},
		{name: "cron IANA", kind: "cron", expression: "0 12 1 1 *", zone: "Europe/Berlin", existing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := f.withT(t)
			input := f.input(test.name)
			input["schedule_kind"], input["schedule_expression"], input["timezone"] = test.kind, test.expression, test.zone
			if test.times != nil {
				input["times"] = test.times
			}
			if test.days != nil {
				input["weekdays"] = test.days
			}
			if test.existing {
				input["issue_mode"], input["issue_id"] = "existing", issue.ID
				input["concurrency_policy"], input["misfire_policy"] = "allow", "run_once"
			}
			task := f.createTask(input)
			if task.ProjectID != f.project.ID || task.AgentID != f.agent.ID || task.Name != test.name || task.ScheduleKind != test.kind || task.Timezone != test.zone {
				t.Fatalf("calendar/identity round trip mismatch: %+v", task)
			}
			mode, concurrency, misfire := "new", "forbid", "skip"
			if test.existing {
				mode, concurrency, misfire = "existing", "allow", "run_once"
				if task.IssueID != issue.ID {
					t.Fatal("existing target was not retained")
				}
			}
			if task.IssueMode != mode || task.ConcurrencyPolicy != concurrency || task.MisfirePolicy != misfire {
				t.Fatalf("defaults/explicit policies lost: %+v", task)
			}
			location, err := time.LoadLocation(test.zone)
			if err != nil {
				t.Fatal(err)
			}
			local := task.NextRunAt.In(location)
			switch test.kind {
			case "daily", "weekly":
				if !reflect.DeepEqual(task.Times, []string{"08:15", "17:45"}) || task.ScheduleExpression != "" {
					t.Fatalf("times must be deduplicated full HH:mm pairs: %+v", task)
				}
				if local.Format("15:04") != "08:15" && local.Format("15:04") != "17:45" {
					t.Fatalf("calendar formed an hour/minute cross product: %v", local)
				}
				if test.kind == "weekly" && (!reflect.DeepEqual(task.Weekdays, []int{0, 1, 5}) || (local.Weekday() != time.Sunday && local.Weekday() != time.Monday && local.Weekday() != time.Friday)) {
					t.Fatalf("weekly normalized days/next day mismatch: %+v next=%v", task, local)
				}
			case "once":
				if !task.NextRunAt.Equal(scheduleSurfaceTime(t, "2040-06-01T04:34:56Z")) || task.NextRunAt.Location() != time.UTC {
					t.Fatalf("once offset was not normalized to UTC: %v", task.NextRunAt)
				}
			case "cron":
				if task.ScheduleExpression != test.expression || local.Month() != time.January || local.Day() != 1 || local.Hour() != 12 || local.Minute() != 0 {
					t.Fatalf("cron lost its IANA calendar: %+v next=%v", task, local)
				}
			}
			if got := f.task(task.ID); !reflect.DeepEqual(got, task) {
				t.Fatalf("GET not equal to committed create response: %+v %+v", got, task)
			}
			if len(f.history(task.ID)) != 0 {
				t.Fatal("metadata creation must not execute a trigger")
			}
		})
	}
	if len(f.tasks(f.project.ID)) != 4 || len(f.issues(f.project.ID)) != 1 || len(f.comments(issue.ID)) != 0 {
		t.Fatal("calendar metadata writes created execution side effects")
	}
	other := f.createProject("schedule list isolation")
	if len(f.tasks(other.ID)) != 0 {
		t.Fatal("project list leaked schedules from another owned project")
	}
}

func TestScheduleSurfacePersistedDSTCalendarTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	input := f.input("DST full minute pairs")
	input["schedule_kind"], input["schedule_expression"], input["timezone"], input["times"] = "daily", "", "America/New_York", []string{"02:30", "01:30"}
	task := f.createTask(input)
	stored := f.task(task.ID)
	// HTTP handlers use wall time. These deterministic 2040 cursors deliberately
	// test the shared calculator on the HTTP-persisted profile, not a falsely
	// claimed frozen HTTP preview or actual scheduler execution.
	for _, test := range []struct{ name, after, want string }{
		{"spring nonexistent 02:30 is skipped", "2040-03-11T06:31:00Z", "2040-03-12T05:30:00Z"},
		{"fall first 01:30", "2040-11-04T05:29:00Z", "2040-11-04T05:30:00Z"},
		{"fall repeated 01:30", "2040-11-04T05:30:00Z", "2040-11-04T06:30:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, err := domain.NextScheduledTaskTime(stored, scheduleSurfaceTime(t, test.after))
			if err != nil || !next.Equal(scheduleSurfaceTime(t, test.want)) {
				t.Fatalf("persisted DST calendar next=%v want=%s err=%v", next, test.want, err)
			}
		})
	}
	if len(f.history(task.ID)) != 0 || len(f.issues(f.project.ID)) != 0 {
		t.Fatal("DST metadata/calendar verification executed work")
	}
}

func TestScheduleSurfaceUpdatePauseResumeVersionTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	issue := f.createIssue(f.project.ID, "preserved existing target")
	input := f.input("weekly existing")
	input["schedule_kind"], input["schedule_expression"], input["times"], input["weekdays"] = "weekly", "", []string{"17:45", "08:15"}, []int{5, 1}
	input["issue_mode"], input["issue_id"] = "existing", issue.ID
	input["concurrency_policy"], input["misfire_policy"] = "allow", "run_once"
	current := f.createTask(input)
	path := "/v1/scheduled-tasks/" + current.ID
	for _, action := range []string{"pause", "resume"} {
		before := current
		reply := f.call(http.MethodPost, path+"/"+action, map[string]any{"expected_version": current.Version}, "", http.StatusOK,
			"pause/resume changes only enabled/version/timestamp and retains the complete extended weekly/Issue/policy configuration")
		current = scheduleSurfaceData[domain.ScheduledTask](t, reply)
		if current.Version != before.Version+1 || reply.Meta.ResourceVersion != current.Version || current.Enabled != (action == "resume") {
			t.Fatalf("invalid %s transition: %+v", action, current)
		}
		comparison := current
		comparison.Versioned, comparison.Enabled = before.Versioned, before.Enabled
		if !reflect.DeepEqual(comparison, before) {
			t.Fatalf("%s dropped extension configuration: before=%+v after=%+v", action, before, current)
		}
		f.assertTaskUnchanged(current)
		for _, version := range []int64{before.Version, current.Version + 1} {
			scheduleSurfaceError(t, f.call(http.MethodPost, path+"/"+action, map[string]any{"expected_version": version}, "", http.StatusConflict,
				"stale/future optimistic versions reject without changing enabled state or profile"), "version_conflict")
			f.assertTaskUnchanged(current)
		}
	}
	// Omitted Agent and Issue destination are specified retention semantics;
	// changing the calendar must not silently convert an existing target to new.
	delete(input, "agent_id")
	delete(input, "issue_mode")
	delete(input, "issue_id")
	input["name"], input["expected_version"], input["times"] = "  updated name  ", current.Version, []string{"09:10", "18:20", "09:10"}
	updated := scheduleSurfaceData[domain.ScheduledTask](t, f.call(http.MethodPatch, path, input, "", http.StatusOK,
		"valid PATCH increments version, trims name, deduplicates times, and preserves omitted Agent/Issue target"))
	if updated.Version != current.Version+1 || updated.Name != "updated name" || updated.AgentID != f.agent.ID || updated.IssueMode != "existing" || updated.IssueID != issue.ID || !reflect.DeepEqual(updated.Times, []string{"09:10", "18:20"}) {
		t.Fatalf("PATCH retention/normalization mismatch: %+v", updated)
	}
	f.assertTaskUnchanged(updated)
	input["expected_version"], input["name"] = current.Version, "must not be persisted"
	scheduleSurfaceError(t, f.call(http.MethodPatch, path, input, "", http.StatusConflict,
		"stale PATCH must not partially save name/calendar/profile"), "version_conflict")
	f.assertTaskUnchanged(updated)
	if len(f.history(updated.ID)) != 0 || len(f.comments(issue.ID)) != 0 {
		t.Fatal("pause/resume/edit unexpectedly created execution")
	}
}

func TestScheduleSurfaceCreateAndUpdateRejectWithoutHalfWriteTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	valid := f.createTask(f.input("unchanged valid task"))
	other := f.createProject("cross-project target")
	foreignIssue := f.createIssue(other.ID, "must not receive a scheduled instruction")
	tooManyTimes := make([]string, 65)
	for index := range tooManyTimes {
		tooManyTimes[index] = fmt.Sprintf("%02d:%02d", index/60, index%60)
	}
	for _, test := range []struct {
		name   string
		fields map[string]any
		omit   string
		status int
		code   string
	}{
		{name: "missing prompt", omit: "prompt"},
		{name: "null prompt", fields: map[string]any{"prompt": nil}},
		{name: "empty prompt", fields: map[string]any{"prompt": []any{}}},
		{name: "blank text", fields: map[string]any{"prompt": []domain.ContentBlock{{Type: "text", Text: " "}}}},
		{name: "non text prompt", fields: map[string]any{"prompt": []domain.ContentBlock{{Type: "image", Text: "not text"}}}},
		{name: "unknown field", fields: map[string]any{"unexpected": true}},
		{name: "unknown schedule kind", fields: map[string]any{"schedule_kind": "yearly"}},
		{name: "invalid timezone", fields: map[string]any{"timezone": "Not/A_Zone"}},
		{name: "machine Local timezone", fields: map[string]any{"timezone": "Local"}},
		{name: "past once", fields: map[string]any{"schedule_expression": "2000-01-01T00:00:00Z"}},
		{name: "once not RFC3339", fields: map[string]any{"schedule_expression": "2040-01-01"}},
		{name: "bad cron syntax", fields: map[string]any{"schedule_kind": "cron", "schedule_expression": "not cron"}},
		{name: "impossible cron", fields: map[string]any{"schedule_kind": "cron", "schedule_expression": "0 0 31 2 *"}, code: "schedule_no_next_run"},
		{name: "daily no times", fields: map[string]any{"schedule_kind": "daily", "times": []string{}}},
		{name: "daily null times", fields: map[string]any{"schedule_kind": "daily", "times": nil}},
		{name: "daily too many distinct times", fields: map[string]any{"schedule_kind": "daily", "times": tooManyTimes}},
		{name: "daily bad pair", fields: map[string]any{"schedule_kind": "daily", "times": []string{"8:15"}}},
		{name: "daily invalid hour", fields: map[string]any{"schedule_kind": "daily", "times": []string{"24:00"}}},
		{name: "weekly no weekdays", fields: map[string]any{"schedule_kind": "weekly", "times": []string{"08:15"}}},
		{name: "weekly invalid weekday", fields: map[string]any{"schedule_kind": "weekly", "times": []string{"08:15"}, "weekdays": []int{7}}},
		{name: "invalid issue mode", fields: map[string]any{"issue_mode": "append"}},
		{name: "existing no issue", fields: map[string]any{"issue_mode": "existing"}},
		{name: "new with issue", fields: map[string]any{"issue_mode": "new", "issue_id": foreignIssue.ID}},
		{name: "cross project issue", fields: map[string]any{"issue_mode": "existing", "issue_id": foreignIssue.ID}, status: http.StatusNotFound, code: "not_found"},
		{name: "nonexistent existing issue", fields: map[string]any{"issue_mode": "existing", "issue_id": "missing-issue"}, status: http.StatusNotFound, code: "not_found"},
		{name: "nonexistent agent", fields: map[string]any{"agent_id": "missing-agent"}, status: http.StatusNotFound, code: "not_found"},
		{name: "invalid concurrency policy", fields: map[string]any{"concurrency_policy": "replace"}},
		{name: "invalid misfire policy", fields: map[string]any{"misfire_policy": "catch_up"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := f.withT(t)
			beforeTasks, beforeIssues := f.tasks(f.project.ID), f.issues(f.project.ID)
			body := f.input("must not partially save")
			for key, value := range test.fields {
				body[key] = value
			}
			if test.omit != "" {
				delete(body, test.omit)
			}
			status, code := test.status, test.code
			if status == 0 {
				status = http.StatusBadRequest
			}
			if code == "" {
				code = "invalid_argument"
			}
			scheduleSurfaceError(t, f.call(http.MethodPost, "/v1/projects/"+f.project.ID+"/scheduled-tasks", body, "", status,
				"invalid create rejects "+test.name+" before persisting task/profile/Issue"), code)
			body["expected_version"] = valid.Version
			scheduleSurfaceError(t, f.call(http.MethodPatch, "/v1/scheduled-tasks/"+valid.ID, body, "", status,
				"invalid PATCH rejects "+test.name+" without partial writes"), code)
			f.assertTaskUnchanged(valid)
			if !reflect.DeepEqual(beforeTasks, f.tasks(f.project.ID)) || !reflect.DeepEqual(beforeIssues, f.issues(f.project.ID)) || len(f.comments(foreignIssue.ID)) != 0 {
				t.Fatal("rejected schedule mutation left a half-written task or Issue/comment")
			}
		})
	}
	// Blank Agent is a create error; omission on PATCH deliberately retains it.
	body := f.input("missing writer")
	delete(body, "agent_id")
	scheduleSurfaceError(t, f.call(http.MethodPost, "/v1/projects/"+f.project.ID+"/scheduled-tasks", body, "", http.StatusBadRequest,
		"new task requires an explicit configured Agent"), "invalid_argument")
	f.assertTaskUnchanged(valid)
}

func TestScheduleSurfaceUnconfiguredAndDisabledWriterTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	lumi := scheduleSurfaceData[domain.Agent](t, f.call(http.MethodGet, "/v1/work-assistant", nil, "", http.StatusOK,
		"fresh Lumi identity exists but has no configured revision"))
	if lumi.CurrentRevisionID != "" {
		t.Fatal("fresh fixture unexpectedly configured Lumi")
	}
	input := f.input("unconfigured writer must not save")
	input["agent_id"] = lumi.ID
	scheduleSurfaceError(t, f.call(http.MethodPost, "/v1/projects/"+f.project.ID+"/scheduled-tasks", input, "", http.StatusBadRequest,
		"protected Lumi identity alone does not satisfy the configured schedule writer requirement"), "invalid_argument")
	f.call(http.MethodPatch, "/v1/agents/"+f.agent.ID, map[string]any{"expected_version": f.agent.Version, "status": domain.AgentDisabled}, "", http.StatusOK,
		"disable the ordinary HTTP-created writer using the public Agent API")
	input["agent_id"] = f.agent.ID
	scheduleSurfaceError(t, f.call(http.MethodPost, "/v1/projects/"+f.project.ID+"/scheduled-tasks", input, "", http.StatusBadRequest,
		"disabled configured Agent cannot create an enabled schedule"), "invalid_argument")
	if len(f.tasks(f.project.ID)) != 0 || len(f.issues(f.project.ID)) != 0 {
		t.Fatal("invalid schedule writer left task or Issue state")
	}
}

func TestScheduleSurfaceTriggerAtomicReplayAndConcurrencyTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	task := f.createTask(f.input("atomic new Issue"))
	path := "/v1/scheduled-tasks/" + task.ID + "/run-now"
	scheduleSurfaceError(t, f.call(http.MethodPost, path, nil, "", http.StatusBadRequest,
		"manual trigger requires an idempotency key before making any execution records"), "invalid_argument")
	if len(f.history(task.ID)) != 0 || len(f.issues(f.project.ID)) != 0 {
		t.Fatal("missing idempotency key wrote a trigger/Issue")
	}
	first := f.call(http.MethodPost, path, nil, "atomic-one", http.StatusAccepted,
		"first manual trigger atomically commits Issue, user comment, consumed mention, fresh instance, queued Run, and history")
	trigger := scheduleSurfaceData[domain.ScheduledTaskRun](t, first)
	if first.Meta.Replayed || trigger.ID == "" || trigger.RunID == "" || trigger.IssueID == "" || trigger.TriggerKey != "manual:atomic-one" || trigger.ScheduledTaskID != task.ID || trigger.Status != "running" {
		t.Fatalf("incomplete first trigger: %+v", trigger)
	}
	f.assertAtomicTrigger(task, trigger)
	beforeIssues, beforeComments, beforeInstances := f.issues(f.project.ID), f.comments(trigger.IssueID), f.instances(trigger.IssueID)
	reply := f.call(http.MethodPost, path, nil, "atomic-one", http.StatusOK,
		"successful same-key replay returns the exact same trigger even while forbid has an active Run")
	replayed := scheduleSurfaceData[domain.ScheduledTaskRun](t, reply)
	if !reply.Meta.Replayed || !reflect.DeepEqual(trigger, replayed) {
		t.Fatalf("replay changed trigger identity/metadata: %+v %+v", trigger, replayed)
	}
	scheduleSurfaceError(t, f.call(http.MethodPost, path, nil, "atomic-two", http.StatusConflict,
		"a different trigger key under forbid rejects concurrent active execution without half-writing a new Issue"), "schedule_concurrency_forbidden")
	if history := f.history(task.ID); len(history) != 1 || !reflect.DeepEqual(history[0], trigger) {
		t.Fatalf("replay/concurrency conflict duplicated history: %+v", history)
	}
	if !reflect.DeepEqual(beforeIssues, f.issues(f.project.ID)) || !reflect.DeepEqual(beforeComments, f.comments(trigger.IssueID)) || !reflect.DeepEqual(beforeInstances, f.instances(trigger.IssueID)) {
		t.Fatal("replay or forbidden trigger left execution half-writes")
	}
	f.assertTaskUnchanged(task)
}

func (f *scheduleSurfaceFixture) assertAtomicTrigger(task domain.ScheduledTask, trigger domain.ScheduledTaskRun) {
	f.t.Helper()
	issue := scheduleSurfaceData[domain.Issue](f.t, f.call(http.MethodGet, "/v1/issues/"+trigger.IssueID, nil, "", http.StatusOK,
		"trigger Issue is readable immediately after commit and belongs to the schedule project"))
	if issue.ProjectID != task.ProjectID || issue.Status != domain.IssueOpen || issue.CreatedByUserID != f.user.ID || (task.IssueMode == "new" && issue.Title != "新任务") {
		f.t.Fatalf("invalid scheduled Issue: %+v", issue)
	}
	run := scheduleSurfaceData[domain.Run](f.t, f.call(http.MethodGet, "/v1/runs/"+trigger.RunID, nil, "", http.StatusOK,
		"trigger Run is a real immediately readable queued writable Issue Run, not a missing-id negative"))
	if run.ProjectID != task.ProjectID || run.IssueID != trigger.IssueID || run.AgentID != task.AgentID || run.IssueAgentGenerationID == "" || run.IssueAgentInstanceID == "" || run.Status != domain.RunQueued || run.ReadOnly || run.AgentServiceID != f.service.ID || run.Model != "schedule-fixture-model" {
		f.t.Fatalf("invalid durable scheduled Run: %+v", run)
	}
	comments := f.comments(trigger.IssueID)
	var instructions, working int
	for _, comment := range comments {
		if comment.AuthorKind == domain.IssueCommentAuthorUser && comment.Body == "first instruction\n\nsecond instruction" {
			instructions++
			if comment.AuthorUserID != f.user.ID || comment.RunID != "" || comment.Status != domain.IssueCommentPublished {
				f.t.Fatalf("schedule invented an Agent-authored public instruction: %+v", comment)
			}
			// No public HTTP mentions route exists. This read-only public repository
			// check supplements GET comments/instances/Run; it does not seed state.
			mentions, err := f.repo.ListIssueCommentMentions(f.t.Context(), f.user.ID, trigger.IssueID)
			if err != nil || len(mentions) != 1 || mentions[0].CommentID != comment.ID || mentions[0].TargetKind != "agent" || mentions[0].TargetAgentID != task.AgentID || mentions[0].ConsumedAt == nil {
				f.t.Fatalf("scheduled mention was not atomically consumed: %+v err=%v", mentions, err)
			}
		}
		if comment.RunID == run.ID && comment.AuthorKind == domain.IssueCommentAuthorAgent && comment.AuthorAgentID == task.AgentID && comment.Status == domain.IssueCommentWorking {
			working++
		}
	}
	if instructions != 1 || working != 1 {
		f.t.Fatalf("expected one genuine user instruction and one linked working reply, got %+v", comments)
	}
	instances := f.instances(trigger.IssueID)
	agent := scheduleSurfaceData[domain.Agent](f.t, f.call(http.MethodGet, "/v1/agents/"+task.AgentID, nil, "", http.StatusOK,
		"scheduled instance binds the selected Agent's actual configured revision"))
	var linked int
	for _, instance := range instances {
		if instance.ID == run.IssueAgentInstanceID && instance.AgentID == task.AgentID && instance.AgentRevisionID == agent.CurrentRevisionID && instance.PendingMentionCount == 0 {
			linked++
		}
	}
	if linked != 1 {
		f.t.Fatalf("Run does not link a unique committed session: %+v", instances)
	}
	instanceRuns := scheduleSurfaceData[domain.Page[domain.Run]](f.t, f.call(http.MethodGet, "/v1/agent-instances/"+run.IssueAgentInstanceID+"/runs", nil, "", http.StatusOK,
		"GET instance runs confirms the trigger Run belongs to its durable session"))
	if len(instanceRuns.Items) != 1 || instanceRuns.Items[0].ID != run.ID {
		f.t.Fatalf("unexpected instance Run set: %+v", instanceRuns)
	}
}

func TestScheduleSurfaceConcurrentIdenticalKeyTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	input := f.input("concurrent trigger admission")
	input["concurrency_policy"] = "allow" // Deduplication, not forbid, must prevent duplicates.
	task := f.createTask(input)
	const requests = 12
	replies := make([]scheduleSurfaceReply, requests)
	errors := make([]error, requests)
	var group sync.WaitGroup
	start := make(chan struct{})
	for index := range requests {
		// Declare the allowed response and invariant before every concurrent send.
		t.Logf("EXPECT 202(first) or 200(replay) POST run-now request=%d — identical key must commit exactly one trigger/Issue/mention/Run", index)
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			replies[index], errors[index] = f.do(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/run-now", nil, "simultaneous-identical")
		}()
	}
	close(start)
	group.Wait()
	var first int
	var canonical domain.ScheduledTaskRun
	for index, reply := range replies {
		if errors[index] != nil {
			t.Fatal(errors[index])
		}
		if reply.Status != http.StatusAccepted && reply.Status != http.StatusOK {
			t.Fatalf("same-key concurrent request %d: status=%d body=%s", index, reply.Status, reply.Raw)
		}
		if reply.Status == http.StatusAccepted {
			first++
			if reply.Meta.Replayed {
				t.Fatal("first trigger marked replayed")
			}
		} else if !reply.Meta.Replayed {
			t.Fatal("replayed concurrent trigger lacks replay metadata")
		}
		trigger := scheduleSurfaceData[domain.ScheduledTaskRun](t, reply)
		if index == 0 {
			canonical = trigger
		} else if !reflect.DeepEqual(canonical, trigger) {
			t.Fatalf("same key returned different trigger identities: %+v %+v", canonical, trigger)
		}
	}
	if first != 1 || len(f.history(task.ID)) != 1 || len(f.issues(f.project.ID)) != 1 {
		t.Fatalf("concurrent key was not exactly once: first=%d", first)
	}
	f.assertAtomicTrigger(task, canonical)
	if len(f.instances(canonical.IssueID)) != 1 || len(f.comments(canonical.IssueID)) != 2 {
		t.Fatal("concurrent replay created extra sessions/comments")
	}
}

func TestScheduleSurfaceExistingTargetAndConfiguredLumiTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	issue := f.createIssue(f.project.ID, "existing Issue title")
	f.call(http.MethodPost, "/v1/issues/"+issue.ID+"/comments", map[string]any{"body": "existing history"}, "", http.StatusCreated,
		"existing target retains pre-schedule discussion")
	lumi := scheduleSurfaceData[domain.Agent](t, f.call(http.MethodGet, "/v1/work-assistant", nil, "", http.StatusOK,
		"get the real protected Lumi identity rather than inventing one"))
	lumi = scheduleSurfaceData[domain.Agent](t, f.call(http.MethodPatch, "/v1/agents/"+lumi.ID,
		map[string]any{"expected_version": lumi.Version, "agent_service_id": f.service.ID, "model": "schedule-fixture-model", "system_prompt": "configured via the public Agent API"},
		"", http.StatusOK, "configure Lumi through the same public Agent endpoint as any writable Agent"))
	input := f.input("append to existing with Lumi")
	input["agent_id"], input["issue_mode"], input["issue_id"], input["concurrency_policy"] = lumi.ID, "existing", issue.ID, "allow"
	task := f.createTask(input)
	trigger := scheduleSurfaceData[domain.ScheduledTaskRun](t, f.call(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/run-now", nil, "existing-lumi", http.StatusAccepted,
		"configured Lumi atomically appends a real writable Run to the selected existing Issue"))
	if trigger.IssueID != issue.ID || len(f.issues(f.project.ID)) != 1 {
		t.Fatal("existing target trigger created a new or different Issue")
	}
	f.assertAtomicTrigger(task, trigger)
	stored := scheduleSurfaceData[domain.Issue](t, f.call(http.MethodGet, "/v1/issues/"+issue.ID, nil, "", http.StatusOK,
		"existing Issue title/description remain unchanged after scheduled append"))
	if stored.Title != issue.Title || stored.Description != issue.Description || len(f.comments(issue.ID)) != 3 {
		t.Fatal("existing target context/history was overwritten")
	}
	second := scheduleSurfaceData[domain.ScheduledTaskRun](t, f.call(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/run-now", nil, "existing-lumi-second", http.StatusAccepted,
		"allow admits a distinct trigger while the first Run is still queued, preserving per-mention fresh sessions"))
	if second.ID == trigger.ID || second.RunID == trigger.RunID || second.IssueID != issue.ID || len(f.history(task.ID)) != 2 || len(f.instances(issue.ID)) != 2 || len(f.comments(issue.ID)) != 5 || len(f.issues(f.project.ID)) != 1 {
		t.Fatal("allow failed to create exactly one additional Run/session in the existing Issue")
	}
	runs := scheduleSurfaceData[domain.Page[domain.Run]](t, f.call(http.MethodGet, "/v1/issues/"+issue.ID+"/runs", nil, "", http.StatusOK,
		"both allowed scheduled Runs are real queued Runs with different fresh sessions"))
	if len(runs.Items) != 2 || runs.Items[0].IssueAgentInstanceID == runs.Items[1].IssueAgentInstanceID || runs.Items[0].ReadOnly || runs.Items[1].ReadOnly || runs.Items[0].Status != domain.RunQueued || runs.Items[1].Status != domain.RunQueued {
		t.Fatalf("allow/repeated mention semantics mismatch: %+v", runs)
	}
	mentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, issue.ID)
	if err != nil || len(mentions) != 2 || mentions[0].ConsumedAt == nil || mentions[1].ConsumedAt == nil || mentions[0].CommentID == mentions[1].CommentID {
		t.Fatalf("allowed triggers did not independently consume their mentions: %+v err=%v", mentions, err)
	}
}

func TestScheduleSurfaceStoppedTargetsRejectWithoutHalfWriteTCP(t *testing.T) {
	for _, state := range []string{"closed", "resolved", "paused-instance", "paused-task", "disabled-agent"} {
		t.Run(state, func(t *testing.T) {
			f := newScheduleSurfaceFixture(t)
			issue := f.createIssue(f.project.ID, "emergency stop target")
			input := f.input("must respect stop")
			input["issue_mode"], input["issue_id"], input["concurrency_policy"] = "existing", issue.ID, "allow"
			task := f.createTask(input)
			status, code := http.StatusBadRequest, "invalid_argument"
			switch state {
			case "closed", "resolved":
				f.call(http.MethodPatch, "/v1/issues/"+issue.ID, map[string]any{"expected_version": issue.Version, "status": state}, "", http.StatusOK,
					"user explicitly ends the existing Issue before the scheduled trigger")
			case "paused-instance":
				// Public repository creation is only a fixture for the already-open
				// session that the user pauses; no dispatcher/model is run.
				instance, _, err := f.repo.EnsureIssueAgentInstance(t.Context(), f.user.ID, issue.ID, f.agent.ID)
				if err != nil {
					t.Fatal(err)
				}
				f.call(http.MethodPost, "/v1/issues/"+issue.ID+"/agent-instances/"+instance.ID+"/pause", map[string]any{"expected_version": instance.Version}, "", http.StatusOK,
					"user emergency-pauses an existing conversation; schedule must not bypass it with a new session")
			case "paused-task":
				task = scheduleSurfaceData[domain.ScheduledTask](t, f.call(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/pause", map[string]any{"expected_version": task.Version}, "", http.StatusOK,
					"user pauses schedule before manual trigger"))
				status, code = http.StatusConflict, "scheduled_task_paused"
			case "disabled-agent":
				f.call(http.MethodPatch, "/v1/agents/"+f.agent.ID, map[string]any{"expected_version": f.agent.Version, "status": domain.AgentDisabled}, "", http.StatusOK,
					"disable selected Agent after schedule configuration")
				status, code = http.StatusNotFound, "not_found"
			}
			beforeIssues, beforeComments, beforeInstances := f.issues(f.project.ID), f.comments(issue.ID), f.instances(issue.ID)
			beforeMentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, issue.ID)
			if err != nil {
				t.Fatal(err)
			}
			scheduleSurfaceError(t, f.call(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/run-now", nil, "blocked-"+state, status,
				"run-now respects "+state+" and atomically rolls back trigger/comment/mention/session/Run"), code)
			if len(f.history(task.ID)) != 0 || !reflect.DeepEqual(beforeIssues, f.issues(f.project.ID)) || !reflect.DeepEqual(beforeComments, f.comments(issue.ID)) || !reflect.DeepEqual(beforeInstances, f.instances(issue.ID)) {
				t.Fatal("blocked manual trigger left a half-written execution")
			}
			afterMentions, err := f.repo.ListIssueCommentMentions(t.Context(), f.user.ID, issue.ID)
			if err != nil || !reflect.DeepEqual(beforeMentions, afterMentions) {
				t.Fatalf("blocked trigger changed durable mentions: before=%+v after=%+v err=%v", beforeMentions, afterMentions, err)
			}
			runs := scheduleSurfaceData[domain.Page[domain.Run]](t, f.call(http.MethodGet, "/v1/issues/"+issue.ID+"/runs", nil, "", http.StatusOK,
				"blocked scheduled trigger leaves no orphan Issue Run even when history is empty"))
			if len(runs.Items) != 0 {
				t.Fatalf("blocked trigger left an orphan Run: %+v", runs)
			}
			f.assertTaskUnchanged(task)
			if state == "disabled-agent" {
				paused := scheduleSurfaceData[domain.ScheduledTask](t, f.call(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/pause", map[string]any{"expected_version": task.Version}, "", http.StatusOK,
					"emergency pause must remain available after selected Agent is disabled"))
				scheduleSurfaceError(t, f.call(http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/resume", map[string]any{"expected_version": paused.Version}, "", http.StatusNotFound,
					"resume requires a still-valid enabled Agent and must leave the paused task unchanged"), "not_found")
				f.assertTaskUnchanged(paused)
			}
		})
	}
}

func TestScheduleSurfaceMissingResourcesAndVersionShapeTCP(t *testing.T) {
	f := newScheduleSurfaceFixture(t)
	task := f.createTask(f.input("version admission"))
	for _, operation := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/projects/missing-project/scheduled-tasks", nil},
		{http.MethodPost, "/v1/projects/missing-project/scheduled-tasks", f.input("missing project")},
		{http.MethodGet, "/v1/scheduled-tasks/missing-task", nil},
		{http.MethodGet, "/v1/scheduled-tasks/missing-task/runs", nil},
		{http.MethodPatch, "/v1/scheduled-tasks/missing-task", map[string]any{"name": "missing", "schedule_kind": "once", "schedule_expression": "2040-01-01T00:00:00Z", "timezone": "UTC", "prompt": []domain.ContentBlock{{Type: "text", Text: "text"}}, "expected_version": 1}},
		{http.MethodPost, "/v1/scheduled-tasks/missing-task/pause", map[string]any{"expected_version": 1}},
		{http.MethodPost, "/v1/scheduled-tasks/missing-task/resume", map[string]any{"expected_version": 1}},
		{http.MethodPost, "/v1/scheduled-tasks/missing-task/run-now", nil},
	} {
		scheduleSurfaceError(t, f.call(operation.method, operation.path, operation.body, "missing-resource", http.StatusNotFound,
			"missing parent/resource is a real not_found and never creates execution"), "not_found")
	}
	for _, action := range []string{"pause", "resume", "patch"} {
		for _, test := range []struct {
			name  string
			value any
			omit  bool
		}{
			{name: "omitted", omit: true}, {name: "null", value: nil}, {name: "zero", value: 0}, {name: "negative", value: -1}, {name: "string", value: "1"}, {name: "fraction", value: 1.5},
		} {
			t.Run(action+"/"+test.name, func(t *testing.T) {
				f := f.withT(t)
				body, method, path := map[string]any{}, http.MethodPost, "/v1/scheduled-tasks/"+task.ID+"/"+action
				if action == "patch" {
					body, method, path = f.input("must not change"), http.MethodPatch, "/v1/scheduled-tasks/"+task.ID
				}
				if !test.omit {
					body["expected_version"] = test.value
				}
				scheduleSurfaceError(t, f.call(method, path, body, "", http.StatusBadRequest,
					fmt.Sprintf("%s rejects %s version shape before mutation", action, test.name)), "invalid_argument")
				f.assertTaskUnchanged(task)
			})
		}
	}
	if len(f.history(task.ID)) != 0 || len(f.issues(f.project.ID)) != 0 {
		t.Fatal("admission failures created execution side effects")
	}
}
