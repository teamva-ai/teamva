package domain

import "time"

// Markdown is the canonical document. No editor JSON/HTML representation is
// persisted alongside it; metadata is independent of the editable body.
type Inspiration struct {
	Versioned
	ProjectID   string     `json:"project_id"`
	Title       string     `json:"title"`
	Markdown    string     `json:"markdown"`
	Completed   bool       `json:"completed"`
	CompletedAt *time.Time `json:"completed_at"`
}
type InspirationCard struct {
	Versioned
	ProjectID   string     `json:"project_id"`
	Title       string     `json:"title"`
	Preview     string     `json:"preview"`
	Completed   bool       `json:"completed"`
	CompletedAt *time.Time `json:"completed_at"`
}
type InspirationQuery struct {
	ProjectID *string `json:"project_id,omitempty"`
	Search    string  `json:"search,omitempty"`
	Status    string  `json:"status,omitempty"`
	Offset    int     `json:"offset,omitempty"`
	Limit     int     `json:"limit,omitempty"`
}
type InspirationPage struct {
	Items []InspirationCard `json:"items"`
	Total int               `json:"total"`
}
type InspirationGroup struct {
	ProjectID      string `json:"project_id"`
	ProjectName    string `json:"project_name"`
	ProjectDeleted bool   `json:"project_deleted"`
	Total          int    `json:"total"`
	Completed      int    `json:"completed"`
}
type InspirationPatch struct {
	ProjectID *string `json:"project_id"`
	Title     *string `json:"title"`
	Markdown  *string `json:"markdown"`
	Completed *bool   `json:"completed"`
}
