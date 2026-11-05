package domain

import "time"

type User struct {
	Versioned
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

type Session struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// Token is an internal cookie secret. It must never be serialized into an
	// API response; browsers receive it exclusively through Set-Cookie.
	Token         string    `json:"-"`
	CSRF          string    `json:"csrf_token,omitempty"`
	IdleExpiresAt time.Time `json:"idle_expires_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// SessionMeta carries the browser context recorded with a web session.
type SessionMeta struct {
	DeviceID      string
	IPHMAC        string
	UserAgentHMAC string
}

// WebSessionInfo is the non-secret projection of a web session shown in the
// "logged-in devices" UI. Tokens, CSRF values and raw IPs are never included.
type WebSessionInfo struct {
	ID            string     `json:"id"`
	DeviceID      string     `json:"device_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	RotatedAt     time.Time  `json:"rotated_at"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	IdleExpiresAt time.Time  `json:"idle_expires_at"`
	Current       bool       `json:"current"`
}

// SessionTokenMatch records which server-side token slot authenticated the
// presented browser cookie. Callers must not treat a previous-token match as
// permission to write the presented (stale) token back to the browser.
type SessionTokenMatch string

const (
	SessionTokenMatchCurrent  SessionTokenMatch = "current"
	SessionTokenMatchPrevious SessionTokenMatch = "previous"
)

// SessionAuthentication is the result of atomically authenticating, touching
// and, when due, rotating a browser session. RotatedToken is populated only
// when this request matched the current token and performed the rotation.
type SessionAuthentication struct {
	User         User
	Session      WebSessionInfo
	TokenMatch   SessionTokenMatch
	RotatedToken string
}

type Computer struct {
	Versioned
	UserID          string     `json:"user_id"`
	Name            string     `json:"name"`
	Note            string     `json:"note,omitempty"`
	Platform        string     `json:"platform"`
	Architecture    string     `json:"architecture"`
	Fingerprint     string     `json:"fingerprint"`
	Status          string     `json:"status"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	NodeVersion     string     `json:"node_version,omitempty"`
	ProtocolVersion int        `json:"protocol_version,omitempty"`
	// ComputerUseEnabled 为该计算机启用 open-computer-use 官方 MCP
	// （Accessibility 元素树 + 截图），在每次 Run 的实例配置中注入。
	ComputerUseEnabled bool `json:"computer_use_enabled"`
}

type NodeHeartbeat struct {
	NodeVersion     string `json:"node_version"`
	ProtocolVersion int    `json:"protocol_version"`
}

type AgentService struct {
	Versioned
	ComputerID        string         `json:"computer_id"`
	Provider          string         `json:"provider"`
	ProviderVersion   string         `json:"provider_version"`
	LoginStatus       string         `json:"login_status"`
	Status            string         `json:"status"`
	Capabilities      map[string]any `json:"capabilities"`
	CapabilityVersion int            `json:"capability_version"`
	LastProbedAt      *time.Time     `json:"last_probed_at,omitempty"`
}

type AuditEvent struct {
	ID         string         `json:"id"`
	UserID     string         `json:"user_id,omitempty"`
	ProjectID  string         `json:"project_id,omitempty"`
	IssueID    string         `json:"issue_id,omitempty"`
	RunID      string         `json:"run_id,omitempty"`
	ComputerID string         `json:"computer_id,omitempty"`
	ActorType  string         `json:"actor_type"`
	ActorRef   string         `json:"actor_ref,omitempty"`
	Action     string         `json:"action"`
	Outcome    string         `json:"outcome"`
	Details    map[string]any `json:"details"`
	TraceID    string         `json:"trace_id,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}
