package store

import (
	"reflect"
	"testing"
	"time"
)

func TestSQLiteGeneralAgentDefaultNoteWithoutConfiguration(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	agent, err := f.repository.EnsureGeneralAgent(t.Context(), f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Note != generalAgentNote || agent.Version != 1 {
		t.Fatalf("initial built-in defaults: %+v", agent)
	}
	if agent.AgentServiceID != "" || agent.Model != "" || agent.CurrentRevisionID != "" || agent.SystemRole != "general" {
		t.Fatalf("seeding note configured the built-in Agent: %+v", agent)
	}
	if err := f.repository.Close(); err != nil {
		t.Fatal(err)
	}
	f.repository, err = OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.repository.EnsureGeneralAgent(t.Context(), f.user.ID)
	if err != nil || !reflect.DeepEqual(again, agent) {
		t.Fatalf("reopen changed initial defaults: %+v, err=%v", again, err)
	}
}

func TestSQLiteGeneralAgentMigratesOnlyGeneratedReadOnlyNote(t *testing.T) {
	ctx := t.Context()
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	agent, err := f.repository.EnsureGeneralAgent(ctx, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.db.ExecContext(ctx,
		`UPDATE agents SET note = ?, version = version + 1 WHERE id = ?`,
		legacyReadOnlyGeneralAgentNote, agent.ID); err != nil {
		t.Fatal(err)
	}
	before, err := f.repository.GetAgent(ctx, f.user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.repository.EnsureGeneralAgent(ctx, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Note != generalAgentNote || after.Version != before.Version+1 {
		t.Fatalf("generated read-only note was not migrated once: before=%+v after=%+v", before, after)
	}
	again, err := f.repository.EnsureGeneralAgent(ctx, f.user.ID)
	if err != nil || !reflect.DeepEqual(again, after) {
		t.Fatalf("repeated ensure changed the migrated note: %+v, err=%v", again, err)
	}
	custom := "我的自定义 Lumi 备注"
	if _, err := f.repository.db.ExecContext(ctx,
		`UPDATE agents SET note = ?, version = version + 1 WHERE id = ?`, custom, agent.ID); err != nil {
		t.Fatal(err)
	}
	kept, err := f.repository.EnsureGeneralAgent(ctx, f.user.ID)
	if err != nil || kept.Note != custom {
		t.Fatalf("custom note was overwritten: %+v, err=%v", kept, err)
	}
}

func TestSQLiteGeneralAgentNoteDefaultsPreserveConfigurationAcrossReopen(t *testing.T) {
	for _, note := range []string{"", " \t\n\u3000", "custom note", "  keep custom whitespace  "} {
		t.Run(note, func(t *testing.T) {
			ctx := t.Context()
			f := newSQLiteAgentTestFixture(t)
			t.Cleanup(func() { _ = f.repository.Close() })
			agent, err := f.repository.EnsureGeneralAgent(ctx, f.user.ID)
			if err != nil {
				t.Fatal(err)
			}
			installed := installSkillUninstallTestPackage(t, f)
			name, model, prompt := "Roomie", "custom-model", "keep custom instructions"
			agent, err = f.repository.UpdateConfiguredAgent(ctx, f.user.ID, agent.ID, agent.Version, AgentConfigurationPatch{
				Name: &name, AgentServiceID: &f.service.ID, Model: &model, SystemPrompt: &prompt,
				SkillVersionIDs: []string{installed.SkillPackageVersionID},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.repository.db.ExecContext(ctx, `UPDATE agents SET note = ?, updated_at = ? WHERE id = ?`,
				note, sqliteTimeValue(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)), agent.ID); err != nil {
				t.Fatal(err)
			}
			before, err := f.repository.GetAgent(ctx, f.user.ID, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := f.repository.GetAgentRevision(ctx, f.user.ID, agent.CurrentRevisionID)
			if err != nil {
				t.Fatal(err)
			}
			skills, err := f.repository.ListAgentRevisionSkillPackages(ctx, f.user.ID, revision.ID)
			if err != nil || len(skills) != 1 {
				t.Fatalf("initial Skills: %+v, err=%v", skills, err)
			}
			for attempt := range 2 {
				if err := f.repository.Close(); err != nil {
					t.Fatal(err)
				}
				f.repository, err = OpenSQLite(ctx, f.path)
				if err != nil {
					t.Fatal(err)
				}
				after, err := f.repository.EnsureGeneralAgent(ctx, f.user.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := before
				if attempt == 0 && (note == "" || note == " \t\n\u3000") {
					want.Note, want.Version, want.UpdatedAt = generalAgentNote, before.Version+1, after.UpdatedAt
					if !after.UpdatedAt.After(before.UpdatedAt) {
						t.Fatal("default note fill did not update timestamp")
					}
					_, err := f.repository.UpdateConfiguredAgent(ctx, f.user.ID, agent.ID, before.Version, AgentConfigurationPatch{Note: &note})
					assertDomainCode(t, err, "version_conflict")
				}
				if !reflect.DeepEqual(after, want) {
					t.Fatalf("ensure changed unrelated fields or note more than once: got=%+v want=%+v", after, want)
				}
				gotRevision, err := f.repository.GetAgentRevision(ctx, f.user.ID, revision.ID)
				if err != nil || !reflect.DeepEqual(gotRevision, revision) {
					t.Fatalf("ensure changed prompt/model/revision: %+v, err=%v", gotRevision, err)
				}
				gotSkills, err := f.repository.ListAgentRevisionSkillPackages(ctx, f.user.ID, revision.ID)
				if err != nil || !reflect.DeepEqual(gotSkills, skills) {
					t.Fatalf("ensure changed Skills: %+v, err=%v", gotSkills, err)
				}
				again, err := f.repository.EnsureGeneralAgent(ctx, f.user.ID)
				if err != nil || !reflect.DeepEqual(again, after) {
					t.Fatalf("repeated ensure changed Agent: %+v, err=%v", again, err)
				}
				before = after
			}
		})
	}
}
