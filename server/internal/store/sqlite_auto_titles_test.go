package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func autoTitleFixture(t *testing.T) sqliteExecutionFixture {
	t.Helper()
	f := newSQLiteExecutionFixture(t)
	lumi, err := f.repository.EnsureGeneralAgent(t.Context(), f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.repository.ConfigureGeneralAgent(t.Context(), f.user.ID, f.service.ID, "title-model", lumi.Version); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.repository.Close() })
	return f
}

func titleTransaction(t *testing.T, s *SQLite, fn func(*sql.Tx) error) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func titleState(t *testing.T, s *SQLite, kind, id string) autoTitleState {
	t.Helper()
	var result autoTitleState
	titleTransaction(t, s, func(tx *sql.Tx) error {
		var err error
		var exists bool
		result, exists, err = readAutoTitleTx(t.Context(), tx, kind, id)
		if !exists && err == nil {
			t.Fatal("missing state")
		}
		return err
	})
	return result
}

func queueFixtureTitle(t *testing.T, f sqliteExecutionFixture) {
	t.Helper()
	titleTransaction(t, f.repository, func(tx *sql.Tx) error {
		if err := initAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID); err != nil {
			return err
		}
		return queueAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID, "首条内容")
	})
}

func claimFixtureTitle(t *testing.T, s *SQLite) AutoTitleClaim {
	t.Helper()
	claim, ok, err := s.ClaimAutoTitle(t.Context(), time.Now(), 3*time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	return claim
}

func TestAutoTitleFirstSeedOneClaimAndNoUserHistoryPollution(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	counts := func() []int {
		var result []int
		for _, table := range []string{"runs", "assistant_sessions", "assistant_messages", "issue_comments", "command_outbox"} {
			var n int
			if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			result = append(result, n)
		}
		return result
	}
	before := counts()
	queueFixtureTitle(t, f)
	titleTransaction(t, s, func(tx *sql.Tx) error {
		return queueAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID, "后续内容不替换首条")
	})
	claim := claimFixtureTitle(t, s)
	if claim.Request.Seed != "首条内容" || claim.Request.Model != "title-model" || claim.Request.Provider != f.service.Provider {
		t.Fatalf("wrong actual Lumi snapshot: %+v", claim.Request)
	}
	if _, ok, err := s.ClaimAutoTitle(t.Context(), time.Now(), time.Minute); err != nil || ok {
		t.Fatal("duplicate claim", err)
	}
	if err := s.CompleteAutoTitle(t.Context(), claim, "修复并发问题"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAutoTitle(t.Context(), claim, "迟到重复结果"); err != nil {
		t.Fatal(err)
	}
	issue, err := s.GetIssue(t.Context(), f.user.ID, f.issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Title != "修复并发问题" || issue.Version != f.issue.Version+1 {
		t.Fatalf("title not applied exactly once: %+v", issue)
	}
	state := titleState(t, s, "issue", f.issue.ID)
	if state.Mode != "generated" || state.Seed != "" || state.LeaseToken != "" {
		t.Fatalf("terminal seed retained: %+v", state)
	}
	after := counts()
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("hidden task polluted business tables: %v -> %v", before, after)
		}
	}
}

func TestAutoTitleManualSameValueAndReturnToDefaultAlwaysWins(t *testing.T) {
	for _, when := range []string{"before_claim", "during_claim", "after_apply"} {
		t.Run(when, func(t *testing.T) {
			f := autoTitleFixture(t)
			s := f.repository
			queueFixtureTitle(t, f)
			var claim AutoTitleClaim
			if when != "before_claim" {
				claim = claimFixtureTitle(t, s)
			}
			if when == "after_apply" {
				if err := s.CompleteAutoTitle(t.Context(), claim, "自动标题"); err != nil {
					t.Fatal(err)
				}
			}
			titleTransaction(t, s, func(tx *sql.Tx) error {
				if _, err := tx.Exec(`UPDATE issues SET title=? WHERE id=?`, f.issue.Title, f.issue.ID); err != nil {
					return err
				}
				return manualAutoTitleTx(t.Context(), tx, "issue", f.issue.ID)
			})
			if when != "before_claim" {
				if active, err := s.AutoTitleClaimActive(t.Context(), claim); err != nil || active {
					t.Fatal("manual claim still active", err)
				}
				if err := s.CompleteAutoTitle(t.Context(), claim, "覆盖用户的错误标题"); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok, err := s.ClaimAutoTitle(t.Context(), time.Now(), time.Minute); err != nil || ok {
				t.Fatal("manual title was reclaimed", err)
			}
			issue, err := s.GetIssue(t.Context(), f.user.ID, f.issue.ID)
			if err != nil || issue.Title != f.issue.Title {
				t.Fatal("manual title lost", err)
			}
			state := titleState(t, s, "issue", f.issue.ID)
			if state.Mode != "manual" || state.Epoch != 2 || state.Seed != "" {
				t.Fatalf("manual state=%+v", state)
			}
		})
	}
}

func TestAutoTitleNonTitleVersionsDoNotRevokeOwnership(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	queueFixtureTitle(t, f)
	claim := claimFixtureTitle(t, s)
	if _, err := s.db.Exec(`UPDATE issues SET version=version+7,description='new body' WHERE id=?`, f.issue.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAutoTitle(t.Context(), claim, "保留正文"); err != nil {
		t.Fatal(err)
	}
	issue, err := s.GetIssue(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || issue.Title != "保留正文" || issue.Description != "new body" {
		t.Fatalf("wide update lost body: %+v %v", issue, err)
	}
}

func TestAutoTitleCrashLeaseFencesOldAttemptAndRetriesBoundedly(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	queueFixtureTitle(t, f)
	old := claimFixtureTitle(t, s)
	// Simulate process death: no in-memory task is needed to recover the claim.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, ok, err := reopened.ClaimAutoTitle(t.Context(), time.Now(), time.Minute); err != nil || ok {
		t.Fatal("unexpired lease reclaimed", err)
	}
	current, ok, err := reopened.ClaimAutoTitle(t.Context(), time.Now().Add(4*time.Minute), 3*time.Minute)
	if err != nil || !ok || current.LeaseToken == old.LeaseToken {
		t.Fatal("expired attempt did not receive new fence", err)
	}
	if err := reopened.CompleteAutoTitle(t.Context(), old, "旧进程迟到"); err != nil {
		t.Fatal(err)
	}
	issue, _ := reopened.GetIssue(t.Context(), f.user.ID, f.issue.ID)
	if issue.Title != f.issue.Title {
		t.Fatal("old attempt applied")
	}
	if err := reopened.FailAutoTitle(t.Context(), current, "raw secret must not persist"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := reopened.ClaimAutoTitle(t.Context(), time.Now().Add(8*time.Minute), time.Minute); err != nil || ok {
		t.Fatal("unlimited attempts", err)
	}
	state := titleState(t, reopened, "issue", f.issue.ID)
	if state.Mode != "failed" || state.Attempt != 2 || state.Seed != "" || strings.Contains(state.Failure, "secret") {
		t.Fatalf("failure not bounded/private: %+v", state)
	}
}

func TestAutoTitleConcurrentWorkersOnlyOneWins(t *testing.T) {
	f := autoTitleFixture(t)
	queueFixtureTitle(t, f)
	var wg sync.WaitGroup
	claims := make(chan AutoTitleClaim, 8)
	for range 8 {
		wg.Go(func() {
			claim, ok, err := f.repository.ClaimAutoTitle(context.Background(), time.Now(), time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				claims <- claim
			}
		})
	}
	wg.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatalf("claims=%d", len(claims))
	}
}

func TestAutoTitleRetryPinsActualLumiRevisionAndOldLeaseCannotApply(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	queueFixtureTitle(t, f)
	first := claimFixtureTitle(t, s)
	lumi, err := s.EnsureGeneralAgent(t.Context(), f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfigureGeneralAgent(t.Context(), f.user.ID, f.service.ID, "changed-model", lumi.Version); err != nil {
		t.Fatal(err)
	}
	if err = s.FailAutoTitle(t.Context(), first, "generation_failed"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimAutoTitle(t.Context(), time.Now(), time.Minute); err != nil || ok {
		t.Fatal("retry ignored backoff", err)
	}
	second, ok, err := s.ClaimAutoTitle(t.Context(), time.Now().Add(6*time.Second), time.Minute)
	if err != nil || !ok || second.Request.Model != "title-model" || second.Request.ExecutionID == first.Request.ExecutionID {
		t.Fatalf("retry silently changed Lumi identity/config: %+v %v", second.Request, err)
	}
	// Expired without a replacement worker is still no longer entitled to write.
	titleTransaction(t, s, func(tx *sql.Tx) error {
		state, _, err := readAutoTitleTx(t.Context(), tx, "issue", f.issue.ID)
		if err != nil {
			return err
		}
		state.LeaseUntil = time.Now().Add(-time.Second)
		return saveAutoTitleTx(t.Context(), tx, state)
	})
	if err := s.CompleteAutoTitle(t.Context(), second, "过期结果"); err != nil {
		t.Fatal(err)
	}
	issue, err := s.GetIssue(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || issue.Title != f.issue.Title {
		t.Fatal("expired lease applied", err)
	}
}

func TestAutoTitleWrongOwnerAndExpiredQueueFailClosed(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	queueFixtureTitle(t, f)
	claim := claimFixtureTitle(t, s)
	forged := claim
	forged.UserID = "other-user"
	if err := s.CompleteAutoTitle(t.Context(), forged, "跨用户结果"); err != nil {
		t.Fatal(err)
	}
	issue, err := s.GetIssue(t.Context(), f.user.ID, f.issue.ID)
	if err != nil || issue.Title != f.issue.Title {
		t.Fatal("forged owner applied", err)
	}
	if err := s.FailAutoTitle(t.Context(), claim, "generation_failed"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimAutoTitle(t.Context(), time.Now().Add(31*time.Minute), time.Minute); err != nil || ok {
		t.Fatal("expired queue invoked model", err)
	}
	if state := titleState(t, s, "issue", f.issue.ID); state.Mode != "cancelled" || state.Seed != "" {
		t.Fatalf("expired queue retained seed: %+v", state)
	}
}

func TestAutoTitleLegacyAndEmptyAndSensitiveContentDoNotCallModel(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	titleTransaction(t, s, func(tx *sql.Tx) error {
		return queueAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID, "legacy")
	})
	if _, ok, err := s.ClaimAutoTitle(t.Context(), time.Now(), time.Minute); err != nil || ok {
		t.Fatal("legacy title acquired automatic eligibility", err)
	}
	titleTransaction(t, s, func(tx *sql.Tx) error {
		if err := initAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID); err != nil {
			return err
		}
		return queueAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID, " \n\t ")
	})
	if state := titleState(t, s, "issue", f.issue.ID); state.Mode != "awaiting" {
		t.Fatalf("empty content queued: %+v", state)
	}
	titleTransaction(t, s, func(tx *sql.Tx) error {
		return queueAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID, "example api_key=not-a-real-key")
	})
	if _, ok, err := s.ClaimAutoTitle(t.Context(), time.Now(), time.Minute); err != nil || ok {
		t.Fatal("sensitive seed queued", err)
	}
	if state := titleState(t, s, "issue", f.issue.ID); state.Mode != "failed" || state.Seed != "" {
		t.Fatal("sensitive content retained")
	}
}

func TestAutoTitleUnavailableLumiKeepsDefaultWithoutRetryStorm(t *testing.T) {
	f := newSQLiteExecutionFixture(t)
	defer f.repository.Close()
	queueFixtureTitle(t, f)
	if _, ok, err := f.repository.ClaimAutoTitle(t.Context(), time.Now(), time.Minute); err != nil || ok {
		t.Fatal("unconfigured Lumi claimed", err)
	}
	state := titleState(t, f.repository, "issue", f.issue.ID)
	if state.Mode != "failed" || state.Failure != "lumi_unavailable" || state.Seed != "" {
		t.Fatalf("wrong unavailable state: %+v", state)
	}
}

func TestAutoTitleDeletedTargetAndDeletedParentCannotBeRevived(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "target", true: "parent"}[parent], func(t *testing.T) {
			f := autoTitleFixture(t)
			s := f.repository
			queueFixtureTitle(t, f)
			claim := claimFixtureTitle(t, s)
			query := `UPDATE issues SET deleted_at=? WHERE id=?`
			id := f.issue.ID
			if parent {
				query = `UPDATE projects SET deleted_at=? WHERE id=?`
				id = f.project.ID
			}
			if _, err := s.db.Exec(query, sqliteTimeValue(time.Now()), id); err != nil {
				t.Fatal(err)
			}
			if active, err := s.AutoTitleClaimActive(t.Context(), claim); err != nil || active {
				t.Fatal("deleted target active", err)
			}
			if err := s.CompleteAutoTitle(t.Context(), claim, "不得复活"); err != nil {
				t.Fatal(err)
			}
			if state := titleState(t, s, "issue", f.issue.ID); state.Mode != "cancelled" || state.Seed != "" {
				t.Fatalf("deleted state=%+v", state)
			}
		})
	}
}

func TestAutoTitleTransactionRollbackAndCorruptionFailClosed(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = initAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID); err != nil {
		t.Fatal(err)
	}
	if err = queueAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID, "rollback"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM app_metadata WHERE key GLOB 'auto_title:v1:*'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("ghost job", err)
	}
	bad := `{"schema":2,"mode":"queued"}`
	if _, err = s.db.Exec(`INSERT INTO app_metadata(key,value) VALUES(?,?)`, autoTitleKey("issue", f.issue.ID), bad); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ClaimAutoTitle(t.Context(), time.Now(), time.Minute); err == nil {
		t.Fatal("corrupt job was silently reset")
	}
}

func TestAutoTitleDeletedFirstCommentCancelsPendingResult(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	comment, err := s.CreateIssueComment(t.Context(), f.user.ID, domain.IssueComment{
		IssueID: f.issue.ID, AuthorKind: domain.IssueCommentAuthorUser, AuthorUserID: f.user.ID, Body: "first content for naming",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	titleTransaction(t, s, func(tx *sql.Tx) error {
		if err := initAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID); err != nil {
			return err
		}
		return queueAutoTitleTx(t.Context(), tx, f.user.ID, "issue", f.issue.ID, comment.Body)
	})
	claim := claimFixtureTitle(t, s)
	if _, err := s.db.Exec(`UPDATE issue_comments SET deleted_at=? WHERE id=?`, sqliteTimeValue(time.Now()), comment.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := s.AutoTitleClaimActive(t.Context(), claim); err != nil || active {
		t.Fatal("deleted source still active", err)
	}
	if err := s.CompleteAutoTitle(t.Context(), claim, "不能泄漏已删除内容"); err != nil {
		t.Fatal(err)
	}
	if state := titleState(t, s, "issue", f.issue.ID); state.Mode != "cancelled" || state.Seed != "" {
		t.Fatalf("deleted source retained: %+v", state)
	}
}

func TestAutoTitleNormalConversationSendBindsSourceBeforeClaim(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	graph, err := s.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID}, domain.ConversationNode{AgentID: f.agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := s.SendConversationMessage(t.Context(), f.user.ID, graph.Nodes[0].ID, "source-fence", "真实首消息")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ kind, id string }{{"conversation_node", graph.Nodes[0].ID}, {"conversation_graph", graph.Graph.ID}} {
		state := titleState(t, s, target.kind, target.id)
		if state.SourceID != message.ID || state.SourceKind != "conversation_message" {
			t.Fatalf("normal send did not retain real source identity: %+v", state)
		}
	}
	var graphClaim AutoTitleClaim
	for range 2 {
		claim := claimFixtureTitle(t, s)
		if claim.Kind == "conversation_graph" {
			graphClaim = claim
		}
	}
	if _, err := s.db.Exec(`UPDATE conversation_messages SET deleted_at=? WHERE id=?`, sqliteTimeValue(time.Now()), message.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := s.AutoTitleClaimActive(t.Context(), graphClaim); err != nil || active {
		t.Fatal("deleted source still allowed graph naming", err)
	}
	if err := s.CompleteAutoTitle(t.Context(), graphClaim, "删除内容不应复活"); err != nil {
		t.Fatal(err)
	}
	if state := titleState(t, s, "conversation_graph", graph.Graph.ID); state.Mode != "cancelled" || state.Seed != "" {
		t.Fatalf("source deletion not cancelled: %+v", state)
	}
}

func TestAutoTitleArchiveAndImmediateRestoreCannotReviveAttempt(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	memory, err := s.CreateMemory(t.Context(), f.user.ID, domain.ProjectMemoryEntry{ProjectID: f.project.ID, Key: "archive-title", Kind: "fact", Content: "首份记忆内容", SourceType: "user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	claim := claimFixtureTitle(t, s)
	if err := s.ArchiveMemory(t.Context(), f.user.ID, f.project.ID, memory.ID, memory.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreMemory(t.Context(), f.user.ID, f.project.ID, memory.ID, memory.Version+1); err != nil {
		t.Fatal(err)
	}
	if active, err := s.AutoTitleClaimActive(t.Context(), claim); err != nil || active {
		t.Fatal("archive/restore revived old claim", err)
	}
	if err := s.CompleteAutoTitle(t.Context(), claim, "归档前的迟到标题"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimAutoTitle(t.Context(), time.Now().Add(4*time.Minute), time.Minute); err != nil || ok {
		t.Fatal("restored memory retried cancelled title", err)
	}
	current, err := s.GetMemory(t.Context(), f.user.ID, f.project.ID, memory.ID)
	if err != nil || current.Title != memory.Title {
		t.Fatal("restored title overwritten", err)
	}
	if state := titleState(t, s, "memory", memory.ID); state.Mode != "cancelled" || state.Seed != "" {
		t.Fatalf("archive did not durably cancel: %+v", state)
	}
}

func TestAutoTitleObservedDeletionIsDurableEvenIfTargetReappears(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	queueFixtureTitle(t, f)
	claim := claimFixtureTitle(t, s)
	if _, err := s.db.Exec(`UPDATE issues SET deleted_at=? WHERE id=?`, sqliteTimeValue(time.Now()), f.issue.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := s.AutoTitleClaimActive(t.Context(), claim); err != nil || active {
		t.Fatal("deleted claim active", err)
	}
	if _, err := s.db.Exec(`UPDATE issues SET deleted_at=NULL WHERE id=?`, f.issue.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := s.AutoTitleClaimActive(t.Context(), claim); err != nil || active {
		t.Fatal("observed deletion was forgotten", err)
	}
	if state := titleState(t, s, "issue", f.issue.ID); state.Mode != "cancelled" || state.Seed != "" {
		t.Fatalf("deletion was not persisted: %+v", state)
	}
}

func TestAutoTitleOutputValidationRejectsUnsafeTitles(t *testing.T) {
	for _, title := range []string{"", " padded", "line\nbreak", strings.Repeat("字", 61), "<script>", "title\u202e", "a\x00b", "a\u2028b", "```json", "[link](https://example.com)", string([]byte{0xff})} {
		if validAutoTitle(title) {
			t.Fatalf("accepted invalid title %q", title)
		}
	}
	if !validAutoTitle("实现无感自动命名") {
		t.Fatal("rejected ordinary title")
	}
}

func TestAutoTitleAllEntityKindsApplyNarrowlyAndMemoryHasRevision(t *testing.T) {
	f := autoTitleFixture(t)
	s := f.repository
	graph, err := s.CreateConversationGraph(t.Context(), f.user.ID, domain.ConversationGraph{ProjectID: f.project.ID, Title: "existing graph"}, domain.ConversationNode{AgentID: f.agent.ID, Title: "existing node"})
	if err != nil {
		t.Fatal(err)
	}
	note, err := s.CreateInspiration(t.Context(), f.user.ID, "title-note", domain.Inspiration{Title: "existing note", Markdown: "body"})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := s.CreateMemory(t.Context(), f.user.ID, domain.ProjectMemoryEntry{ProjectID: f.project.ID, Key: "title-memory", Kind: "fact", Title: "existing memory", Content: "body", SourceType: "user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateScheduledTask(t.Context(), f.user.ID, domain.ScheduledTask{ProjectID: f.project.ID, Name: "existing task", ScheduleKind: "cron", ScheduleExpression: "0 9 * * *", Timezone: "Asia/Shanghai", Prompt: []domain.ContentBlock{{Type: "text", Text: "body"}}, ConcurrencyPolicy: "forbid", MisfirePolicy: "run_once"})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ kind, id string }{{"issue", f.issue.ID}, {"conversation_graph", graph.Graph.ID}, {"conversation_node", graph.Nodes[0].ID}, {"inspiration", note.ID}, {"memory", memory.ID}, {"schedule", task.ID}} {
		t.Run(target.kind, func(t *testing.T) {
			titleTransaction(t, s, func(tx *sql.Tx) error {
				if err := initAutoTitleTx(t.Context(), tx, f.user.ID, target.kind, target.id); err != nil {
					return err
				}
				return queueAutoTitleTx(t.Context(), tx, f.user.ID, target.kind, target.id, "seed")
			})
			claim := claimFixtureTitle(t, s)
			if claim.EntityID != target.id {
				t.Fatal("wrong target")
			}
			if err := s.CompleteAutoTitle(t.Context(), claim, "新主题"); err != nil {
				t.Fatal(err)
			}
			if state := titleState(t, s, target.kind, target.id); state.Mode != "generated" {
				t.Fatal("not applied")
			}
		})
	}
	versions, err := s.ListMemoryVersions(t.Context(), f.user.ID, f.project.ID, memory.ID)
	if err != nil || len(versions) != 2 || versions[1].Title != "新主题" || versions[1].Content != "body" {
		t.Fatalf("memory history=%+v err=%v", versions, err)
	}
	var raw string
	if err = s.db.QueryRow(`SELECT value FROM app_metadata WHERE key=?`, autoTitleKey("memory", memory.ID)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal([]byte(raw), &fields) != nil || fields["seed"] != nil {
		t.Fatal("terminal raw seed retained")
	}
}
