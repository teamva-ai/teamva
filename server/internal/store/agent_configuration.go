package store

// AgentConfigurationPatch preserves omitted fields and treats a non-nil empty
// SkillVersionIDs slice as explicit unbinding. All Agents share this path.
type AgentConfigurationPatch struct {
	Name, Note, Status, Model, SystemPrompt, AgentServiceID *string
	SkillVersionIDs                                         []string
}
