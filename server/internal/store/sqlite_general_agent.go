package store

import (
	"context"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/node/provider/catalog"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

const (
	generalAgentName = "Lumi"
	// The built-in Agent is protected from deletion and kept active. Otherwise
	// it is configured and used like an ordinary Agent, including Issue work
	// and schedules; read-only behavior belongs to explicit assistant sessions.
	generalAgentNote = "内置 Agent，不能被删除；可像普通 Agent 一样配置并参与任务。"
	// This was a generated default before the boundary changed. Only the exact
	// generated text is migrated; custom notes stay untouched.
	legacyReadOnlyGeneralAgentNote = "内置只读助手，帮助回顾工作记录、检索信息和整理灵感。"
	legacyGeneralAgentName         = "通用 Agent"
)

func (s *SQLite) EnsureGeneralAgent(ctx context.Context, userID string) (domain.Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agents(id, computer_id, system_role, name, note, status)
		SELECT 'agt_general_' || id, id, 'general', ?, ?, 'active'
		FROM computers WHERE user_id = ? AND deleted_at IS NULL
		ON CONFLICT DO NOTHING`, generalAgentName, generalAgentNote, userID)
	if err != nil {
		return domain.Agent{}, err
	}
	var id, name, note string
	err = tx.QueryRowContext(ctx, `
		SELECT a.id, a.name, a.note FROM agents a JOIN computers c ON c.id = a.computer_id
		WHERE c.user_id = ? AND a.system_role = 'general' AND a.deleted_at IS NULL AND c.deleted_at IS NULL`, userID).Scan(&id, &name, &note)
	if err != nil {
		return domain.Agent{}, err
	}
	// Refresh only untouched defaults, preserving user notes and immutable
	// configuration revisions (including prompt and Skills).
	renamed := name == legacyGeneralAgentName
	defaultNote := strings.TrimSpace(note) == "" || note == legacyReadOnlyGeneralAgentNote
	if renamed || defaultNote {
		if renamed {
			name = generalAgentName
		}
		if defaultNote {
			note = generalAgentNote
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE agents SET name = ?, note = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			name, note, sqliteTimeValue(time.Now()), id); err != nil {
			return domain.Agent{}, err
		}
	}
	agent, err := getSQLiteAgent(ctx, tx, userID, id)
	if err != nil {
		return domain.Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Agent{}, err
	}
	return agent, nil
}

func (s *SQLite) ConfigureGeneralAgent(ctx context.Context, userID, serviceID, model string, version int64) (domain.Agent, error) {
	agent, err := s.EnsureGeneralAgent(ctx, userID)
	if err != nil {
		return domain.Agent{}, err
	}
	// Fixture/internal convenience only: there is no separate HTTP/UI editor.
	// The common Agent transaction preserves prompt/Skills and owns validation.
	return s.UpdateConfiguredAgent(ctx, userID, agent.ID, version, AgentConfigurationPatch{AgentServiceID: &serviceID, Model: &model})
}

func requireAssistantProvider(providerID string) error {
	definition, ok := catalog.Lookup(providerID)
	if !ok || !definition.Metadata.Supports("read_only") || !definition.Metadata.Supports("file_bridge") {
		return domain.Invalid("Provider 缺少只读工具能力")
	}
	return nil
}
