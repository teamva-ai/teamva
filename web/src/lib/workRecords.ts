import { v4 } from "uuid";
export interface WorkCondition {
  join?: "and" | "or";
  conditions?: WorkCondition[];
  field?: string;
  operator?: string;
  value?: string;
}
export interface WorkQuery {
  search?: string;
  from?: string;
  until?: string;
  filter?: WorkCondition;
  include_changes?: boolean;
  offset?: number;
  limit?: number;
}
export interface WorkRecord {
  id: string;
  kind: string;
  action: string;
  actor_type: string;
  actor_id: string;
  object_type: string;
  object_id: string;
  object_version: number;
  title: string;
  project_id: string;
  run_id: string;
  parent_id: string;
  request_id: string;
  client_id: string;
  page_id: string;
  entry_type: string;
  sequence: number;
  outcome: string;
  occurred_at: string;
  received_at: string;
  finished_at: string;
  error_code: string;
  refs: Record<string, unknown>;
  before: Record<string, unknown>;
  after: Record<string, unknown>;
}
export interface WorkLink {
  kind: string;
  id: string;
  title: string;
  url: string;
  deleted: boolean;
}
export interface WorkDetail {
  record: WorkRecord;
  children: WorkRecord[];
  links: WorkLink[];
}
export interface WorkPage {
  items: WorkRecord[];
  total: number;
}
export interface WorkFilter {
  id: string;
  name: string;
  query: WorkQuery;
}
export interface AssistantSession {
  id: string;
  agent_id: string;
  title: string;
  created_at: string;
  updated_at: string;
}
export interface AssistantMessage {
  id: string;
  session_id: string;
  author_kind: string;
  body: string;
  run_id: string;
  status: string;
  created_at: string;
}
export const workFields = [
  "action",
  "project_id",
  "object_type",
  "object_id",
  "actor_type",
  "outcome",
  "client_id",
  "page_id",
  "entry_type",
  "agent_id",
  "graph_id",
  "node_id",
  "issue_id",
  "fork_message_id",
  "parent_id",
  "agent_revision_id",
  "run_id",
  "request_id",
  "title",
  "id",
  "from_project_id",
  "to_project_id",
  "from_issue_id",
  "to_issue_id",
  "from_graph_id",
  "to_graph_id",
  "from_node_id",
  "to_node_id"
];

export function cleanWorkCondition(value: WorkCondition): WorkCondition | undefined {
  if (value.join) {
    const conditions = (value.conditions ?? [])
      .map(cleanWorkCondition)
      .filter((v): v is WorkCondition => Boolean(v));
    return conditions.length ? { join: value.join, conditions } : undefined;
  }
  return value.field && value.operator && value.value
    ? { field: value.field, operator: value.operator, value: value.value }
    : undefined;
}

// Non-authentication identifiers; never reuse any session/device credential.
let clientID = "";
const pageID = v4();
export function workClientHeaders(): Record<string, string> {
  if (!clientID) {
    try {
      clientID = localStorage.getItem("teamva-work-client") ?? "";
    } catch {
      /* private storage */
    }
    if (!/^[a-zA-Z0-9_-]{1,100}$/.test(clientID)) clientID = v4();
    try {
      localStorage.setItem("teamva-work-client", clientID);
    } catch {
      /* memory identifier */
    }
  }
  const desktop =
    typeof window !== "undefined" &&
    (window.location.protocol === "wails:" || ("runtime" in window && "go" in window));
  return {
    "X-Work-Client": clientID,
    "X-Work-Page": pageID,
    "X-Work-Entry": desktop ? "wails" : "browser"
  };
}
