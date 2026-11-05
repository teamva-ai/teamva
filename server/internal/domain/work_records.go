package domain

import (
	"context"
	"encoding/json"
)

type workOperationKey struct{}

func WithWorkOperation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, workOperationKey{}, id)
}
func WorkOperationID(ctx context.Context) string {
	id, _ := ctx.Value(workOperationKey{}).(string)
	return id
}

type WorkRecord struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Action        string         `json:"action"`
	ActorType     string         `json:"actor_type"`
	ActorID       string         `json:"actor_id"`
	ObjectType    string         `json:"object_type"`
	ObjectID      string         `json:"object_id"`
	ObjectVersion int64          `json:"object_version"`
	Title         string         `json:"title"`
	ProjectID     string         `json:"project_id"`
	RunID         string         `json:"run_id"`
	ParentID      string         `json:"parent_id"`
	RequestID     string         `json:"request_id"`
	ClientID      string         `json:"client_id"`
	PageID        string         `json:"page_id"`
	EntryType     string         `json:"entry_type"`
	Sequence      int64          `json:"sequence"`
	Outcome       string         `json:"outcome"`
	OccurredAt    string         `json:"occurred_at"`
	ReceivedAt    string         `json:"received_at"`
	FinishedAt    string         `json:"finished_at"`
	Refs          map[string]any `json:"refs"`
	Before        map[string]any `json:"before"`
	After         map[string]any `json:"after"`
	ErrorCode     string         `json:"error_code"`
}

type WorkCondition struct {
	Join       string          `json:"join,omitempty"`
	Conditions []WorkCondition `json:"conditions,omitempty"`
	Field      string          `json:"field,omitempty"`
	Operator   string          `json:"operator,omitempty"`
	Value      string          `json:"value,omitempty"`
}
type WorkQuery struct {
	Search         string         `json:"search,omitempty"`
	From           string         `json:"from,omitempty"`
	Until          string         `json:"until,omitempty"`
	Filter         *WorkCondition `json:"filter,omitempty"`
	IncludeChanges bool           `json:"include_changes,omitempty"`
	Offset         int            `json:"offset,omitempty"`
	Limit          int            `json:"limit,omitempty"`
}
type WorkPage struct {
	Items []WorkRecord `json:"items"`
	Total int          `json:"total"`
}
type WorkFilter struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Query WorkQuery `json:"query"`
}
type AssistantSession struct {
	Scope     AssistantScope `json:"scope"`
	ID        string         `json:"id"`
	AgentID   string         `json:"agent_id"`
	Title     string         `json:"title"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
}
type AssistantMessage struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	AuthorKind string          `json:"author_kind"`
	Body       string          `json:"body"`
	RunID      string          `json:"run_id"`
	Query      json.RawMessage `json:"query"`
	CreatedAt  string          `json:"created_at"`
	Status     string          `json:"status"`
}
