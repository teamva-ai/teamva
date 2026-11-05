package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	storepkg "github.com/teamva-ai/teamva/server/internal/store"
	"github.com/gin-gonic/gin"
)

// --- Agents ---

func (a *API) listAgents(c *gin.Context) {
	items, err := a.store.ListAgents(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.Agent]{Items: items}, 0)
}

func (a *API) createAgent(c *gin.Context) {
	var request struct {
		AgentServiceID  string   `json:"agent_service_id" binding:"required"`
		Model           string   `json:"model"`
		Name            string   `json:"name" binding:"required"`
		Note            string   `json:"note"`
		SystemPrompt    string   `json:"system_prompt"`
		SkillVersionIDs []string `json:"skill_version_ids"`
	}
	if !bindJSON(c, &request) {
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		respondError(c, domain.Invalid("Agent 名称不能为空"))
		return
	}
	userID := authenticatedUser(c).ID
	item, err := a.store.CreateConfiguredAgent(c.Request.Context(), userID, domain.Agent{
		ComputerID:     c.Param("computer_id"),
		AgentServiceID: request.AgentServiceID,
		Model:          strings.TrimSpace(request.Model),
		Name:           name,
		Note:           strings.TrimSpace(request.Note),
	}, request.SystemPrompt, request.SkillVersionIDs)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) updateAgent(c *gin.Context) {
	var request struct {
		Name            *string  `json:"name"`
		AgentServiceID  *string  `json:"agent_service_id"`
		Note            *string  `json:"note"`
		Status          *string  `json:"status"`
		Model           *string  `json:"model"`
		SystemPrompt    *string  `json:"system_prompt"`
		SkillVersionIDs []string `json:"skill_version_ids"`
		ExpectedVersion int64    `json:"expected_version" binding:"required,min=1"`
	}
	if !bindJSON(c, &request) {
		return
	}
	userID := authenticatedUser(c).ID
	item, err := a.store.UpdateConfiguredAgent(c.Request.Context(), userID, c.Param("agent_id"), request.ExpectedVersion, storepkg.AgentConfigurationPatch{
		Name: request.Name, Note: request.Note, Status: request.Status, Model: request.Model, AgentServiceID: request.AgentServiceID,
		SystemPrompt: request.SystemPrompt, SkillVersionIDs: request.SkillVersionIDs,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) deleteAgent(c *gin.Context) {
	expectedVersion, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	if err := a.store.DeleteAgent(c.Request.Context(), authenticatedUser(c).ID, c.Param("agent_id"), expectedVersion); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) getAgent(c *gin.Context) {
	item, err := a.store.GetAgent(c.Request.Context(), authenticatedUser(c).ID, c.Param("agent_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, item, item.Version)
}

func (a *API) getAgentRevisions(c *gin.Context) {
	userID := authenticatedUser(c).ID
	revision, err := a.store.GetAgentRevision(c.Request.Context(), userID, c.Param("revision_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	skills, err := a.store.ListAgentRevisionSkillPackages(c.Request.Context(), userID, revision.ID)
	if err != nil {
		respondError(c, err)
		return
	}
	skillPackages := make(map[string]domain.SkillPackage, len(skills))
	for _, binding := range skills {
		version, err := a.store.GetSkillPackageVersion(c.Request.Context(), userID, binding.SkillPackageVersionID)
		if err != nil {
			respondError(c, err)
			return
		}
		item, err := a.store.GetSkillPackage(c.Request.Context(), userID, version.SkillPackageID)
		if err != nil {
			respondError(c, err)
			return
		}
		// The response is keyed by the immutable version ID used by the binding.
		// This keeps the editor able to render package metadata even after a newer
		// package version has been published.
		skillPackages[binding.SkillPackageVersionID] = item
	}
	respond(c, http.StatusOK, map[string]any{
		"revision":       revision,
		"skills":         skills,
		"skill_packages": skillPackages,
	}, revision.Version)
}

// --- Skill packages (store) ---

func (a *API) listSkillPackages(c *gin.Context) {
	items, err := a.store.ListSkillPackages(c.Request.Context(), authenticatedUser(c).ID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.SkillPackage]{Items: items}, 0)
}

func (a *API) listComputerSkillPackages(c *gin.Context) {
	if _, err := a.store.GetComputer(c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id")); err != nil {
		respondError(c, err)
		return
	}
	items, err := a.store.ListComputerSkillPackages(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id"),
	)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, domain.Page[domain.ComputerSkillPackage]{Items: items}, 0)
}

func (a *API) installComputerSkillPackage(c *gin.Context) {
	var request struct {
		SkillPackageID string `json:"skill_package_id" binding:"required"`
	}
	if !bindJSON(c, &request) {
		return
	}
	userID := authenticatedUser(c).ID
	computerID := c.Param("computer_id")
	// Resolve the target before invoking the machine-wide installation relay.
	// An empty scoped installation list does not prove that a computer exists or
	// belongs to the caller; checking only at persistence is too late.
	if _, err := a.store.GetComputer(c.Request.Context(), userID, computerID); err != nil {
		respondError(c, err)
		return
	}
	packageItem, err := a.store.GetSkillPackage(c.Request.Context(), userID, request.SkillPackageID)
	if err != nil {
		respondError(c, err)
		return
	}
	executor, ok := a.runtime.(localCommandExecutor)
	if !ok {
		respondError(c, &domain.Error{
			Code: "capability_unsupported", Message: "当前运行模式暂不支持安装本机 Skill 制品",
			HTTPStatus: http.StatusNotImplemented,
		})
		return
	}
	// Reinstall an existing immutable version at the same commit.
	var pinnedRevision string
	installations, err := a.store.ListComputerSkillPackages(c.Request.Context(), userID, computerID)
	if err != nil {
		respondError(c, err)
		return
	}
	for _, installation := range installations {
		if installation.SkillPackageID != packageItem.ID {
			continue
		}
		version, versionErr := a.store.GetSkillPackageVersion(c.Request.Context(), userID, installation.SkillPackageVersionID)
		if versionErr != nil {
			respondError(c, versionErr)
			return
		}
		pinnedRevision = version.CommitSHA
		break
	}
	raw, err := executor.ExecuteLocalCommand(c.Request.Context(), "skill.install", nodeprotocol.InstallSkillPayload{
		Slug: packageItem.Slug, SourceURL: packageItem.SourceURL, Revision: pinnedRevision,
		IncludePaths: storepkg.BuiltinSkillIncludePaths(packageItem.Slug),
	})
	if err != nil {
		respondError(c, err)
		return
	}
	var installedArtifact nodeprotocol.InstallSkillResult
	if err := json.Unmarshal(raw, &installedArtifact); err != nil {
		respondError(c, err)
		return
	}
	if installedArtifact.Slug != packageItem.Slug || installedArtifact.CommitSHA == "" ||
		installedArtifact.ContentDigest == "" || installedArtifact.FileCount <= 0 {
		respondError(c, domain.Invalid("本机返回了无效的 Skill 安装结果"))
		return
	}
	item, err := a.store.InstallComputerSkillPackageVersion(
		c.Request.Context(), userID, computerID, packageItem.ID,
		domain.SkillPackageVersion{
			SkillPackageID: packageItem.ID, CommitSHA: installedArtifact.CommitSHA,
			ContentDigest: installedArtifact.ContentDigest, Manifest: installedArtifact.Manifest,
			FileCount: installedArtifact.FileCount, SizeBytes: installedArtifact.SizeBytes,
			ExecutableFiles: installedArtifact.ExecutableFiles,
			ProviderInstall: map[string]any{"layout": "teamva-skill-v1"},
		},
	)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusCreated, item, item.Version)
}

func (a *API) uninstallComputerSkillPackage(c *gin.Context) {
	expectedVersion, ok := expectedVersionFromQuery(c)
	if !ok {
		return
	}
	uninstaller, ok := a.store.(skillArtifactUninstaller)
	if !ok {
		respondError(c, skillUninstallUnsupported())
		return
	}
	// The store holds its write transaction throughout this filesystem-only
	// callback. Do not query the repository from inside it.
	if err := uninstaller.UninstallComputerSkillPackageWithArtifact(
		c.Request.Context(), authenticatedUser(c).ID, c.Param("computer_id"),
		c.Param("installation_id"), expectedVersion, a.removeSkillArtifact,
	); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) openComputerSkillPackage(c *gin.Context) {
	userID := authenticatedUser(c).ID
	computerID := c.Param("computer_id")
	installations, err := a.store.ListComputerSkillPackages(c.Request.Context(), userID, computerID)
	if err != nil {
		respondError(c, err)
		return
	}
	var packageID string
	for _, installation := range installations {
		if installation.ID == c.Param("installation_id") {
			packageID = installation.SkillPackageID
			break
		}
	}
	if packageID == "" {
		respondError(c, domain.NotFound("本机 Skill", c.Param("installation_id")))
		return
	}
	item, err := a.store.GetSkillPackage(c.Request.Context(), userID, packageID)
	if err != nil {
		respondError(c, err)
		return
	}
	executor, ok := a.runtime.(localCommandExecutor)
	if !ok {
		respondError(c, &domain.Error{
			Code: "capability_unsupported", Message: "当前运行模式无法打开本机 Skill 目录",
			HTTPStatus: http.StatusNotImplemented,
		})
		return
	}
	result, err := executor.ExecuteLocalCommand(c.Request.Context(), "skill.open", map[string]any{"slug": item.Slug})
	if err != nil {
		respondError(c, err)
		return
	}
	var opened any
	if err := json.Unmarshal(result, &opened); err != nil {
		respondError(c, err)
		return
	}
	respond(c, http.StatusOK, opened, 0)
}
