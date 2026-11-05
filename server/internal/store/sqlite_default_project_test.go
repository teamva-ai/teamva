package store

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestSQLiteDefaultProjectUniqueAcrossReopenAndConcurrentStartup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	repository, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	projects, err := repository.ListProjects(ctx, user.ID, computer.ID)
	if err != nil || len(projects) != 1 {
		t.Fatalf("empty database projects = %+v, err=%v", projects, err)
	}
	initial := projects[0]
	if !initial.IsDefault || initial.ID != domain.DefaultProjectID(computer.ID) || initial.Name != "默认项目" || initial.Version != 1 {
		t.Fatalf("invalid default project: %+v", initial)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			reopened, err := OpenSQLite(ctx, path)
			if err != nil {
				t.Errorf("concurrent startup: %v", err)
				return
			}
			defer reopened.Close()
			items, err := reopened.ListProjects(ctx, user.ID, computer.ID)
			if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], initial) {
				t.Errorf("startup changed default: %+v, err=%v", items, err)
			}
		}()
	}
	workers.Wait()
}

func TestSQLiteDefaultProjectAddedToExistingValidDatabaseWithoutChangingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	repository, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	// Model a valid pre-feature database, including a user project with exactly
	// the same name. No baseline/schema migration is involved.
	if _, err := repository.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, domain.DefaultProjectID(computer.ID)); err != nil {
		t.Fatal(err)
	}
	ordinary, err := repository.CreateProject(ctx, user.ID, domain.Project{
		Versioned:  domain.Versioned{ID: domain.DefaultProjectID(computer.ID)},
		ComputerID: computer.ID, Name: domain.DefaultProjectName, IsDefault: true,
		Description: "unchanged",
	})
	if err != nil || ordinary.IsDefault || ordinary.ID == domain.DefaultProjectID(computer.ID) {
		t.Fatalf("input forged a built-in project: %+v, err=%v", ordinary, err)
	}
	second, err := repository.CreateProject(ctx, user.ID, domain.Project{ComputerID: computer.ID, Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := repository.CreateResource(ctx, user.ID, domain.ProjectResource{ProjectID: ordinary.ID, Kind: "url", Name: "keep", URL: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := repository.GetUserPreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferences, err = repository.UpdateUserPreferences(ctx, user.ID, preferences.Version, domain.LocaleEnUS, domain.ThemeDark, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	items, err := repository.ListProjects(ctx, user.ID, computer.ID)
	if err != nil || len(items) != 3 || !items[0].IsDefault || !reflect.DeepEqual(items[1], ordinary) || !reflect.DeepEqual(items[2], second) {
		t.Fatalf("startup changed existing projects/order: %+v, err=%v", items, err)
	}
	resources, err := repository.ListResources(ctx, user.ID, ordinary.ID)
	if err != nil || len(resources) != 1 || !reflect.DeepEqual(resources[0], resource) {
		t.Fatalf("startup changed resources: %+v, err=%v", resources, err)
	}
	gotPreferences, err := repository.GetUserPreferences(ctx, user.ID)
	if err != nil || gotPreferences != preferences {
		t.Fatalf("startup changed preferences: %+v, err=%v", gotPreferences, err)
	}
	if err := repository.DeleteProject(ctx, user.ID, ordinary.ID, ordinary.Version); err != nil {
		t.Fatalf("ordinary same-name project must remain deletable: %v", err)
	}
}

func TestSQLiteDefaultProjectProtectionAndOwnership(t *testing.T) {
	ctx := context.Background()
	repository, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	id := domain.DefaultProjectID(computer.ID)
	otherUserID, _ := insertSQLiteFeatureTenant(t, ctx, repository)
	_, err = repository.GetProject(ctx, otherUserID, id)
	assertDomainCode(t, err, "not_found")
	_, err = repository.ListProjects(ctx, otherUserID, computer.ID)
	assertDomainCode(t, err, "not_found")
	_, err = repository.UpdateProject(ctx, otherUserID, id, 1, func(item *domain.Project) {
		t.Error("non-owner update callback was invoked")
	})
	assertDomainCode(t, err, "not_found")
	assertDomainCode(t, repository.DeleteProject(ctx, otherUserID, id, 1), "not_found")
	_, err = repository.UpdateProject(ctx, user.ID, id, 1, func(item *domain.Project) {
		item.Name, item.IsDefault = "renamed", false
	})
	assertDomainCode(t, err, "protected_project")
	assertDomainCode(t, repository.DeleteProject(ctx, user.ID, id, 1), "protected_project")
	updated, err := repository.UpdateProject(ctx, user.ID, id, 1, func(item *domain.Project) {
		item.Description = "notes"
	})
	if err != nil || !updated.IsDefault || updated.Name != domain.DefaultProjectName || updated.Description != "notes" || updated.Version != 2 {
		t.Fatalf("default project normal fields update: %+v, err=%v", updated, err)
	}
	assertDomainCode(t, repository.DeleteProject(ctx, user.ID, id, updated.Version), "protected_project")
}

func TestSQLiteDefaultProjectFormerNameUpdatedOnceOnReopen(t *testing.T) {
	for _, name := range []string{"不使用项目", " 不使用项目 ", "custom default name"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "app.db")
			repository, err := OpenSQLite(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = repository.Close() })
			user, computer, err := repository.LocalIdentity()
			if err != nil {
				t.Fatal(err)
			}
			id := domain.DefaultProjectID(computer.ID)
			ordinary, err := repository.CreateProject(ctx, user.ID, domain.Project{
				ComputerID: computer.ID, Name: name, Description: "ordinary notes",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.db.ExecContext(ctx, `
				UPDATE projects SET name = ?, description = 'keep notes', theme = ?, version = 7, updated_at = ? WHERE id = ?`,
				name, "chamber", sqliteTimeValue(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)), id); err != nil {
				t.Fatal(err)
			}
			before, err := repository.GetProject(ctx, user.ID, id)
			if err != nil {
				t.Fatal(err)
			}
			for attempt := range 2 {
				if err := repository.Close(); err != nil {
					t.Fatal(err)
				}
				repository, err = OpenSQLite(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				after, err := repository.GetProject(ctx, user.ID, id)
				if err != nil {
					t.Fatal(err)
				}
				want := before
				if attempt == 0 && name == "不使用项目" {
					want.Name, want.Version, want.UpdatedAt = "默认项目", before.Version+1, after.UpdatedAt
					if !after.UpdatedAt.After(before.UpdatedAt) {
						t.Fatal("actual rename did not update timestamp")
					}
				}
				if !reflect.DeepEqual(after, want) {
					t.Fatalf("startup changed protected project unexpectedly: got=%+v want=%+v", after, want)
				}
				gotOrdinary, err := repository.GetProject(ctx, user.ID, ordinary.ID)
				if err != nil || !reflect.DeepEqual(gotOrdinary, ordinary) {
					t.Fatalf("startup changed ordinary same-name project: %+v, err=%v", gotOrdinary, err)
				}
				before = after
			}
			if name == "不使用项目" {
				// A reopened default remains editable through its normal fields.
				if _, err := repository.UpdateProject(ctx, user.ID, id, before.Version, func(item *domain.Project) {
					item.Description = "edited after rename"
				}); err != nil {
					t.Fatalf("renamed built-in project cannot be saved: %v", err)
				}
			}
			if err := repository.DeleteProject(ctx, user.ID, ordinary.ID, ordinary.Version); err != nil {
				t.Fatalf("ordinary former-name project must remain deletable: %v", err)
			}
		})
	}
}

func TestSQLiteDefaultProjectBusinessDataSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	repository, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	id := domain.DefaultProjectID(computer.ID)
	issue, err := repository.CreateIssue(ctx, user.ID, domain.Issue{ProjectID: id, Title: "default issue"})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := repository.CreateMemory(ctx, user.ID, domain.ProjectMemoryEntry{ProjectID: id, Key: "fact", Kind: "fact", Title: "Default", Content: "retained", SourceType: "user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := repository.CreateResource(ctx, user.ID, domain.ProjectResource{ProjectID: id, Kind: "url", Name: "Default", URL: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := repository.CreateScheduledTask(ctx, user.ID, domain.ScheduledTask{ProjectID: id, Name: "Default", ScheduleKind: "cron", ScheduleExpression: "0 9 * * *", Timezone: "Asia/Shanghai", Prompt: []domain.ContentBlock{{Type: "text", Text: "review"}}, ConcurrencyPolicy: "forbid", MisfirePolicy: "run_once"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	issues, err := repository.ListIssues(ctx, user.ID, id)
	if err != nil || len(issues) != 1 || issues[0].ID != issue.ID {
		t.Fatalf("restored default issues: %+v, err=%v", issues, err)
	}
	memories, err := repository.ListMemories(ctx, user.ID, id)
	if err != nil || len(memories) != 1 || memories[0].ID != memory.ID || memories[0].Content != "retained" {
		t.Fatalf("restored default memories: %+v, err=%v", memories, err)
	}
	resources, err := repository.ListResources(ctx, user.ID, id)
	if err != nil || len(resources) != 1 || resources[0].ID != resource.ID {
		t.Fatalf("restored default resources: %+v, err=%v", resources, err)
	}
	gotTask, err := repository.GetScheduledTask(ctx, user.ID, task.ID)
	if err != nil || gotTask.ProjectID != id {
		t.Fatalf("restored default schedule: %+v, err=%v", gotTask, err)
	}
}
