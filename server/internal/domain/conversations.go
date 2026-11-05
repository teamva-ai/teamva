package domain

// 图对话（Conversation Graph）是与 Issue 完全独立的一套协作形态：
// 一个项目下有多棵树，每棵树由若干单 Agent 对话节点组成，节点之间通过
// 「基于某条 Agent 最终消息分叉」连接。图对话只共用项目、Resource、Agent
// 配置和项目记忆，不共用评论、mention、实例和 Goal 表。

const (
	ConversationNodeIdle    = "idle"
	ConversationNodeRunning = "running"
	ConversationNodePaused  = "paused"
	ConversationNodeFailed  = "failed"

	ConversationMessageWorking   = "working"
	ConversationMessagePublished = "published"
	ConversationMessageFailed    = "failed"

	ConversationAuthorUser  = "user"
	ConversationAuthorAgent = "agent"

	ConversationTitleLimit   = 200
	ConversationMessageLimit = 20000
)

// ConversationGraph 是项目下的一棵图对话。
type ConversationGraph struct {
	Versioned
	ProjectID  string `json:"project_id"`
	ComputerID string `json:"computer_id"`
	Title      string `json:"title"`
	NodeCount  int    `json:"node_count"`
}

// ConversationNode 是图里的一个方形节点：一段单 Agent 对话。
type ConversationNode struct {
	Versioned
	GraphID         string `json:"graph_id"`
	ParentID        string `json:"parent_id,omitempty"`
	ForkMessageID   string `json:"fork_message_id,omitempty"`
	AgentID         string `json:"agent_id"`
	AgentRevisionID string `json:"agent_revision_id,omitempty"`
	Title           string `json:"title"`
	// TitleEdited distinguishes an explicit same-value title PATCH from layout edits.
	TitleEdited bool    `json:"-"`
	Status      string  `json:"status"`
	PositionX   float64 `json:"position_x"`
	PositionY   float64 `json:"position_y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	// Locked 表示节点已经成为分支历史：不能再发送消息，只能从它的消息
	// 继续创建分支。
	Locked  bool   `json:"locked"`
	IssueID string `json:"issue_id,omitempty"`
	// InheritedMessages 是创建分支时冻结的上下文快照，供界面展示分支
	// 携带的聊天记录。
	InheritedMessages []ConversationContextEntry `json:"inherited_messages,omitempty"`
	MessageCount      int                        `json:"message_count"`
	Preview           string                     `json:"preview,omitempty"`
}

// ConversationAgentBinding is optional for legacy clients. New clients bind a
// send/goal to the Agent and revision they actually displayed.
type ConversationAgentBinding struct {
	ExpectedAgentID         string `json:"expected_agent_id,omitempty"`
	ExpectedAgentRevisionID string `json:"expected_agent_revision_id,omitempty"`
}

// ConversationContextEntry 是分支继承上下文中的一条历史消息。
type ConversationContextEntry struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

// ConversationMessage 是节点里的一条消息：用户输入或 Agent 最终回复。
// 一次 Run 里的流式输出和工具调用在完成后合并为一条 Agent 消息。
type ConversationMessage struct {
	Versioned
	NodeID     string `json:"node_id"`
	AuthorKind string `json:"author_kind"`
	Body       string `json:"body"`
	Status     string `json:"status"`
	RunID      string `json:"run_id,omitempty"`
}

// ConversationGraphDetail 是图页面的完整快照：节点 + 每个节点的消息。
// 消息量受图规模限制，超出上限时由消息分页接口按节点拉取。
type ConversationGraphDetail struct {
	Goals    []ConversationGoal    `json:"goals"`
	Graph    ConversationGraph     `json:"graph"`
	Nodes    []ConversationNode    `json:"nodes"`
	Messages []ConversationMessage `json:"messages"`
}

func IsConversationNodeTerminal(status string) bool {
	switch status {
	case ConversationNodeIdle, ConversationNodeFailed:
		return true
	default:
		return false
	}
}
