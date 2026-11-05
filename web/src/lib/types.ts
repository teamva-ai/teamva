export interface Versioned {
  id: string;
  version: number;
  created_at: string;
  updated_at: string;
  deleted_at?: string;
}

export interface User extends Versioned {
  email: string;
  display_name: string;
  status: string;
}

export type SupportedLocale =
  | "zh-CN"
  | "zh-TW"
  | "en-US"
  | "ja-JP"
  | "de-DE"
  | "fr-FR"
  | "es-ES"
  | "pt-BR"
  | "ru-RU";

export interface UserPreferences {
  user_id: string;
  locale: SupportedLocale;
  locale_explicit: boolean;
  theme: "system" | "light" | "dark";
  appearance?: import("./appearanceThemes").AppearancePreferences;
  allow_write: boolean;
  version: number;
}

export type UserPreferencesPatch = Partial<
  Pick<UserPreferences, "locale" | "theme" | "allow_write" | "appearance">
>;

export interface NetworkAccess {
  external_access: boolean;
  external_urls: string[];
  port: number;
  password_set: boolean;
  can_manage: boolean;
  listen_error: boolean;
}

export interface Onboarding {
  dismissed: boolean;
  needed: boolean;
}

export interface RuntimeCapabilities {
  mode: "local" | "remote" | string;
  auth: Record<string, boolean>;
  topology: Record<string, boolean>;
  runtime: Record<string, boolean>;
  filesystem: Record<string, boolean>;
  database: Record<string, string>;
}

export interface Project extends Versioned {
  computer_id: string;
  name: string;
  readonly is_default?: boolean;
  description?: string;
}

export interface Computer extends Versioned {
  user_id: string;
  name: string;
  note?: string;
  platform: string;
  architecture: string;
  fingerprint: string;
  status: string;
  last_seen_at?: string;
  computer_use_enabled: boolean;
}

export interface AgentService extends Versioned {
  computer_id: string;
  provider: string;
  provider_version: string;
  login_status: string;
  status: string;
  capabilities: Record<string, unknown>;
  capability_version: number;
	last_probed_at?: string;
}

export interface ProgrammingToolDiagnosticStep {
  id: string;
  label: string;
  status: "success" | "failed" | "warning" | "skipped" | string;
  message: string;
  detail?: string;
  duration_ms?: number;
}

export interface ProgrammingToolDiagnostic {
  provider: string;
  provider_version?: string;
  available: boolean;
  authenticated: boolean;
  status: "connected" | "needs_auth" | "unavailable" | string;
  unavailable_reason_code?: string;
  capabilities?: Record<string, unknown>;
  capability_version: number;
  models?: string[];
  started_at: string;
  finished_at: string;
  steps: ProgrammingToolDiagnosticStep[];
}

/** One Provider's CLI resolution: custom override plus what the runtime uses. */
export interface ProgrammingToolExecutable {
  provider: string;
  /** User-configured path; empty means automatic resolution. */
  path: string;
  /** Effective executable path or command name. */
  resolved: string;
  source: "custom" | "environment" | "bundled" | "path" | string;
}

// --- 本机 Agent 配置体系 ---

export interface Agent extends Versioned {
	 system_role?: "general";
  computer_id: string;
  agent_service_id: string;
  model?: string;
  name: string;
  note?: string;
  status: "active" | "disabled";
  current_revision_id?: string;
}

export interface AgentRevision extends Versioned {
  agent_id: string;
  agent_service_id: string;
  model?: string;
  system_prompt?: string;
}

export interface AgentRevisionSkillPackage {
  id: string;
  agent_revision_id: string;
  skill_package_version_id: string;
  position: number;
}

export interface SkillPackage extends Versioned {
  slug: string;
  name: string;
  description: string;
  source_url?: string;
  license?: string;
  builtin: boolean;
}

export interface SkillPackageVersion extends Versioned {
  skill_package_id: string;
  commit_sha: string;
  content_digest: string;
  manifest: Record<string, unknown>;
  file_count: number;
  size_bytes: number;
  executable_files?: string[];
  provider_install?: Record<string, unknown>;
}

export interface ComputerUseBinaryStatus {
  installed: boolean;
  version?: string;
  path?: string;
  error?: string;
}

export interface ComputerUseStatus {
  binary: ComputerUseBinaryStatus;
  npm_path?: string;
  platform: string;
  supported: boolean;
  errors?: string[];
}

export interface ComputerUseInstallResult {
  status: ComputerUseStatus;
  output: string;
}

export interface ComputerSkillPackage extends Versioned {
  computer_id: string;
  skill_package_id: string;
  skill_package_version_id: string;
}

// --- Issue（任务）体系 ---

export type IssueStatus = "open" | "resolved" | "closed";

export interface Issue extends Versioned {
  project_id: string;
  title: string;
  description?: string;
  status: IssueStatus;
  worktree_hint: boolean;
  created_by_user_id: string;
}

export interface IssueComment extends Versioned {
  issue_id: string;
  author_kind: "user" | "agent";
  author_user_id?: string;
  author_agent_id?: string;
  body: string;
  run_id?: string;
  status: "working" | "published" | "failed";
  edited_at?: string;
  work_items?: IssueWorkItem[];
}

export interface IssueWorkItemSpec {
  key: string;
  agent_id: string;
  title: string;
  instruction: string;
  after: string[];
}

export interface IssueWorkItem extends IssueWorkItemSpec {
  status: string;
  status_reason?: string;
  mention_id?: string;
  run_id?: string;
  issue_agent_instance_id?: string;
  result_comment_id?: string;
  blocked_by?: string[];
}

export interface IssueCommentMention {
  id: string;
  comment_id: string;
  target_kind: "user" | "agent";
  target_agent_id?: string;
  consumed_at?: string;
}

export interface IssueCommentRevision {
  id: string;
  comment_id: string;
  body: string;
  created_at: string;
}

export type AgentInstanceStatus =
  | "idle"
  | "running"
  | "paused"
  | "queued"
  | "resource_exhausted";

export interface IssueAgentInstance extends Versioned {
  issue_id: string;
  agent_id: string;
  agent_revision_id: string;
  status: AgentInstanceStatus;
  last_activity_at?: string;
  pending_mention_count: number;
}

export interface IssueAgentGeneration extends Versioned {
  issue_agent_instance_id: string;
  agent_revision_id: string;
  native_session_key?: string;
}

export interface ProjectResource extends Versioned {
  project_id: string;
  kind: "code_directory" | "document_directory" | "url";
  name: string;
  path?: string;
  url?: string;
  status: string;
  git_detected?: boolean;
}

export interface ResourceOpener {
  id: string;
  kind: "file_manager" | "terminal" | "ide" | string;
  name: string;
  available: boolean;
}

export interface ResourceOpeners {
  platform: "darwin" | "windows" | "linux" | string;
  items: ResourceOpener[];
}

export interface ContentBlock {
  type: "text" | "image";
  text?: string;
  mime_type?: string;
  filename?: string;
  data_url?: string;
  /** Internal control instruction sent to the provider but hidden from the user-message timeline. */
  internal?: boolean;
}

export interface Run extends Versioned {
	conversation_node_id?: string;
	assistant_session_id?: string;
  issue_id: string;
  issue_agent_instance_id?: string;
  issue_agent_generation_id?: string;
  agent_id?: string;
  project_id: string;
  status: string;
  agent_service_id?: string;
  model?: string;
  read_only: boolean;
  lease_epoch: number;
  input: ContentBlock[];
  started_at?: string;
  finished_at?: string;
  terminal_reason?: string;
  goal_id?: string;
  purpose: "work" | "goal_continuation" | "goal_audit" | "goal_finalize" | string;
}

export type IssueAgentGoalStatus =
  | "active"
  | "paused"
  | "blocked"
  | "budget_reached"
  | "completed"
  | "cancelled";

export interface IssueAgentGoal extends Versioned {
  issue_id: string;
  issue_agent_instance_id: string;
  agent_id: string;
  agent_revision_id: string;
  objective: string;
  status: IssueAgentGoalStatus;
  phase: "running" | "evaluating" | "finalizing" | "dispatching" | "settled";
  token_budget: number | null;
  tokens_used: number;
  continuations_used: number;
  max_continuations: number;
  blocked_streak: number;
  audit_failure_streak: number;
  finalize_attempts: number;
  note?: string;
  status_reason?: string;
  summary_comment_id: string;
  current_run_id?: string;
  last_work_run_id?: string;
  settled_at?: string;
}

export interface CurrentIssueAgentGoal {
  goal?: IssueAgentGoal | null;
}

export interface RunEvent {
  id: string;
  run_id: string;
  seq: number;
  event_type: string;
  payload: Record<string, unknown>;
  occurred_at: string;
}

export interface MemoryEntry extends Versioned {
  project_id: string;
  key: string;
  kind: string;
  title: string;
  content: string;
  source_type: string;
  source_ref?: string;
}

export type ProjectSearchKind =
  | "memory"
  | "issue_description"
  | "issue_comment"
  | "session_message";

export interface ProjectSearchResult {
  id: string;
  kind: ProjectSearchKind;
  project_id: string;
  title: string;
  excerpt: string;
  memory_id?: string;
  issue_id?: string;
  comment_id?: string;
  issue_agent_instance_id?: string;
  run_id?: string;
  role?: "user" | "assistant";
  anchor?: string;
  occurred_at: string;
}

export interface ProjectSearchPage {
  items: ProjectSearchResult[];
  truncated: boolean;
}

export interface Approval extends Versioned {
  project_id: string;
  issue_id: string;
  run_id: string;
  status: string;
  action_kind: string;
  action_digest: string;
  normalized_action: Record<string, unknown>;
  risk: string;
  expires_at: string;
  decision_scope?: string;
  decided_at?: string;
  provider_approval_ref?: string;
}

export interface PolicyRule extends Versioned {
  project_id: string;
  action_kind: string;
  effect: string;
  conditions: Record<string, unknown>;
  enabled: boolean;
}

export interface ScheduledTask extends Versioned {
  project_id: string;
  name: string;
  enabled: boolean;
  schedule_kind: "daily" | "weekly" | "cron" | "once";
  schedule_expression: string;
  times?: string[];
  weekdays?: number[];
  issue_mode?: "new" | "existing";
  issue_id?: string;
  timezone: string;
  prompt: ContentBlock[];
  agent_id?: string;
  concurrency_policy: string;
  misfire_policy: string;
  next_run_at?: string;
  dispatch_attempts?: number;
  dispatch_retry_at?: string;
  last_dispatch_error?: string;
}

export interface ScheduledTaskRequest {
  name: string;
  schedule_kind: "daily" | "weekly" | "cron" | "once";
  schedule_expression: string;
  times?: string[];
  weekdays?: number[];
  issue_mode?: "new" | "existing";
  issue_id?: string;
  timezone: string;
  prompt: ContentBlock[];
  agent_id: string;
  concurrency_policy: "forbid" | "allow";
  misfire_policy: "skip" | "run_once";
  expected_version?: number;
}

export interface ScheduledTaskRun {
  id: string;
  scheduled_task_id: string;
  run_id?: string;
  issue_id?: string;
  trigger_key: string;
  scheduled_for: string;
  status: string;
  created_at: string;
}

export interface AuditEvent {
  id: string;
  action: string;
  outcome: string;
  actor_type: string;
  details: Record<string, unknown>;
  occurred_at: string;
}

export interface Page<T> {
  items: T[];
  next_cursor?: string;
}

export interface Envelope<T> {
  data: T;
  meta: {
    request_id: string;
    resource_version?: number;
    replayed?: boolean;
  };
}

// 图对话：与 Issue 完全独立的一套协作实体。
export interface ConversationGraph extends Versioned {
  project_id: string;
  computer_id: string;
  title: string;
  node_count: number;
}

export type ConversationNodeStatus = "idle" | "running" | "paused" | "failed";

export interface ConversationAgentBinding {
  expected_agent_id?: string;
  expected_agent_revision_id?: string;
}

export interface CreateConversationGraphRequest {
  title?: string;
  agent_id?: string;
  node_title?: string;
}

export interface InitializeConversationGraphRootRequest {
  agent_id: string;
  expected_version: number;
}

export interface UpdateConversationGraphRequest {
  title: string;
  expected_version: number;
}

export interface UpdateConversationNodeRequest {
  expected_version: number;
  title?: string;
  agent_id?: string;
  position_x?: number;
  position_y?: number;
  width?: number;
  height?: number;
}

export interface CreateConversationMessageRequest extends ConversationAgentBinding {
  body: string;
}

export interface CreateConversationGoalRequest extends ConversationAgentBinding {
  objective: string;
  token_budget?: number | null;
}

export interface ConversationNode extends Versioned {
  graph_id: string;
  parent_id?: string;
  fork_message_id?: string;
  agent_id: string;
  agent_revision_id?: string;
  title: string;
  status: ConversationNodeStatus;
  position_x: number;
  position_y: number;
  width: number;
  height: number;
  /** 冻结的分叉点父快照：不能发送消息；两条可继续的子分支不锁定。 */
  locked: boolean;
  issue_id?: string;
  /** 创建分支时冻结的聊天记录快照。 */
  inherited_messages?: Array<{ author: "user" | "agent"; body: string }>;
  message_count: number;
  preview?: string;
}

export interface ConversationMessage extends Versioned {
  node_id: string;
  author_kind: "user" | "agent";
  body: string;
  status: "working" | "published" | "failed";
  run_id?: string;
}

export interface ConversationGraphDetail {
  graph: ConversationGraph;
  nodes: ConversationNode[];
  messages: ConversationMessage[];
  goals: ConversationGoal[];
}

export interface ConversationGoal extends Versioned {
  node_id: string;
  agent_revision_id: string;
  objective: string;
  status: "active" | "paused" | "blocked" | "budget_reached" | "completed" | "cancelled";
  phase: "running" | "evaluating" | "settled";
  token_budget: number | null;
  tokens_used: number;
  work_runs: number;
  run_count: number;
  max_work_runs: number;
  current_run_id?: string;
  last_work_run_id?: string;
  note: string;
  status_reason: string;
}
