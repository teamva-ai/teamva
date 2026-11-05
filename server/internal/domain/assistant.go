package domain

// AssistantScope separates conversation histories, not user ownership. Every
// scope uses the same protected Lumi and read-only execution boundary.
type AssistantScope string

const (
	AssistantWorkHistory  AssistantScope = "work_history"
	AssistantInspirations AssistantScope = "inspirations"
)

func (scope AssistantScope) Validate() error {
	switch scope {
	case AssistantWorkHistory, AssistantInspirations:
		return nil
	default:
		return Invalid("助手会话范围无效")
	}
}

// AssistantSelection contains references/query metadata only, never copied
// document bodies. Its serialized form is also the idempotency fingerprint.
type AssistantSelection struct {
	Query            WorkQuery         `json:"query"`
	SelectedIDs      []string          `json:"selected_ids"`
	InspirationIDs   []string          `json:"inspiration_ids,omitempty"`
	InspirationQuery *InspirationQuery `json:"inspiration_query,omitempty"`
}

type AssistantMessageRequest struct {
	Body string `json:"body" binding:"required"`
	AssistantSelection
}

const (
	AssistantMessageLimit    = 500
	AssistantContextMessages = 60
	AssistantContextBytes    = 180000
	AssistantQuestionBytes   = 40000
)
