package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (a *API) queryWorkRecords(c *gin.Context) {
	r := a.store
	var q domain.WorkQuery
	if !bindJSON(c, &q) {
		return
	}
	v, err := r.QueryWorkRecords(c.Request.Context(), authenticatedUser(c).ID, q)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, 200, v, 0)
}
func (a *API) getWorkRecord(c *gin.Context) {
	r := a.store
	user := authenticatedUser(c).ID
	v, err := r.GetWorkRecord(c.Request.Context(), user, c.Param("record_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	children, err := r.WorkRecordChildren(c.Request.Context(), user, v.ID)
	if err != nil {
		respondError(c, err)
		return
	}
	links, err := r.WorkRecordLinks(c.Request.Context(), user, v)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, 200, map[string]any{"record": v, "children": children, "links": links}, 0)
}
func (a *API) clearWorkRecords(c *gin.Context) {
	r := a.store
	var req struct {
		Query   domain.WorkQuery `json:"query"`
		Confirm bool             `json:"confirm"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if !req.Confirm || req.Query.Until == "" {
		respondError(c, domain.Invalid("清除需要确认和明确截止时间"))
		return
	}
	n, err := r.ClearWorkRecords(c.Request.Context(), authenticatedUser(c).ID, req.Query)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, 200, map[string]any{"deleted": n}, 0)
}
func (a *API) listWorkFilters(c *gin.Context) {
	r := a.store
	items, err := r.ListWorkFilters(c.Request.Context(), authenticatedUser(c).ID)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, 200, map[string]any{"items": items}, 0)
}
func (a *API) saveWorkFilter(c *gin.Context) {
	r := a.store
	var req struct {
		ID    string            `json:"id"`
		Name  string            `json:"name"`
		Query *domain.WorkQuery `json:"query" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	f := domain.WorkFilter{ID: req.ID, Name: req.Name, Query: *req.Query}
	v, err := r.SaveWorkFilter(c.Request.Context(), authenticatedUser(c).ID, f)
	if err != nil {
		respondError(c, err)
		return
	}
	respond(c, 200, v, 0)
}
func (a *API) deleteWorkFilter(c *gin.Context) {
	r := a.store
	if err := r.DeleteWorkFilter(c.Request.Context(), authenticatedUser(c).ID, c.Param("filter_id")); err != nil {
		respondError(c, err)
		return
	}
	c.Status(204)
}

type workLocation struct {
	ProjectID string `json:"project_id,omitempty"`
	IssueID   string `json:"issue_id,omitempty"`
	GraphID   string `json:"graph_id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
}

func (location workLocation) metadata() map[string]any {
	values := make(map[string]any, 4)
	for key, value := range map[string]string{
		"project_id": location.ProjectID, "issue_id": location.IssueID,
		"graph_id": location.GraphID, "node_id": location.NodeID,
	} {
		if value != "" {
			values[key] = value
		}
	}
	return values
}

func (a *API) recordWorkNavigation(c *gin.Context) {
	r := a.store
	var req struct {
		ID         string        `json:"id" binding:"required"`
		Sequence   int64         `json:"sequence" binding:"required,min=1"`
		OccurredAt string        `json:"occurred_at" binding:"required"`
		From       *workLocation `json:"from" binding:"required"`
		To         *workLocation `json:"to" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if !validWorkID(req.ID) || req.Sequence < 1 {
		respondError(c, domain.Invalid("导航标识无效"))
		return
	}
	if _, err := time.Parse(time.RFC3339Nano, req.OccurredAt); err != nil {
		respondError(c, domain.Invalid("操作时间无效"))
		return
	}
	for _, loc := range []workLocation{*req.From, *req.To} {
		if len(loc.ProjectID) > 128 || len(loc.IssueID) > 128 || len(loc.GraphID) > 128 || len(loc.NodeID) > 128 {
			respondError(c, domain.Invalid("导航对象标识过长"))
			return
		}
	}
	// 完整且有效的观察才可判定为没有切换；不能用相同位置绕过必填字段或时间校验。
	if *req.From == *req.To {
		c.Status(204)
		return
	}
	user := authenticatedUser(c).ID
	// A navigation observation is not proof of a business mutation. Its type
	// and authenticated owner are server-controlled, never supplied by clients.
	before, after := req.From.metadata(), req.To.metadata()
	record := workClient(c)
	record.ID = "nav_" + user + "_" + req.ID
	record.Kind = "navigation"
	record.Action = "navigation.changed"
	record.ActorType = "user"
	record.ActorID = user
	record.Sequence = req.Sequence
	record.OccurredAt = req.OccurredAt
	record.Outcome = "succeeded"
	record.ProjectID = req.To.ProjectID
	record.Before = map[string]any{"state": before}
	record.After = map[string]any{"state": after}
	record.Refs = after
	record.ObjectType, record.ObjectID = "project", req.To.ProjectID
	if req.To.IssueID != "" {
		record.ObjectType, record.ObjectID = "issue", req.To.IssueID
	}
	if req.To.GraphID != "" {
		record.ObjectType, record.ObjectID = "graph", req.To.GraphID
	}
	if req.To.NodeID != "" {
		record.ObjectType, record.ObjectID = "node", req.To.NodeID
	}
	link := a.store.ResolveWorkLink(c.Request.Context(), user, record.ObjectType, record.ObjectID)
	if link.Deleted {
		respondError(c, domain.Invalid("目标对象不存在或不可访问"))
		return
	}
	parsed, err := url.Parse(link.URL)
	if err != nil || (record.ObjectType != "project" && parsed.Query().Get("project") != req.To.ProjectID) {
		respondError(c, domain.Invalid("导航目标不属于指定项目"))
		return
	}
	record.Title = link.Title
	for _, state := range []map[string]any{before, after} {
		for field, kind := range map[string]string{"project_id": "project", "issue_id": "issue", "graph_id": "graph", "node_id": "node"} {
			if id, ok := state[field].(string); ok && id != "" {
				resolved := a.store.ResolveWorkLink(c.Request.Context(), user, kind, id)
				if !resolved.Deleted {
					state[strings.TrimSuffix(field, "_id")+"_name"] = resolved.Title
				}
			}
		}
	}
	if err := r.AppendWorkRecord(c.Request.Context(), user, record); err != nil {
		respondError(c, err)
		return
	}
	c.Status(204)
}

func validWorkID(v string) bool {
	if v == "" || len(v) > 100 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func workClient(c *gin.Context) domain.WorkRecord {
	r := domain.WorkRecord{RequestID: requestID(c), EntryType: "api"}
	if c.GetHeader("X-Work-Entry") == "wails" {
		r.EntryType = "wails"
	} else if c.GetHeader("X-Work-Entry") == "browser" {
		r.EntryType = "browser"
	}
	if IsRemoteAccess(c.Request) {
		r.EntryType = "remote"
	}
	if v := c.GetHeader("X-Work-Client"); validWorkID(v) {
		r.ClientID = v
	}
	if v := c.GetHeader("X-Work-Page"); validWorkID(v) {
		r.PageID = v
	}
	return r
}

type workCapture struct {
	gin.ResponseWriter
	body     bytes.Buffer
	overflow bool
}

func (w *workCapture) Write(b []byte) (int, error) {
	if !w.overflow {
		if w.body.Len()+len(b) > 1<<20 {
			w.body.Reset()
			w.overflow = true
		} else {
			_, _ = w.body.Write(b)
		}
	}
	return w.ResponseWriter.Write(b)
}
func (w *workCapture) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

var workKinds = map[string]string{"projects": "project", "agents": "agent", "issues": "issue", "comments": "comment", "conversation-graphs": "graph", "conversation-nodes": "node", "messages": "message", "runs": "run", "resources": "resource", "memories": "memory", "scheduled-tasks": "schedule", "policy-rules": "policy", "skill-packages": "skill", "work-assistant": "agent", "sessions": "assistant", "goals": "goal", "agent-instances": "agent_instance", "inspirations": "inspiration"}

func (a *API) captureWorkOperation() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			c.Next()
			return
		}
		parts := strings.Split(strings.Trim(c.FullPath(), "/"), "/")
		kind := ""
		objectID := ""
		action := ""
		for i, part := range parts {
			if k, ok := workKinds[part]; ok {
				kind = k
				objectID = ""
				if i+1 < len(parts) && strings.HasPrefix(parts[i+1], ":") {
					objectID = c.Param(strings.TrimPrefix(parts[i+1], ":"))
				}
			}
		}
		if kind == "" || strings.HasSuffix(c.FullPath(), "/preview") || strings.HasSuffix(c.FullPath(), "/search") {
			c.Next()
			return
		}
		r := a.store
		switch c.Request.Method {
		case "POST":
			action = "create"
		case "PATCH", "PUT":
			action = "update"
		case "DELETE":
			action = "delete"
		default:
			c.Next()
			return
		}
		if last := parts[len(parts)-1]; !strings.HasPrefix(last, ":") {
			if _, ok := workKinds[last]; !ok {
				action = last
			}
		}
		user := authenticatedUser(c).ID
		record := workClient(c)
		// Trace IDs may be shared by retries or unrelated caller requests.
		// Each business operation needs its own server-owned identity.
		record.ID = "op_" + uuid.NewString()
		record.Kind = "operation"
		record.ActorType = "user"
		record.ActorID = user
		record.ObjectType = kind
		record.ObjectID = objectID
		record.Action = kind + "." + action
		record.Outcome = "pending"
		record.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
		record.Refs = map[string]any{}
		if objectID != "" {
			record.Title = a.store.ResolveWorkLink(c.Request.Context(), user, kind, objectID).Title
		}
		for _, p := range c.Params {
			if strings.HasSuffix(p.Key, "_id") {
				record.Refs[p.Key] = p.Value
			}
		}
		if p, ok := record.Refs["project_id"].(string); ok {
			record.ProjectID = p
		}
		if err := r.AppendWorkRecord(c.Request.Context(), user, record); err != nil {
			respondError(c, err)
			c.Abort()
			return
		}
		c.Set("work_input", record.Refs)
		c.Request = c.Request.WithContext(domain.WithWorkOperation(c.Request.Context(), record.ID))
		w := &workCapture{ResponseWriter: c.Writer}
		c.Writer = w
		c.Next()
		record.Outcome = "succeeded"
		if c.Writer.Status() >= 400 {
			record.Outcome = "failed"
		}
		var response struct {
			Data  map[string]any `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if !w.overflow && w.body.Len() != 0 {
			if err := json.Unmarshal(w.body.Bytes(), &response); err != nil {
				slog.Warn("work response metadata unavailable", "record_id", record.ID)
			}
		}
		record.ErrorCode = response.Error.Code
		data := response.Data
		if graph, ok := data["graph"].(map[string]any); ok {
			ids := []string{}
			if nodes, ok := data["nodes"].([]any); ok {
				for _, raw := range nodes {
					if node, ok := raw.(map[string]any); ok {
						if id, ok := node["id"].(string); ok {
							ids = append(ids, id)
						}
					}
				}
			}
			record.Refs["created_node_ids"] = ids
			if len(ids) == 1 {
				record.Refs["node_id"] = ids[0]
			}
			data = graph
		}
		if record.Outcome == "succeeded" {
			safe := workMetadata(data)
			for key, value := range safe {
				record.Refs[key] = value
			}
			if id, ok := data["id"].(string); ok {
				record.ObjectID = id
			}
			if version, ok := data["version"].(float64); ok {
				record.ObjectVersion = int64(version)
			}
			for _, key := range []string{"name", "title"} {
				if value, ok := data[key].(string); ok {
					record.Title = value
				}
			}
		}
		if id, ok := record.Refs["assistant_session_id"].(string); ok && id != "" {
			record.ObjectType = "run"
			record.RunID = record.ObjectID
			record.Action = "assistant.message"
		}
		if p, ok := record.Refs["project_id"].(string); ok {
			record.ProjectID = p
		}
		if record.ObjectType == "project" {
			record.ProjectID = record.ObjectID
		}
		if id, ok := record.Refs["run_id"].(string); ok {
			record.RunID = id
		}
		if kind == "run" {
			record.RunID = record.ObjectID
		}
		if record.Title == "" {
			record.Title = a.store.ResolveWorkLink(c.Request.Context(), user, record.ObjectType, record.ObjectID).Title
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
		defer cancel()
		if err := r.FinishWorkOperation(ctx, user, record); err != nil {
			slog.Warn("work operation enrichment failed; transaction history retained", "record_id", record.ID)
		}
	}
}

func workMetadata(input map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"id", "version", "name", "title", "status", "project_id", "computer_id", "agent_id", "agent_service_id", "agent_revision_id", "current_revision_id", "issue_id", "issue_agent_instance_id", "issue_agent_generation_id", "graph_id", "node_id", "message_id", "fork_message_id", "parent_id", "run_id", "assistant_session_id", "model", "kind", "expected_version"} {
		if v, ok := input[key]; ok {
			switch v.(type) {
			case string, float64, int, int64, bool:
				result[key] = v
			}
		}
	}
	return result
}
