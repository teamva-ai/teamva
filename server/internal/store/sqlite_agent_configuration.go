package store

import (
	"context"
	"slices"
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

// CreateConfiguredAgent atomically publishes a usable Agent, its immutable
// initial revision and installed Skill bindings. There is no half-created Agent.
func (s *SQLite) CreateConfiguredAgent(ctx context.Context, userID string, input domain.Agent, prompt string, skills []string) (domain.Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	agent, err := createSQLiteAgent(ctx, tx, userID, input)
	if err != nil {
		return domain.Agent{}, err
	}
	_, err = createSQLiteAgentRevisionTx(ctx, tx, userID, domain.AgentRevision{
		AgentID: agent.ID, AgentServiceID: agent.AgentServiceID, Model: agent.Model, SystemPrompt: prompt,
	}, skills, false)
	if err != nil {
		return domain.Agent{}, err
	}
	agent, err = getSQLiteAgent(ctx, tx, userID, agent.ID)
	if err != nil {
		return domain.Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Agent{}, err
	}
	return agent, nil
}

// UpdateConfiguredAgent checks the version, preserves omitted configuration,
// validates installations, and publishes all changes in one write transaction.
func (s *SQLite) UpdateConfiguredAgent(ctx context.Context, userID, id string, version int64, patch AgentConfigurationPatch) (domain.Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	agent, err := getSQLiteAgent(ctx, tx, userID, id)
	if err != nil {
		return domain.Agent{}, err
	}
	if agent.Version != version {
		return domain.Agent{}, versionConflict(agent.Version)
	}
	var revision domain.AgentRevision
	hasRevision := agent.CurrentRevisionID != ""
	if hasRevision {
		if err := scanSQLiteAgentRevision(tx.QueryRowContext(ctx, sqliteAgentRevisionSelect+`
		WHERE revisions.id = ? AND revisions.agent_id = ? AND revisions.deleted_at IS NULL
	`, agent.CurrentRevisionID, id), &revision); err != nil {
			return domain.Agent{}, err
		}
	} else {
		revision = domain.AgentRevision{AgentID: agent.ID, AgentServiceID: agent.AgentServiceID, Model: agent.Model}
	}
	rows, err := tx.QueryContext(ctx, `SELECT skill_package_version_id FROM agent_revision_skill_packages WHERE agent_revision_id=? ORDER BY position`, revision.ID)
	if err != nil {
		return domain.Agent{}, err
	}
	skills := make([]string, 0)
	for rows.Next() {
		var skill string
		if err := rows.Scan(&skill); err != nil {
			rows.Close()
			return domain.Agent{}, err
		}
		skills = append(skills, skill)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.Agent{}, err
	}
	nextService, nextModel, nextPrompt, nextSkills := agent.AgentServiceID, revision.Model, revision.SystemPrompt, skills
	if patch.AgentServiceID != nil {
		nextService = strings.TrimSpace(*patch.AgentServiceID)
	}
	if patch.Model != nil {
		nextModel = strings.TrimSpace(*patch.Model)
	}
	if patch.SystemPrompt != nil {
		nextPrompt = *patch.SystemPrompt
	}
	if patch.SkillVersionIDs != nil {
		nextSkills = patch.SkillVersionIDs
	}
	if len(nextModel) > 500 {
		return domain.Agent{}, domain.Invalid("模型标识过长")
	}
	configurationChanged := !hasRevision || nextService != agent.AgentServiceID || nextModel != revision.Model || nextPrompt != revision.SystemPrompt || !slices.Equal(nextSkills, skills)
	agent, err = updateSQLiteAgent(ctx, tx, userID, id, version, func(item *domain.Agent) {
		if patch.Name != nil {
			item.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Note != nil {
			item.Note = strings.TrimSpace(*patch.Note)
		}
		if patch.Status != nil {
			item.Status = *patch.Status
		}
		item.Model = nextModel
		item.AgentServiceID = nextService
	})
	if err != nil {
		return domain.Agent{}, err
	}
	if configurationChanged {
		_, err = createSQLiteAgentRevisionTx(ctx, tx, userID, domain.AgentRevision{
			AgentID: id, AgentServiceID: agent.AgentServiceID, Model: nextModel, SystemPrompt: nextPrompt,
		}, nextSkills, false)
		if err != nil {
			return domain.Agent{}, err
		}
		agent, err = getSQLiteAgent(ctx, tx, userID, id)
		if err != nil {
			return domain.Agent{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Agent{}, err
	}
	return agent, nil
}
