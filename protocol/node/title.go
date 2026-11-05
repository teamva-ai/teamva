package nodeprotocol

// TitleRequest is an isolated title-only generation attempt. Tool restraint is
// a prompt policy, not a strict DisableTools requirement. It is not a Run,
// assistant conversation, or resumable native session.
type TitleRequest struct {
	ExecutionID  string `json:"execution_id"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	SystemPrompt string `json:"system_prompt"`
	Seed         string `json:"seed"`
}

type TitleResult struct {
	Title string `json:"title"`
}
