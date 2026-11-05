package schema

import (
	"fmt"
	"strings"
)

// Explicit per-entity allow-lists. Never serialize entire rows: bodies, prompts,
// arbitrary request payloads, tokens and credentials are not work metadata.
func workRecordTriggers() string {
	type entity struct {
		table, kind, title, owner, project, run string
		fields                                  []string
	}
	computerOwner := `(SELECT user_id FROM computers WHERE id=NEW.computer_id)`
	projectOwner := `(SELECT c.user_id FROM projects p JOIN computers c ON c.id=p.computer_id WHERE p.id=NEW.project_id)`
	nodeProject := `(SELECT project_id FROM conversation_graphs WHERE id=NEW.graph_id)`
	nodeOwner := `(SELECT c.user_id FROM conversation_graphs g JOIN computers c ON c.id=g.computer_id WHERE g.id=NEW.graph_id)`
	messageProject := `(SELECT g.project_id FROM conversation_nodes n JOIN conversation_graphs g ON g.id=n.graph_id WHERE n.id=NEW.node_id)`
	messageOwner := `(SELECT c.user_id FROM conversation_nodes n JOIN conversation_graphs g ON g.id=n.graph_id JOIN computers c ON c.id=g.computer_id WHERE n.id=NEW.node_id)`
	issueProject := `(SELECT project_id FROM issues WHERE id=NEW.issue_id)`
	issueOwner := `(SELECT c.user_id FROM issues i JOIN projects p ON p.id=i.project_id JOIN computers c ON c.id=p.computer_id WHERE i.id=NEW.issue_id)`
	entities := []entity{
		{"inspirations", "inspiration", "title", "NEW.user_id", "COALESCE(NEW.project_id,'')", "''", []string{"project_id", "title", "completed", "completed_at"}},
		{"projects", "project", "name", computerOwner, "NEW.id", "''", []string{"computer_id", "name"}},
		{"agents", "agent", "name", computerOwner, "''", "''", []string{"computer_id", "name", "model", "status", "current_revision_id", "agent_service_id"}},
		{"issues", "issue", "title", projectOwner, "NEW.project_id", "''", []string{"project_id", "title", "status"}},
		{"project_memory_entries", "memory", "title", projectOwner, "NEW.project_id", "''", []string{"project_id", "title", "memory_key", "kind", "current_version", "source_type"}},
		{"policy_rules", "policy", "action_kind", projectOwner, "NEW.project_id", "''", []string{"project_id", "action_kind", "effect", "enabled", "expires_at"}},
		{"project_resources", "resource", "name", projectOwner, "NEW.project_id", "''", []string{"project_id", "name", "kind", "local_reference", "status"}},
		{"scheduled_tasks", "schedule", "name", projectOwner, "NEW.project_id", "''", []string{"project_id", "name", "agent_id", "enabled", "schedule_kind", "schedule_expression", "timezone", "next_run_at"}},
		{"conversation_graphs", "graph", "title", computerOwner, "NEW.project_id", "''", []string{"project_id", "computer_id", "title"}},
		{"conversation_nodes", "node", "title", nodeOwner, nodeProject, "''", []string{"graph_id", "parent_id", "fork_message_id", "agent_id", "agent_revision_id", "title", "status", "locked", "issue_id"}},
		{"conversation_messages", "message", "", messageOwner, messageProject, "COALESCE(NEW.run_id,'')", []string{"node_id", "author_kind", "run_id", "status"}},
		{"issue_comments", "comment", "", issueOwner, issueProject, "COALESCE(NEW.run_id,'')", []string{"issue_id", "author_kind", "author_agent_id", "run_id", "status"}},
		{"runs", "run", "", "COALESCE(" + computerOwner + "," + projectOwner + ")", "COALESCE(NEW.project_id,'')", "NEW.id", []string{"project_id", "issue_id", "agent_id", "issue_agent_instance_id", "issue_agent_generation_id", "conversation_node_id", "assistant_session_id", "provider", "model", "status", "started_at", "finished_at"}},
	}
	var result strings.Builder
	for _, e := range entities {
		jsonFor := func(prefix string) string {
			parts := []string{"'id'", prefix + ".id", "'version'", prefix + ".version"}
			if e.kind == "run" {
				parts = append(parts, "'agent_revision_id'", "json_extract("+prefix+".snapshot,'$.agent_revision_id')")
			}
			for _, field := range e.fields {
				parts = append(parts, "'"+field+"'", prefix+"."+field)
			}
			return "json_object(" + strings.Join(parts, ",") + ")"
		}
		title := "''"
		if e.title != "" {
			title = "NEW." + e.title
		}
		for _, operation := range []string{"INSERT", "UPDATE"} {
			action := "'" + e.kind + ".created'"
			before := "'{}'"
			condition := ""
			if e.kind == "node" && operation == "INSERT" {
				action = "CASE WHEN NEW.parent_id IS NOT NULL THEN 'node.forked' ELSE 'node.created' END"
			}
			if operation == "UPDATE" {
				action = "CASE WHEN NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN '" + e.kind + ".deleted' ELSE '" + e.kind + ".updated' END"
				before = jsonFor("OLD")
				condition = "WHEN NEW.version<>OLD.version OR NEW.deleted_at IS NOT OLD.deleted_at"
				if e.kind == "run" {
					condition = "WHEN NEW.status<>OLD.status"
				}
				if e.kind == "node" {
					condition = "WHEN NEW.deleted_at IS NOT OLD.deleted_at OR NEW.title<>OLD.title OR NEW.locked<>OLD.locked OR NEW.parent_id IS NOT OLD.parent_id OR NEW.fork_message_id IS NOT OLD.fork_message_id"
				}
			}
			actor := "'system'"
			if e.kind == "comment" || e.kind == "message" {
				actor = "NEW.author_kind"
			}
			if e.kind == "run" {
				actor = "'agent'"
			}
			parent := "''"
			if e.run != "''" {
				parent = "COALESCE((SELECT json_extract(snapshot,'$.work_record_id') FROM runs WHERE id=" + e.run + "),'')"
			}
			statement := fmt.Sprintf(`
CREATE TRIGGER work_%s_%s AFTER %s ON %s %s BEGIN
 INSERT INTO work_records(id,user_id,kind,action,actor_type,object_type,object_id,object_version,title,project_id,run_id,parent_id,occurred_at,refs,before_values,after_values)
 VALUES('wrk_'||lower(hex(randomblob(16))),%s,'change',%s,%s,'%s',NEW.id,NEW.version,%s,COALESCE(%s,''),%s,%s,NEW.updated_at,%s,%s,%s);
END;
`, e.table, strings.ToLower(operation), operation, e.table, condition, e.owner, action, actor, e.kind, title, e.project, e.run, parent, jsonFor("NEW"), before, jsonFor("NEW"))
			if e.kind == "memory" {
				statement = strings.ReplaceAll(statement, ".version", ".current_version")
			}
			result.WriteString(statement)
		}
	}
	return result.String()
}
