package store

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// Inspect metadata through the shared persistence boundary without coupling
// these integration tests to the title worker's private state/job JSON shape.
func titleWiringMetadata(t *testing.T, s *SQLite, entityID string) map[string]string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), `SELECT key,value FROM app_metadata ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		if entityID == "" || (!strings.HasPrefix(key, "scheduled_task_config:") && (strings.Contains(key, entityID) || strings.Contains(value, entityID))) {
			values[key] = value
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func titleWiringContains(values map[string]string, text string) bool {
	for _, value := range values {
		if strings.Contains(value, text) {
			return true
		}
	}
	return false
}

type titleWiringEntity struct {
	id, title string
	version   int64
}

func createTitleWiringEntity(t *testing.T, f sqliteExecutionFixture, kind, title, seed string) (titleWiringEntity, error) {
	t.Helper()
	ctx := t.Context()
	switch kind {
	case "issue":
		n, err := f.repository.CreateIssue(ctx, f.user.ID, domain.Issue{ProjectID: f.project.ID, Title: title, Description: seed})
		return titleWiringEntity{n.ID, n.Title, n.Version}, err
	case "inspiration":
		n, err := f.repository.CreateInspiration(ctx, f.user.ID, newID("key"), domain.Inspiration{ProjectID: f.project.ID, Title: title, Markdown: seed})
		return titleWiringEntity{n.ID, n.Title, n.Version}, err
	case "memory":
		n, err := f.repository.CreateMemory(ctx, f.user.ID, domain.ProjectMemoryEntry{ProjectID: f.project.ID, Key: newID("key"), Kind: "fact", Title: title, Content: seed, SourceType: "user"}, "user")
		return titleWiringEntity{n.ID, n.Title, n.Version}, err
	case "schedule":
		n, err := f.repository.CreateScheduledTask(ctx, f.user.ID, domain.ScheduledTask{ProjectID: f.project.ID, Name: title,
			ScheduleKind: "once", ScheduleExpression: "2040-01-01T00:00:00Z", Timezone: "UTC", AgentID: f.agent.ID,
			Prompt: []domain.ContentBlock{{Type: "text", Text: seed}}, ConcurrencyPolicy: "forbid", MisfirePolicy: "skip"})
		return titleWiringEntity{n.ID, n.Name, n.Version}, err
	default:
		t.Fatalf("unsupported kind: %s", kind)
		return titleWiringEntity{}, nil
	}
}

// nil title denotes a body-only update. A non-nil, unchanged title still carries
// explicit manual intent, independently of whether the entity row changes.
func updateTitleWiringEntity(t *testing.T, f sqliteExecutionFixture, kind string, prior titleWiringEntity, title *string, seed string) titleWiringEntity {
	t.Helper()
	ctx := t.Context()
	switch kind {
	case "issue":
		n, err := f.repository.UpdateIssue(ctx, f.user.ID, prior.id, prior.version, func(n *domain.Issue) {
			if title != nil {
				n.Title, n.TitleEdited = *title, true
			}
			n.Description = seed
		})
		if err != nil {
			t.Fatal(err)
		}
		return titleWiringEntity{n.ID, n.Title, n.Version}
	case "inspiration":
		n, err := f.repository.UpdateInspiration(ctx, f.user.ID, prior.id, prior.version, domain.InspirationPatch{Title: title, Markdown: &seed})
		if err != nil {
			t.Fatal(err)
		}
		return titleWiringEntity{n.ID, n.Title, n.Version}
	case "memory":
		value := ""
		if title != nil {
			value = *title
		}
		n, err := f.repository.UpdateMemory(ctx, f.user.ID, f.project.ID, prior.id, prior.version, value, seed, "user", title != nil)
		if err != nil {
			t.Fatal(err)
		}
		return titleWiringEntity{n.ID, n.Title, n.Version}
	case "schedule":
		n, err := f.repository.UpdateScheduledTask(ctx, f.user.ID, prior.id, prior.version, func(n *domain.ScheduledTask) {
			if title != nil {
				n.Name, n.TitleEdited = *title, true
			}
			n.Prompt = []domain.ContentBlock{{Type: "text", Text: seed}}
		})
		if err != nil {
			t.Fatal(err)
		}
		return titleWiringEntity{n.ID, n.Name, n.Version}
	}
	t.Fatalf("unsupported kind: %s", kind)
	return titleWiringEntity{}
}

func TestSQLiteTitleWiringCreateDefaultsAndExplicitTitle(t *testing.T) {
	for _, tc := range []struct{ kind, title string }{{"issue", "新任务"}, {"inspiration", "新灵感"}, {"memory", "新记忆"}, {"schedule", "新定时任务"}} {
		t.Run(tc.kind, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			for _, seed := range []string{"", "creation-title-seed"} {
				n, err := createTitleWiringEntity(t, f, tc.kind, "", seed)
				if err != nil || n.title != tc.title {
					t.Fatalf("create: %+v err=%v", n, err)
				}
				metadata := titleWiringMetadata(t, f.repository, n.id)
				if len(metadata) == 0 || (seed != "" && !titleWiringContains(metadata, seed)) {
					t.Fatal("automatic entity did not atomically initialize state/queue its creation body")
				}
			}
			n, err := createTitleWiringEntity(t, f, tc.kind, "用户标题", "must-not-queue-explicit-title")
			if err != nil || n.title != "用户标题" || len(titleWiringMetadata(t, f.repository, n.id)) != 0 {
				t.Fatalf("explicit title was changed or initialized automatically: %+v err=%v", n, err)
			}
			_ = updateTitleWiringEntity(t, f, tc.kind, n, nil, "legacy-body-update")
			if len(titleWiringMetadata(t, f.repository, n.id)) != 0 {
				t.Fatal("body update migrated an entity without automatic title state")
			}
		})
	}
}

func TestSQLiteTitleWiringFirstContentAndSameValueManualIntent(t *testing.T) {
	for _, kind := range []string{"issue", "inspiration", "memory", "schedule"} {
		t.Run(kind, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			n, err := createTitleWiringEntity(t, f, kind, "", "")
			if err != nil {
				t.Fatal(err)
			}
			n = updateTitleWiringEntity(t, f, kind, n, nil, "first-content-title-seed")
			queued := titleWiringMetadata(t, f.repository, n.id)
			if !titleWiringContains(queued, "first-content-title-seed") {
				t.Fatal("first body-only update disabled generation instead of queuing")
			}
			n = updateTitleWiringEntity(t, f, kind, n, nil, "later-content-must-not-reseed")
			if !reflect.DeepEqual(queued, titleWiringMetadata(t, f.repository, n.id)) {
				t.Fatal("later content requeued/reseeded automatic title")
			}
			n = updateTitleWiringEntity(t, f, kind, n, &n.title, "later-content-must-not-reseed")
			manual := titleWiringMetadata(t, f.repository, n.id)
			if reflect.DeepEqual(queued, manual) {
				t.Fatal("same-value explicit title did not fence the queued result")
			}
			_ = updateTitleWiringEntity(t, f, kind, n, nil, "manual-must-stay-fenced")
			if !reflect.DeepEqual(manual, titleWiringMetadata(t, f.repository, n.id)) {
				t.Fatal("body-only update revived manual title generation")
			}
		})
	}
}

func TestSQLiteTitleWiringFailedCreationHasNoGhostState(t *testing.T) {
	for _, kind := range []string{"issue", "inspiration", "memory", "schedule"} {
		t.Run(kind, func(t *testing.T) {
			f := newSQLiteExecutionFixture(t)
			defer f.repository.Close()
			if _, err := f.repository.db.ExecContext(t.Context(), `CREATE TEMP TRIGGER reject_title_seed BEFORE INSERT ON app_metadata
				WHEN instr(NEW.value,'rollback-title-seed')>0 BEGIN SELECT RAISE(ABORT,'reject title queue'); END`); err != nil {
				t.Fatal(err)
			}
			before := titleWiringMetadata(t, f.repository, "")
			counts := func() []int {
				var result []int
				for _, table := range []string{"issues", "inspirations", "project_memory_entries", "project_memory_versions", "scheduled_tasks"} {
					var count int
					if err := f.repository.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
						t.Fatal(err)
					}
					result = append(result, count)
				}
				return result
			}
			priorCounts := counts()
			if _, err := createTitleWiringEntity(t, f, kind, "", "rollback-title-seed"); err == nil {
				t.Fatal("queue DB error was swallowed")
			}
			if !reflect.DeepEqual(before, titleWiringMetadata(t, f.repository, "")) || !reflect.DeepEqual(priorCounts, counts()) {
				t.Fatal("failed creation left an entity, version, config, title state or job")
			}
		})
	}
}

func TestSQLiteTitleWiringUserCommentsWithoutMentionsAndConcurrentFirst(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	ctx := t.Context()
	n, err := f.repository.CreateIssue(ctx, f.user.ID, domain.Issue{ProjectID: f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.EnsureIssueAgentInstance(ctx, f.user.ID, n.ID, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	initial := titleWiringMetadata(t, f.repository, n.ID)
	if _, err := f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{IssueID: n.ID, AuthorKind: "agent", AuthorAgentID: f.agent.ID, Body: "agent-must-not-seed"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{IssueID: n.ID, AuthorKind: "user", Body: "bad-mention-must-not-seed"}, []string{"missing-agent"}); err == nil {
		t.Fatal("invalid mention accepted")
	}
	if !reflect.DeepEqual(initial, titleWiringMetadata(t, f.repository, n.ID)) {
		t.Fatal("Agent comment or failed user publication seeded the title")
	}
	seeds := []string{"concurrent-user-title-seed-one", "concurrent-user-title-seed-two"}
	start := make(chan struct{})
	errors := make(chan error, len(seeds))
	var wg sync.WaitGroup
	for _, seed := range seeds {
		wg.Add(1)
		go func(seed string) {
			defer wg.Done()
			<-start
			_, err := f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{IssueID: n.ID, AuthorKind: "user", Body: seed}, nil)
			errors <- err
		}(seed)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	queued := titleWiringMetadata(t, f.repository, n.ID)
	if titleWiringContains(queued, seeds[0]) == titleWiringContains(queued, seeds[1]) {
		t.Fatal("concurrent first comments must retain exactly one real user seed")
	}
	if _, err := f.repository.CreateIssueComment(ctx, f.user.ID, domain.IssueComment{IssueID: n.ID, AuthorKind: "user", Body: "replayed-user-body"}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(queued, titleWiringMetadata(t, f.repository, n.ID)) {
		t.Fatal("subsequent/replayed comment replaced the first seed")
	}
}

func TestSQLiteTitleWiringInspirationAndScheduledIssueReplay(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	ctx := context.Background()
	input := domain.Inspiration{Markdown: "inspiration-replay-seed"}
	n, err := f.repository.CreateInspiration(ctx, f.user.ID, "title-replay", input)
	if err != nil {
		t.Fatal(err)
	}
	prior := titleWiringMetadata(t, f.repository, "")
	replay, err := f.repository.CreateInspiration(ctx, f.user.ID, "title-replay", input)
	if err != nil || n.ID != replay.ID || !reflect.DeepEqual(prior, titleWiringMetadata(t, f.repository, "")) {
		t.Fatalf("inspiration replay changed state: %+v err=%v", replay, err)
	}
	task := createProfileSchedule(t, f, "new")
	trigger, replayed, err := f.repository.TriggerScheduledTask(ctx, task.ID, "manual:title-wiring", time.Now())
	if err != nil || replayed {
		t.Fatalf("trigger: %v replayed=%t", err, replayed)
	}
	issue, err := f.repository.GetIssue(ctx, f.user.ID, trigger.IssueID)
	if err != nil || issue.Title != "新任务" || !titleWiringContains(titleWiringMetadata(t, f.repository, issue.ID), task.Prompt[0].Text) {
		t.Fatalf("scheduled issue title/seed: %+v err=%v", issue, err)
	}
	prior = titleWiringMetadata(t, f.repository, "")
	again, replayed, err := f.repository.TriggerScheduledTask(ctx, task.ID, "manual:title-wiring", time.Now())
	if err != nil || !replayed || again.IssueID != issue.ID || !reflect.DeepEqual(prior, titleWiringMetadata(t, f.repository, "")) {
		t.Fatalf("schedule replay changed title state/jobs: %+v err=%v", again, err)
	}
}

func TestSQLiteTitleWiringScheduledPromptInternalFlagIsStillUserContent(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	task, err := f.repository.CreateScheduledTask(t.Context(), f.user.ID, domain.ScheduledTask{ProjectID: f.project.ID,
		ScheduleKind: "once", ScheduleExpression: "2040-01-01T00:00:00Z", Timezone: "UTC", AgentID: f.agent.ID,
		Prompt:            []domain.ContentBlock{{Type: "text", Text: "user-internal-flag-work", Internal: true}},
		ConcurrencyPolicy: "forbid", MisfirePolicy: "skip"})
	if err != nil || !titleWiringContains(titleWiringMetadata(t, f.repository, task.ID), "user-internal-flag-work") {
		t.Fatalf("public prompt display flag suppressed title seed: %+v err=%v", task, err)
	}
}
