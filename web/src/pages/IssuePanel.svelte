<script lang="ts">
  import { Button, Input, Textarea } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import {
    Bot,
    Check,
    CheckCircle2,
    ChevronDown,
    CircleAlert,
    CircleDot,
    GitCompareArrows,
    LoaderCircle,
    Pencil,
    Send,
    Trash2,
    User,
    X,
    XCircle
  } from "lucide-svelte";
  import { onMount, tick } from "svelte";
  import { t } from "../lib/i18n.svelte";
  import { api, APIError } from "../lib/api";
  import { queryClient } from "../lib/query";
  import {
    beginComposerSubmission, completeComposerSubmission, composerDraftKey, composerIdempotencyKey,
    finishComposerSubmission, readComposerDraft, saveComposerDraft,
    subscribeComposerDraft, type ComposerDraft
  } from "../lib/composerDrafts";
  import {
    canMentionAgent, insertAgentMention, mentionQueryAt, reconcileMentionRanges,
    resolveMentionedAgentIds, type MentionRange
  } from "../lib/mentions";
  import { subscribeIssueEvents } from "../lib/issueEventStream";
  import { maximumIssueWorkItems, newIssueWorkItem, validateIssueWorkItems } from "../lib/issueWork";
  import {
    runChangesInvalidationSignal,
    type RunChangesInvalidationSignal
  } from "../lib/activityEvents";
  import { runChangesBundleQueryKey, runGitChangesQueryKey } from "../lib/runGitChanges";
  import type {
    Agent,
    AgentService,
    Approval,
    Issue,
    IssueAgentInstance,
    IssueComment,
    IssueWorkItem,
    IssueWorkItemSpec,
    Page,
    Run
  } from "../lib/types";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import AgentRunChanges from "../components/AgentRunChanges.svelte";
  import RichText from "../components/RichText.svelte";
  import IssueWorkComposer from "../components/IssueWorkComposer.svelte";
  import IssueWorkItems from "../components/IssueWorkItems.svelte";
  import { formatTime, isTerminal } from "../lib/helpers";
  import { location } from "../lib/router.svelte";
  import type { GitRepositorySummary } from "../lib/gitChanges";
  import AgentSideSheet from "./AgentSideSheet.svelte";

  let {
    issue,
    agents,
    services = [],
    projectId,
    repositories,
    onIssueChange
  }: {
    issue: Issue;
    agents: Agent[];
    services?: AgentService[];
    projectId: string;
    repositories: GitRepositorySummary[];
    onIssueChange: (updated: Issue) => void;
  } = $props();

  // TanStack pauses background intervals; a hidden snapshot would prevent resuming after a hidden mount.
  const instances = createQuery<Page<IssueAgentInstance>>(() => ({
    queryKey: ["issue-instances", issue.id],
    queryFn: () => api<Page<IssueAgentInstance>>(`/issues/${issue.id}/agent-instances`),
    refetchInterval: 2_000
  }));
  const runs = createQuery<Page<Run>>(() => ({
    queryKey: ["issue-runs", issue.id],
    queryFn: () => api<Page<Run>>(`/issues/${issue.id}/runs`),
    refetchInterval: 2_000
  }));
  const comments = createQuery<Page<IssueComment>>(() => {
    const hasActiveRun = runs.data?.items.some((run) => !isTerminal(run.status));
    return {
      queryKey: ["issue-comments", issue.id],
      queryFn: () => api<Page<IssueComment>>(`/issues/${issue.id}/comments`),
      // Ordinary comment edits do not always emit SSE notifications, so idle tasks still poll.
      refetchInterval: (query) => hasActiveRun || query.state.data?.items.some((comment) => comment.status === "working") ? 2_000 : 10_000
    };
  });
  const approvals = createQuery<Page<Approval>>(() => ({
    queryKey: ["approvals", "pending", issue.id],
    queryFn: () => api<Page<Approval>>("/approvals?status=pending"),
    refetchInterval: 1_000
  }));
  const agentName = $derived(
    (agentId: string) => agents.find((item) => item.id === agentId)?.name ?? agentId
  );

  const commentItems = $derived(comments.data?.items ?? []);
  function hasCommentBody(comment: IssueComment): boolean {
    return Boolean(comment.body.trim()) && !/^\[teamva:(?:working|goal-working)\]$/.test(comment.body.trim());
  }
  const publishedCommentCount = $derived(commentItems.filter((comment) =>
    (comment.author_kind === "user" || comment.status === "published") && hasCommentBody(comment)
  ).length);
  const instanceItems = $derived(instances.data?.items ?? []);
  const commentRows = $derived.by(() => {
    const runById = new Map((runs.data?.items ?? []).map((item) => [item.id, item]));
    const instanceById = new Map(instanceItems.map((item) => [item.id, item]));
    return commentItems.map((comment) => {
      const run = comment.run_id ? runById.get(comment.run_id) : undefined;
      const instance = run?.issue_agent_instance_id
        ? instanceById.get(run.issue_agent_instance_id)
        : undefined;
      return { comment, instance };
    });
  });
  const pendingApprovals = $derived(
    (approvals.data?.items ?? []).filter((item) => item.issue_id === issue.id)
  );
  function sessionNumber(instance: IssueAgentInstance): number {
    return (
      instanceItems
        .filter((item) => item.agent_id === instance.agent_id)
        .findIndex((item) => item.id === instance.id) + 1
    );
  }

  function sessionLabel(instance: IssueAgentInstance): string {
    return t("issues.agentSessionLabel", {
      agent: agentName(instance.agent_id),
      index: sessionNumber(instance)
    });
  }

  // 参与的 Agent：同一 Agent 的多个会话合并为一个条目，点击即插入 @ 提及。
  const participantAgents = $derived.by(() => {
    const seen = new Set<string>();
    const items: Agent[] = [];
    for (const instance of instanceItems) {
      if (seen.has(instance.agent_id)) continue;
      const agent = agents.find((item) => item.id === instance.agent_id);
      if (!agent) continue;
      seen.add(instance.agent_id);
      items.push(agent);
    }
    return items;
  });

  function approvalAgentName(item: Approval): string {
    const run = runs.data?.items.find((candidate) => candidate.id === item.run_id);
    return run?.agent_id ? agentName(run.agent_id) : t("issues.agent");
  }

  function approvalSummary(item: Approval): string {
    const action = item.normalized_action ?? {};
    const command = action.command ?? action.cmd;
    if (typeof command === "string" && command.trim()) return command;
    const path = action.path ?? action.file_path ?? action.cwd;
    if (typeof path === "string" && path.trim()) return path;
    return item.action_kind;
  }

  const decideApproval = createMutation<
    Approval,
    Error,
    { item: Approval; decision: "approved" | "denied"; issueId: string }
  >(() => ({
    mutationFn: ({ item, decision }) =>
      api<Approval>(`/approvals/${item.id}/decisions`, {
        body: {
          expected_version: item.version,
          action_digest: item.action_digest,
          decision,
          scope: "once"
        }
      }),
    onSuccess: (_item, input) => {
      queryClient.invalidateQueries({ queryKey: ["approvals"] });
      queryClient.invalidateQueries({ queryKey: ["issue-runs", input.issueId] });
      queryClient.invalidateQueries({ queryKey: ["issue-instances", input.issueId] });
    }
  }));

  // --- 评论输入 + @ 补全 ---
  let prompt = $state("");
  let mentionRanges = $state<MentionRange[]>([]);
  let commentSubmitting = $state(false);
  let composerMode = $state<ComposerDraft["mode"]>("comment");
  let workItems = $state<IssueWorkItemSpec[]>([]);
  let workPlanInput = $state<string | null>(null);
  let workPlanning = $state(false);
  const workValidation = $derived(validateIssueWorkItems(workItems, agents.filter(canMentionAgent).map((agent) => agent.id)));
  const workPlanStale = $derived(workPlanInput !== null && workPlanInput !== prompt);
  const immediateWorkCount = $derived(workItems.filter((item) => !item.after.length).length);
  const draftKey = $derived(composerDraftKey(projectId, issue.id));
  $effect(() => subscribeComposerDraft(draftKey, (draft) => {
    prompt = draft.text;
    mentionRanges = draft.mentions;
    commentSubmitting = draft.submitting;
    composerMode = draft.mode;
    workItems = draft.workItems;
    workPlanInput = draft.workPlanInput;
  }));

  function savePrompt() {
    saveComposerDraft(draftKey, { text: prompt, mentions: mentionRanges, mode: composerMode, workItems, workPlanInput });
  }

  function enterWorkMode(agentIds: string[], multiple = false) {
    if (commentSubmitting || sendComment.isPending) return;
    const ids = multiple ? [agentIds[0], agentIds[0]] : [...new Set(agentIds)];
    if (composerMode === "comment") {
      workItems = ids.map(newIssueWorkItem);
      composerMode = "work_items";
    } else if (multiple) {
      workItems = [...workItems, ...ids.slice(0, maximumIssueWorkItems - workItems.length).map(newIssueWorkItem)];
    }
    workPlanInput = null;
    mentionOpen = false;
    savePrompt();
  }

  function cancelWorkMode() {
    composerMode = "comment";
    workItems = [];
    workPlanInput = null;
    workPlanning = false;
    savePrompt();
  }

  function interceptMultipleAgents(agent?: Agent): boolean {
    if (composerMode !== "comment") return false;
    const ids = resolveMentionedAgentIds(prompt, mentionRanges, agents);
    if (agent && !ids.includes(agent.id)) ids.push(agent.id);
    if (ids.length < 2) return false;
    enterWorkMode(ids);
    return true;
  }

  let mentionOpen = $state(false);
  let mentionQuery = $state("");
  let mentionInsertAt = $state(0);
  let mentionActiveIndex = $state(0);
  let promptInput = $state<HTMLTextAreaElement>();
  let timelineScroll = $state<HTMLDivElement>();
  const mentionCandidates = $derived(
    agents.filter(
      (item) =>
        canMentionAgent(item) &&
        item.name.toLocaleLowerCase().includes(mentionQuery.toLocaleLowerCase())
    )
  );

  // 候选打开条件：光标前最后一个 @ 到光标之间没有换行。允许查询串含空格，
  // 这样带空格的 Agent 名（如「Codex 架构师」）也能继续补全。
  function handlePromptInput(event: Event) {
    const target = event.currentTarget as HTMLTextAreaElement;
    mentionRanges = reconcileMentionRanges(prompt, target.value, mentionRanges);
    prompt = target.value;
    savePrompt();
    if (interceptMultipleAgents()) return;
    updateMentionQuery(target);
  }

  function updateMentionQuery(target: HTMLTextAreaElement) {
    const query = mentionQueryAt(prompt, target.selectionStart ?? prompt.length);
    if (query) {
      mentionOpen = true;
      mentionInsertAt = query.start;
      mentionQuery = query.query;
      mentionActiveIndex = 0;
    } else {
      mentionOpen = false;
    }
  }

  function insertMention(agent: Agent) {
    if (interceptMultipleAgents(agent)) return;
    const inserted = insertAgentMention(prompt, mentionRanges, mentionInsertAt, mentionInsertAt + mentionQuery.length + 1, agent);
    prompt = inserted.text;
    mentionRanges = inserted.ranges;
    savePrompt();
    mentionOpen = false;
    const caret = inserted.caret;
    void tick().then(() => {
      promptInput?.focus();
      promptInput?.setSelectionRange(caret, caret);
    });
  }

  // 点击「参与的 Agent」时快速插入 @ 提及：输入框聚焦时插到光标处，否则追加。
  function mentionAgent(agent: Agent) {
    if (!canMentionAgent(agent)) return;
    if (interceptMultipleAgents(agent)) return;
    const input = promptInput;
    let start = prompt.length;
    let end = prompt.length;
    if (input) {
      start = input.selectionStart ?? prompt.length;
      end = input.selectionEnd ?? start;
    }
    const inserted = insertAgentMention(prompt, mentionRanges, start, end, agent);
    prompt = inserted.text;
    mentionRanges = inserted.ranges;
    savePrompt();
    const caret = inserted.caret;
    mentionOpen = false;
    void tick().then(() => {
      promptInput?.focus();
      promptInput?.setSelectionRange(caret, caret);
    });
  }

  // 提交评论：解析 @ 名单 → mentioned_agent_ids（结构化）。
  function submitComment(event: SubmitEvent) {
    event.preventDefault();
    if (commentSubmitting || sendComment.isPending || workPlanning) return;
    if (interceptMultipleAgents()) return;
    const body = prompt.trim();
    if (composerMode === "work_items" ? workValidation || workPlanStale : !body) return;
    savePrompt();
    const snapshot = readComposerDraft(draftKey);
    if (!beginComposerSubmission(draftKey)) return;
    mentionOpen = false;
    sendComment.mutate({
      issueId: issue.id, projectId, draftKey, snapshot, body,
      mentionedAgentIds: snapshot.mode === "work_items" ? [] : resolveMentionedAgentIds(snapshot.text, snapshot.mentions, agents),
      idempotencyKey: snapshot.mode === "work_items" ? composerIdempotencyKey(draftKey) : undefined
    });
  }

  const sendComment = createMutation<
    IssueComment,
    Error,
    { issueId: string; projectId: string; draftKey: string; snapshot: ComposerDraft; body: string; mentionedAgentIds: string[]; idempotencyKey?: string }
  >(() => ({
    mutationFn: (input) =>
      api<IssueComment>(`/issues/${input.issueId}/comments`, {
        body: { body: input.body, mentioned_agent_ids: input.mentionedAgentIds,
          ...(input.snapshot.mode === "work_items" ? { work_items: input.snapshot.workItems } : {}) },
        headers: input.idempotencyKey ? { "Idempotency-Key": input.idempotencyKey } : undefined
      }),
    onSuccess: (item, input) => {
      completeComposerSubmission(input.draftKey, input.snapshot);
      queryClient.setQueryData<Page<IssueComment>>(["issue-comments", input.issueId], (current) => ({
        ...current, items: [...(current?.items ?? []).filter((comment) => comment.id !== item.id), item]
      }));
      queryClient.invalidateQueries({ queryKey: ["issue-comments", input.issueId] });
      queryClient.invalidateQueries({ queryKey: ["issue-instances", input.issueId] });
      queryClient.invalidateQueries({ queryKey: ["issue-runs", input.issueId] });
      queryClient.invalidateQueries({ queryKey: ["issues", input.projectId] });
      if (issue.id !== input.issueId) return;
      void tick().then(() => {
        try {
          timelineScroll?.scrollTo({ top: timelineScroll.scrollHeight, behavior: "smooth" });
        } catch {
          // 测试环境（jsdom）不实现 scrollTo，忽略即可。
        }
      });
    },
    onSettled: (_item, _error, input) => finishComposerSubmission(input.draftKey)
  }));

  // --- 评论编辑与删除：用户评论可编辑可删除，Agent 评论只可删除 ---
  let editingCommentId = $state("");
  let editDraft = $state("");
  let editEpoch = 0;
  let editVersion = 0;

  function startCommentEdit(comment: IssueComment) {
    editEpoch++;
    editingCommentId = comment.id;
    editVersion = comment.version;
    editDraft = comment.body;
  }

  function cancelCommentEdit() {
    editEpoch++;
    editingCommentId = "";
    editDraft = "";
  }

  const updateComment = createMutation<
    IssueComment,
    Error,
    { id: string; issueId: string; version: number; body: string; draft: string; epoch: number }
  >(() => ({
    mutationFn: (input) =>
      api<IssueComment>(`/comments/${input.id}`, {
        method: "PATCH",
        body: { body: input.body, expected_version: input.version }
      }),
    onSuccess: (_item, input) => {
      if (issue.id === input.issueId && editingCommentId === input.id && editEpoch === input.epoch && editDraft === input.draft) cancelCommentEdit();
      queryClient.invalidateQueries({ queryKey: ["issue-comments", input.issueId] });
    }
  }));

  function submitCommentEdit(comment: IssueComment) {
    if (updateComment.isPending) return;
    const body = editDraft.trim();
    if (!body || body === comment.body) {
      cancelCommentEdit();
      return;
    }
    updateComment.mutate({ id: comment.id, issueId: issue.id, version: editVersion, body, draft: editDraft, epoch: editEpoch });
  }

  const deleteComment = createMutation<unknown, Error, { id: string; issueId: string; version: number; draft: string; epoch: number }>(() => ({
    mutationFn: (input) =>
      api(`/comments/${input.id}?expected_version=${input.version}`, { method: "DELETE" }),
    onSuccess: (_result, input) => {
      if (issue.id === input.issueId && editingCommentId === input.id && editEpoch === input.epoch && editDraft === input.draft) cancelCommentEdit();
      queryClient.invalidateQueries({ queryKey: ["issue-comments", input.issueId] });
    }
  }));

  function requestCommentDelete(comment: IssueComment) {
    if (deleteComment.isPending) return;
    if (!window.confirm(t("common.confirmDelete"))) return;
    deleteComment.mutate({ id: comment.id, issueId: issue.id, version: comment.version, draft: editDraft, epoch: editEpoch });
  }

  // --- Issue 标题就地编辑 ---
  let titleEditing = $state(false);
  let titleDraft = $state("");
  let titleTouched = $state(false);
  let titleInput = $state<HTMLInputElement>();
  let titleIssueId = $state("");

  $effect(() => {
    if (titleIssueId === issue.id) return;
    titleIssueId = issue.id;
    titleDraft = "";
    titleTouched = false;
    titleEditing = false;
    mentionOpen = false;
    cancelCommentEdit();
    sideSheetOpen = false;
    sideSheetInstance = null;
    issueChangesOpen = false;
  });

  function startTitleEdit() {
    if (updateTitle.isPending) return;
    titleDraft = issue.title;
    titleTouched = false;
    titleEditing = true;
    void tick().then(() => {
      titleInput?.focus();
      titleInput?.select();
    });
  }

  function cancelTitleEdit() {
    titleDraft = issue.title;
    titleTouched = false;
    titleEditing = false;
  }

  function submitTitleEdit() {
    if (!titleEditing) return;
    const title = titleDraft.trim();
    if (!title || updateTitle.isPending) return;
    if (!titleTouched) {
      titleEditing = false;
      return;
    }
    updateTitle.mutate({ issueId: issue.id, version: issue.version, title, draft: titleDraft });
  }

  const updateTitle = createMutation<Issue, Error, { issueId: string; version: number; title: string; draft: string }>(() => ({
    mutationFn: (input) =>
      api<Issue>(`/issues/${input.issueId}`, {
        method: "PATCH",
        body: { title: input.title, expected_version: input.version }
      }),
    onSuccess: (updated, input) => {
      if (issue.id === input.issueId && titleDraft === input.draft) {
        titleDraft = updated.title;
        titleEditing = false;
      }
      onIssueChange(updated);
    }
  }));

  // --- Issue 状态编辑 ---
  let statusMenuOpen = $state(false);
  let statusTrigger: HTMLButtonElement;
  let statusMenu = $state<HTMLDivElement>();

  function openStatusMenu(last = false) {
    statusMenuOpen = true;
    void tick().then(() => {
      const buttons = statusMenu?.querySelectorAll<HTMLButtonElement>("button");
      buttons?.[last ? buttons.length - 1 : 0]?.focus();
    });
  }

  function handleStatusMenuKey(event: KeyboardEvent) {
    if (event.key === "Escape" || event.key === "Tab") {
      statusMenuOpen = false;
      if (event.key === "Escape") {
        event.preventDefault();
      }
      statusTrigger?.focus();
      return;
    }
    const buttons = [...(statusMenu?.querySelectorAll<HTMLButtonElement>("button") ?? [])];
    const index = buttons.findIndex((button) => button === document.activeElement);
    const next = event.key === "ArrowDown" ? (index + 1) % buttons.length
      : event.key === "ArrowUp" ? (index - 1 + buttons.length) % buttons.length
        : event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : -1;
    if (next >= 0) {
      event.preventDefault();
      buttons[next]?.focus();
    }
  }

  function statusTone(status: string): string {
    switch (status) {
      case "open":
        return "border-success/35 bg-success-subtle text-success";
      case "resolved":
        return "border-accent/35 bg-accent-subtle text-accent";
      default:
        return "border-border-strong bg-surface-subtle text-secondary";
    }
  }

  function statusDescription(status: string): string {
    switch (status) {
      case "open":
        return t("issues.statusOpenDesc");
      case "resolved":
        return t("issues.statusResolvedDesc");
      default:
        return t("issues.statusClosedDesc");
    }
  }

  function selectStatus(status: string) {
    statusMenuOpen = false;
    statusTrigger?.focus();
    if (status !== issue.status) updateStatus.mutate({ issueId: issue.id, version: issue.version, status });
  }

  function handleStatusFocusOut(event: FocusEvent) {
    const target = event.currentTarget as HTMLElement;
    if (!target.contains(event.relatedTarget as Node | null)) statusMenuOpen = false;
  }

  function handleWindowKeydown(event: KeyboardEvent) {
    if (statusMenuOpen && event.key === "Escape") {
      statusMenuOpen = false;
      statusTrigger?.focus();
    }
  }

  const updateStatus = createMutation<Issue, Error, { issueId: string; version: number; status: string }>(() => ({
    mutationFn: (input) =>
      api<Issue>(`/issues/${input.issueId}`, {
        method: "PATCH",
        body: { status: input.status, expected_version: input.version }
      }),
    onSuccess: (updated, input) => {
      onIssueChange(updated);
      queryClient.invalidateQueries({ queryKey: ["issue-comments", input.issueId] });
    }
  }));

  // --- 急停 / 暂停 ---
  // --- SideSheet ---
  let sideSheetOpen = $state(false);
  let sideSheetInstance = $state<IssueAgentInstance | null>(null);
  let issueChangesOpen = $state(false);
  let issueChangesPanel = $state<HTMLElement>();
  let issueChangesCloseButton = $state<HTMLButtonElement>();
  let issueChangesWidth = $state(960);
  let issueLiveChangeSignal = $state<RunChangesInvalidationSignal>();
  const issueChangesTitleId = $derived(`issue-changes-title-${issue.id}`);
  const searchFocus = $derived.by(() => {
    const params = new URLSearchParams(location.search);
    return {
      description: params.get("focus") === "description",
      commentId: params.get("comment") ?? "",
      sessionId: params.get("session") ?? "",
      runId: params.get("run") ?? "",
      entryAnchor: params.get("entry") ?? ""
    };
  });
  let appliedSearchFocus = $state("");

  function focusTimelineTarget(selector: string) {
    void tick().then(() => {
      const target = document.querySelector<HTMLElement>(selector);
      if (!target) return;
      target.scrollIntoView({ behavior: "smooth", block: "center" });
      target.focus({ preventScroll: true });
      target.classList.add("search-target-flash");
      window.setTimeout(() => target.classList.remove("search-target-flash"), 2200);
    });
  }

  $effect(() => {
    const focus = searchFocus;
    const key = `${issue.id}:${focus.description}:${focus.commentId}:${focus.sessionId}:${focus.runId}:${focus.entryAnchor}`;
    if (key === appliedSearchFocus) return;
    if (focus.sessionId) {
      const instance = instanceItems.find((item) => item.id === focus.sessionId);
      if (!instance) return;
      openSideSheet(instance);
      appliedSearchFocus = key;
      return;
    }
    if (focus.commentId) {
      if (!commentItems.some((item) => item.id === focus.commentId)) return;
      appliedSearchFocus = key;
      focusTimelineTarget(`[data-comment-id="${escapeSelector(focus.commentId)}"]`);
      return;
    }
    if (focus.description) {
      appliedSearchFocus = key;
      focusTimelineTarget(`[data-issue-description="${escapeSelector(issue.id)}"]`);
    }
  });

  function escapeSelector(value: string): string {
    return typeof CSS !== "undefined" && typeof CSS.escape === "function"
      ? CSS.escape(value)
      : value.replaceAll('"', '\\"');
  }

  function openSideSheet(instance: IssueAgentInstance) {
    sideSheetInstance = instance;
    sideSheetOpen = true;
  }

  let commentSessionPending = $state("");
  let commentSessionError = $state<{ commentId: string; error: unknown }>();
  let commentSessionEpoch = 0;
  $effect(() => {
    // A lookup started on another task must not reopen this task's side sheet,
    // even when the user switches away and back before the request completes.
    const scope = `${projectId}:${issue.id}`;
    void scope;
    commentSessionPending = "";
    commentSessionError = undefined;
    return () => { commentSessionEpoch++; };
  });

  async function openCommentSession(comment: IssueComment, known?: IssueAgentInstance) {
    if (known) {
      openSideSheet(known);
      return;
    }
    if (!comment.run_id || commentSessionPending) return;
    const epoch = commentSessionEpoch;
    const issueId = issue.id;
    const targetProject = projectId;
    commentSessionPending = comment.id;
    commentSessionError = undefined;
    try {
      // The recent Run page is deliberately bounded. Resolve just this Run on
      // demand instead of loading every old prompt to recover its association.
      const run = await api<Run>(`/runs/${encodeURIComponent(comment.run_id)}`);
      if (epoch !== commentSessionEpoch || issue.id !== issueId || projectId !== targetProject) return;
      const instance = instanceItems.find((item) => item.id === run.issue_agent_instance_id &&
        item.issue_id === issueId && item.agent_id === comment.author_agent_id);
      if (run.id !== comment.run_id || run.issue_id !== issueId || run.project_id !== targetProject ||
        run.agent_id !== comment.author_agent_id || !instance) {
        throw new APIError(404, { error: { code: "not_found" } });
      }
      openSideSheet(instance);
    } catch (error) {
      if (epoch === commentSessionEpoch && issue.id === issueId && projectId === targetProject) {
        commentSessionError = { commentId: comment.id, error };
      }
    } finally {
      if (epoch === commentSessionEpoch) commentSessionPending = "";
    }
  }

  async function openWorkSession(commentId: string, item: IssueWorkItem) {
    if (!item.issue_agent_instance_id || commentSessionPending) return;
    const issueId = issue.id, epoch = commentSessionEpoch;
    commentSessionPending = commentId;
    commentSessionError = undefined;
    try {
      let instance = instanceItems.find((candidate) => candidate.id === item.issue_agent_instance_id);
      if (!instance) {
        const refreshed = await instances.refetch();
        instance = refreshed.data?.items.find((candidate) => candidate.id === item.issue_agent_instance_id);
      }
      if (epoch !== commentSessionEpoch || issue.id !== issueId) return;
      if (!instance || instance.issue_id !== issueId || instance.agent_id !== item.agent_id) throw new APIError(404, { error: { code: "not_found" } });
      openSideSheet(instance);
    } catch (error) {
      if (epoch === commentSessionEpoch && issue.id === issueId) commentSessionError = { commentId, error };
    } finally {
      if (epoch === commentSessionEpoch) commentSessionPending = "";
    }
  }

  function openIssueChanges() {
    sideSheetOpen = false;
    issueLiveChangeSignal = undefined;
    issueChangesOpen = true;
  }

  function closeIssueChanges() {
    issueChangesOpen = false;
  }

  onMount(() => {
    const handleDialogKey = (event: KeyboardEvent) => {
      if (!issueChangesOpen) return;
      if (event.key === "Escape") {
        event.preventDefault();
        closeIssueChanges();
        return;
      }
      if (event.key !== "Tab") return;
      const focusables = issueChangesPanel?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      );
      if (!focusables?.length) return;
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleDialogKey, true);
    return () => document.removeEventListener("keydown", handleDialogKey, true);
  });

  $effect(() => {
    if (!issueChangesOpen) return;
    issueChangesWidth =
      typeof window === "undefined" ? 960 : Math.min(1120, Math.max(420, window.innerWidth - 48));
    void tick().then(() => issueChangesCloseButton?.focus());
  });

  $effect(() => {
    if (!issueChangesOpen) return;
    const issueId = issue.id;
    return subscribeIssueEvents({
      issueId,
      onEvent: (event) => {
        const signal = runChangesInvalidationSignal(event);
        if (signal) issueLiveChangeSignal = signal;
        if (["run.completed", "run.finished", "run.error"].includes(event.event_type)) {
          void queryClient.invalidateQueries({ queryKey: ["issue-runs", issueId] });
          void queryClient.invalidateQueries({ queryKey: runGitChangesQueryKey(event.run_id) });
          void queryClient.invalidateQueries({
            queryKey: runChangesBundleQueryKey("issue", issueId)
          });
        }
      },
      onIssueChanged: () =>
        void queryClient.invalidateQueries({ queryKey: ["issue-runs", issueId] })
    });
  });

  const queryError = $derived(
    comments.error ??
      instances.error ??
      runs.error ??
      approvals.error
  );
  const composerError = $derived(
    (sendComment.variables?.issueId === issue.id ? sendComment.error : null) ??
      (updateComment.variables?.issueId === issue.id ? updateComment.error : null) ??
      (deleteComment.variables?.issueId === issue.id ? deleteComment.error : null) ??
      (decideApproval.variables?.issueId === issue.id ? decideApproval.error : null)
  );
  const issueMutationError = $derived(
    (updateTitle.variables?.issueId === issue.id ? updateTitle.error : null) ??
    (updateStatus.variables?.issueId === issue.id ? updateStatus.error : null)
  );

  function retryQueries() {
    void comments.refetch();
    void instances.refetch();
    void runs.refetch();
    void approvals.refetch();
  }

  const statusLabel = $derived((status: string) => {
    switch (status) {
      case "open":
        return t("issues.statusOpen");
      case "resolved":
        return t("issues.statusResolved");
      case "closed":
        return t("issues.statusClosed");
      default:
        return status;
    }
  });
</script>

<svelte:window onkeydown={handleWindowKeydown} />

<div class="grid h-full min-h-0 grid-rows-[minmax(0,1fr)_auto] bg-canvas">
  <div class="min-h-0 overflow-y-auto" bind:this={timelineScroll}>
    <div class="mx-auto w-full max-w-[920px] px-8 pb-10 pt-7 max-lg:px-6 max-md:px-4">
      <header class="border-b border-border pb-5">
        {#if titleEditing}
          <div class="min-w-0">
            <Input
              class="!min-h-0 min-w-0 !border-0 !bg-transparent !p-0 text-[28px] font-semibold leading-[1.25] tracking-[-0.025em] !shadow-none focus:!border-transparent"
              bind:ref={titleInput}
              bind:value={titleDraft}
              aria-label={t("issues.titleField")}
              disabled={updateTitle.isPending}
              oninput={() => { titleTouched = true; }}
              onblur={submitTitleEdit}
              onkeydown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  cancelTitleEdit();
                } else if (event.key === "Enter") {
                  event.preventDefault();
                  (event.currentTarget as HTMLInputElement).blur();
                }
              }}
            />
          </div>
        {:else}
          <button
            class="group/title flex w-full cursor-text items-start gap-2 border-0 bg-transparent p-0 text-left"
            type="button"
            aria-label={t("issues.editTitle")}
            title={t("issues.editTitle")}
            onclick={startTitleEdit}
          >
            <h1
              class="m-0 min-w-0 break-words text-[28px] font-semibold leading-[1.25] tracking-[-0.025em] text-text"
            >
              {issue.title || t("issues.untitled")}
            </h1>
            <Pencil
              size={14}
              class="mt-2 shrink-0 text-muted"
            />
          </button>
        {/if}
        <div class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2">
          <div class="relative" onfocusout={handleStatusFocusOut}>
            <button
              class="inline-flex min-h-8 cursor-pointer items-center gap-1.5 rounded-full border px-3 text-xs font-semibold transition-colors hover:brightness-95 disabled:cursor-wait disabled:opacity-65 {statusTone(
                issue.status
              )}"
              type="button"
              data-status-trigger
              bind:this={statusTrigger}
              aria-haspopup="menu"
              aria-expanded={statusMenuOpen}
              disabled={updateStatus.isPending}
              onclick={() => statusMenuOpen ? (statusMenuOpen = false) : openStatusMenu()}
              onkeydown={(event) => {
                if (event.key === "Escape") statusMenuOpen = false;
                if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                  event.preventDefault();
                  openStatusMenu(event.key === "ArrowUp");
                }
              }}
            >
              {#if issue.status === "open"}<CircleDot size={15} />
              {:else if issue.status === "resolved"}<CheckCircle2 size={15} />
              {:else}<XCircle size={15} />{/if}
              {statusLabel(issue.status)}
              <ChevronDown size={13} />
            </button>

            {#if statusMenuOpen}
              <div
                class="absolute left-0 top-[calc(100%+6px)] z-30 max-h-[50dvh] w-[270px] max-w-[calc(100vw-4rem)] overflow-y-auto rounded-md border border-border bg-surface p-1.5 shadow-raised"
                bind:this={statusMenu}
                role="menu"
                tabindex="-1"
                aria-label={t("issues.changeStatus")}
                onkeydown={handleStatusMenuKey}
              >
                {#each ["open", "resolved", "closed"] as status}
                  <button
                    class="flex w-full cursor-pointer items-start gap-2.5 rounded-sm border-0 bg-transparent px-2.5 py-2 text-left hover:bg-surface-hover"
                    type="button"
                    role="menuitemradio"
                    aria-checked={issue.status === status}
                    onclick={() => selectStatus(status)}
                  >
                    <span
                      class="mt-0.5 grid size-5 shrink-0 place-items-center {status === 'open'
                        ? 'text-success'
                        : status === 'resolved'
                          ? 'text-accent'
                          : 'text-muted'}"
                    >
                      {#if status === "open"}<CircleDot size={16} />
                      {:else if status === "resolved"}<CheckCircle2 size={16} />
                      {:else}<XCircle size={16} />{/if}
                    </span>
                    <span class="min-w-0 flex-1">
                      <strong class="block text-xs font-semibold text-text"
                        >{statusLabel(status)}</strong
                      >
                      <span class="mt-0.5 block text-2xs leading-relaxed text-muted"
                        >{statusDescription(status)}</span
                      >
                    </span>
                    {#if issue.status === status}<Check
                        class="mt-0.5 shrink-0 text-accent"
                        size={14}
                      />{/if}
                  </button>
                {/each}
              </div>
            {/if}
          </div>

          <p class="m-0 flex items-center gap-1.5 text-xs text-muted">
            <strong class="font-semibold text-secondary">{t("issues.you")}</strong>
            <span
              >{t("issues.issueMeta", {
                time: formatTime(issue.created_at),
                count: publishedCommentCount
              })}</span
            >
          </p>
          {#if issue.worktree_hint}
            <span
              class="rounded-full bg-warning-subtle px-2 py-0.5 text-2xs font-medium text-warning"
              >{t("issues.worktreeEnabled")}</span
            >
          {/if}
          <Button
            controlSize="sm" class="ml-auto max-md:ml-0"
            type="button"
            aria-haspopup="dialog"
            onclick={openIssueChanges}
          >
            <GitCompareArrows size={14} />{t("issues.issueChanges")}
          </Button>
        </div>
      </header>

      {#if queryError}<div class="mt-4"><ErrorNotice error={queryError} onRetry={retryQueries} /></div>{/if}
      {#if issueMutationError}
        <div class="mt-4"><ErrorNotice error={issueMutationError} /></div>
      {/if}
      <section
        class="py-4"
        aria-label={t("issues.timelineLabel")}
        data-run-count={runs.data?.items.length ?? 0}
      >
        {#if issue.description?.trim()}
          <article
            class="grid min-w-0 grid-cols-[36px_minmax(0,1fr)] items-start gap-3 border-y border-border border-l-2 border-l-border-strong bg-surface-subtle/60 px-4 py-3"
            data-issue-description={issue.id}
            tabindex="-1"
          >
            <span class="grid size-9 shrink-0 place-items-center rounded-full bg-ink text-canvas"
              ><User size={15} /></span
            >
            <div class="min-w-0">
              <header class="flex min-h-7 flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                <strong class="font-semibold text-text">{t("issues.you")}</strong>
                <span class="text-muted"
                  >{t("issues.openedThisIssue", { time: formatTime(issue.created_at) })}</span
                >
                <span
                  class="ml-auto rounded-full border border-border bg-surface px-2 py-0.5 text-2xs font-medium text-secondary"
                  >{t("issues.description")}</span
                >
              </header>
              <div class="mt-1.5 text-sm leading-[1.7] text-text">
                <RichText text={issue.description} />
              </div>
            </div>
          </article>
        {/if}

          {#if !publishedCommentCount}
            <div class="py-8 text-center text-sm text-muted" role="status" data-issue-comments-empty>
              <p>{t("issues.noComments")}</p>
              <p class="mt-1 text-xs">{t("issues.noCommentsDesc")}</p>
            </div>
          {/if}
          {#if commentItems.length}
            <div class="divide-y divide-border">
            {#each commentRows as row (row.comment.id)}
              {@const comment = row.comment}
              {@const commentInstance = row.instance}
              {@const commentAuthor =
                comment.author_kind === "user"
                  ? t("issues.you")
                  : commentInstance
                    ? sessionLabel(commentInstance)
                     : agentName(comment.author_agent_id ?? "")}
              {#if comment.author_kind === "agent" && (comment.status !== "published" || !hasCommentBody(comment))}
                <div class="py-3 text-xs text-muted" data-comment-id={comment.id} data-comment-status={comment.status} tabindex="-1">
                  <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span>{commentAuthor}</span>
                    {#if comment.status === "working"}
                      <LoaderCircle size={14} class="animate-spin" />
                      <span>{t("issues.agentWorking")}</span>
                    {:else}
                      <CircleAlert size={14} />
                      <span>{t("issues.summaryMissing")}</span>
                    {/if}
                    {#if commentInstance || comment.run_id}
                      <Button
                        controlSize="sm"
                        type="button"
                        title={commentInstance ? sessionLabel(commentInstance) : t("issues.openAgentSession")}
                        disabled={Boolean(commentSessionPending)}
                        aria-busy={commentSessionPending === comment.id}
                        onclick={() => void openCommentSession(comment, commentInstance)}
                      >{t("issues.openAgentSession")}</Button>
                    {/if}
                  </div>
                  {#if hasCommentBody(comment)}
                    <div class="mt-2 text-sm leading-[1.7]"><RichText text={comment.body} /></div>
                  {/if}
                  {#if commentSessionError?.commentId === comment.id}
                    <ErrorNotice error={commentSessionError.error} onRetry={() => void openCommentSession(comment, commentInstance)} />
                  {/if}
                </div>
              {:else}
              <article
                class="grid min-w-0 grid-cols-[36px_minmax(0,1fr)] items-start gap-3 bg-surface px-4 py-3"
                data-comment-id={comment.id}
                tabindex="-1"
              >
                <span
                  class="grid size-9 shrink-0 place-items-center rounded-full {comment.author_kind ===
                  'user'
                    ? 'bg-ink text-canvas'
                    : 'bg-surface-subtle text-secondary'}"
                >
                  {#if comment.author_kind === "user"}<User size={15} />{:else}<Bot
                      size={15}
                    />{/if}
                </span>
                <div class="min-w-0">
                  <header class="flex min-h-7 flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                    <strong class="font-semibold text-text">{commentAuthor}</strong>
                    <span class="text-muted"
                      >{t("issues.commentedAt", { time: formatTime(comment.created_at) })}</span
                    >
                    {#if comment.edited_at}<span class="text-2xs text-muted"
                        >· {t("issues.edited")}</span
                      >{/if}
                    <div class="ml-auto flex items-center gap-1">
                      {#if comment.author_kind === "agent" && (commentInstance || comment.run_id)}
                        <Button
                          controlSize="sm"
                          type="button"
                          title={commentInstance ? sessionLabel(commentInstance) : t("issues.openAgentSession")}
                          disabled={Boolean(commentSessionPending)}
                          aria-busy={commentSessionPending === comment.id}
                          onclick={() => void openCommentSession(comment, commentInstance)}
                          ><Bot size={12} />{t("issues.openAgentSession")}</Button
                        >
                      {/if}
                      {#if comment.author_kind === "user" && editingCommentId !== comment.id}
                        <button
                          class="inline-flex size-7 cursor-pointer items-center justify-center rounded-sm border border-border bg-surface text-muted hover:bg-surface-hover hover:text-text"
                          type="button"
                          title={t("issues.editComment")}
                          aria-label={t("issues.editComment")}
                          onclick={() => startCommentEdit(comment)}
                          ><Pencil size={12} /></button
                        >
                      {/if}
                      <button
                        class="inline-flex size-7 cursor-pointer items-center justify-center rounded-sm border border-border bg-surface text-muted hover:bg-surface-hover hover:text-danger"
                        type="button"
                        title={t("issues.deleteComment")}
                        aria-label={t("issues.deleteComment")}
                        disabled={deleteComment.isPending}
                        onclick={() => requestCommentDelete(comment)}
                        ><Trash2 size={12} /></button
                      >
                    </div>
                  </header>
                  {#if commentSessionError?.commentId === comment.id}
                    <ErrorNotice error={commentSessionError.error} onRetry={() => void openCommentSession(comment, commentInstance)} />
                  {/if}
                  {#if editingCommentId === comment.id}
                    <div class="mt-2 grid gap-2">
                      <Textarea
                        class="min-h-[72px] resize-y"
                        aria-label={t("issues.editComment")}
                        placeholder={t("issues.editCommentPlaceholder")}
                        bind:value={editDraft}
                        oninput={() => editEpoch++}
                      ></Textarea>
                      <div class="flex items-center gap-2">
                        <Button
                          variant="primary" controlSize="sm"
                          type="button"
                          disabled={updateComment.isPending || !editDraft.trim()}
                          onclick={() => submitCommentEdit(comment)}
                          ><Check size={13} />{t("common.save")}</Button
                        >
                        <Button
                          controlSize="sm"
                          type="button"
                          onclick={cancelCommentEdit}
                          ><X size={13} />{t("common.cancel")}</Button
                        >
                      </div>
                    </div>
                  {:else}
                    <div class="mt-1.5 text-sm leading-[1.7] text-text">
                      <RichText text={comment.body} />
                    </div>
                  {/if}
                  {#if comment.work_items?.length}
                    <IssueWorkItems items={comment.work_items} {agentName} pending={Boolean(commentSessionPending)} onOpenSession={(item) => void openWorkSession(comment.id, item)} />
                  {/if}
                </div>
              </article>
              {/if}
            {/each}
            </div>
          {/if}
      </section>
    </div>
  </div>

  <form
    class="relative border-t border-border bg-surface px-6 py-4 max-md:px-4"
    onsubmit={submitComment}
  >
    <div class="mx-auto grid w-full max-w-[920px] gap-3">
      {#if composerError}<div data-testid="issue-composer-error"><ErrorNotice error={composerError} /></div>{/if}
      {#if pendingApprovals.length}
        <section class="grid gap-2" aria-label={t("issues.pendingActions")}>
          {#each pendingApprovals as item (item.id)}
            <div
              class="flex items-center gap-3 rounded-md border border-warning/35 bg-warning-subtle px-4 py-3 shadow-[0_4px_18px_var(--color-shadow)]"
            >
              <span
                class="grid size-8 shrink-0 place-items-center rounded-full bg-warning/15 text-warning"
                ><CircleAlert size={16} /></span
              >
              <span class="min-w-0 flex-1">
                <strong class="block text-xs"
                  >{t("issues.agentRequestsAction", { agent: approvalAgentName(item) })}</strong
                >
                <code
                  class="mt-1 block truncate text-2xs text-secondary"
                  title={approvalSummary(item)}>{approvalSummary(item)}</code
                >
              </span>
              <Button controlSize="sm"
                type="button"
                disabled={decideApproval.isPending}
                onclick={() => decideApproval.mutate({ item, decision: "denied", issueId: issue.id })}
              >
                <X size={13} />{t("issues.denyAction")}
              </Button>
              <Button variant="primary" controlSize="sm"
                type="button"
                disabled={decideApproval.isPending}
                onclick={() => decideApproval.mutate({ item, decision: "approved", issueId: issue.id })}
              >
                <Check size={13} />{t("issues.allowAction")}
              </Button>
            </div>
          {/each}
        </section>
      {/if}

      {#if participantAgents.length}
        <div class="flex flex-wrap items-center gap-2">
          <span class="mr-1 text-2xs font-semibold text-muted">{t("issues.participants")}</span>
          {#each participantAgents as agent (agent.id)}
            <button
              class="inline-flex cursor-pointer items-center gap-1.5 rounded-full border border-border bg-surface px-2.5 py-1 text-xs hover:bg-surface-hover"
              type="button"
              disabled={!canMentionAgent(agent)}
              title={t("issues.mentionAgentHint", { agent: agent.name })}
              onclick={() => mentionAgent(agent)}
            >
              <Bot size={13} class="text-muted" />
              <span class="max-w-40 truncate">{agent.name}</span>
            </button>
          {/each}
        </div>
      {/if}

      {#if mentionOpen && mentionCandidates.length}
        <div
          class="absolute bottom-[calc(100%+4px)] z-20 max-h-52 w-[560px] max-w-[calc(100vw-2rem)] overflow-y-auto rounded-md border border-border bg-surface p-1 shadow-raised"
        >
          {#each mentionCandidates as agent, index (agent.id)}
            <div class="flex items-center gap-1">
            <button
              class="flex min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-sm px-2.5 py-2 text-left text-xs {index ===
              mentionActiveIndex
                ? 'bg-surface-hover text-text'
                : 'text-secondary'} hover:bg-surface-hover"
              type="button"
              onmouseenter={() => (mentionActiveIndex = index)}
              onclick={() => insertMention(agent)}
              data-agent-id={agent.id}
            >
              <Bot size={13} class="text-muted" /><span class="min-w-0 flex-1 truncate">{agent.name}</span>
              {#if agents.filter((item) => item.name === agent.name).length > 1}
                <small class="max-w-28 truncate text-muted" title={agent.id}>{agent.id}</small>
              {/if}
            </button>
            <Button controlSize="sm" disabled={commentSubmitting || sendComment.isPending || (composerMode === "work_items" && workItems.length > maximumIssueWorkItems - 2)} aria-label={t("issueWork.multiple", { agent: agent.name })} onclick={() => enterWorkMode([agent.id], true)}>{t("issueWork.multipleShort")}</Button>
            </div>
          {/each}
        </div>
      {/if}

      <Textarea
        class="min-h-[88px] resize-y"
        bind:ref={promptInput}
        value={prompt}
        aria-label={t("issues.commentInput")}
        placeholder={t("issues.commentPlaceholder")}
        rows={3}
        oninput={handlePromptInput}
        onclick={(event) => updateMentionQuery(event.currentTarget)}
        onkeyup={(event) => {
          if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) updateMentionQuery(event.currentTarget);
        }}
        onkeydown={(event: KeyboardEvent) => {
          if (event.isComposing) return;
          if (mentionOpen && mentionCandidates.length) {
            if (event.key === "ArrowDown") {
              event.preventDefault();
              mentionActiveIndex = (mentionActiveIndex + 1) % mentionCandidates.length;
              return;
            }
            if (event.key === "ArrowUp") {
              event.preventDefault();
              mentionActiveIndex =
                (mentionActiveIndex - 1 + mentionCandidates.length) % mentionCandidates.length;
              return;
            }
            if (event.key === "Enter" && !event.metaKey && !event.ctrlKey && !event.shiftKey) {
              event.preventDefault();
              insertMention(mentionCandidates[mentionActiveIndex]);
              return;
            }
            if (event.key === "Escape") {
              event.preventDefault();
              mentionOpen = false;
              return;
            }
          }
          if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
            event.preventDefault();
            submitComment(new SubmitEvent("submit"));
          }
        }}></Textarea>
      {#if composerMode === "work_items"}
        <IssueWorkComposer issueId={issue.id} input={prompt} {agents} {services} items={workItems} plannedInput={workPlanInput}
          validation={workValidation} submitting={commentSubmitting || sendComment.isPending}
          onChange={(items, plannedInput) => { workItems = items; workPlanInput = plannedInput; savePrompt(); }}
          onCancel={cancelWorkMode} onPlanningChange={(pending) => workPlanning = pending} />
      {/if}
      <div class="flex flex-wrap items-center gap-3 text-2xs text-muted">
        <span>{composerMode === "work_items" ? t("issueWork.executionCounts", { immediate: immediateWorkCount, waiting: workItems.length - immediateWorkCount }) : t("issues.mentionHint")}</span>
        <Button type="submit" variant="primary" class="ml-auto"
          disabled={(composerMode === "work_items" ? Boolean(workValidation) || workPlanStale || workPlanning : !prompt.trim()) || commentSubmitting || sendComment.isPending}
        >
          <Send size={14} />{composerMode === "work_items" ? t("issueWork.send", { count: workItems.length }) : t("issues.send")}
        </Button>
      </div>
    </div>
  </form>
</div>

{#if sideSheetOpen && sideSheetInstance}
  <AgentSideSheet
    issueId={issue.id}
    {projectId}
    expectedIssueVersion={issue.version}
    {repositories}
    instance={sideSheetInstance}
    agentName={agentName(sideSheetInstance.agent_id)}
    sessionLabel={sessionLabel(sideSheetInstance)}
    focusRunId={searchFocus.runId}
    focusEntryAnchor={searchFocus.entryAnchor}
    onClose={() => (sideSheetOpen = false)}
    onInstanceChange={() =>
      queryClient.invalidateQueries({ queryKey: ["issue-instances", issue.id] })}
  />
{/if}

{#if issueChangesOpen}
  <div
    class="fixed inset-0 z-50 flex justify-end overflow-hidden bg-black/20"
    role="dialog"
    aria-modal="true"
    aria-labelledby={issueChangesTitleId}
    data-testid="issue-changes-dialog"
  >
    <button
      class="absolute inset-0 cursor-default border-0 bg-transparent p-0"
      type="button"
      aria-label={t("common.close")}
      onclick={closeIssueChanges}
    ></button>
    <aside
      bind:this={issueChangesPanel}
      class="relative flex h-full min-w-0 max-w-full flex-col overflow-hidden border-l border-border bg-surface"
      style:width={issueChangesWidth + "px"}
      data-issue-id={issue.id}
    >
      <header class="flex min-h-14 shrink-0 items-center gap-3 border-b border-border px-4 py-2.5">
        <span
          class="grid size-8 shrink-0 place-items-center rounded-full bg-accent-subtle text-accent"
          ><GitCompareArrows size={16} /></span
        >
        <div class="min-w-0 flex-1">
          <h2 id={issueChangesTitleId} class="m-0 truncate text-sm font-semibold">
            {t("issues.issueChanges")}
          </h2>
          <p class="m-0 mt-0.5 truncate text-2xs text-muted">{t("issues.issueChangesDesc")}</p>
        </div>
        <button
          bind:this={issueChangesCloseButton}
          class="inline-grid size-8 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-secondary hover:bg-surface-hover hover:text-text"
          type="button"
          aria-label={t("common.close")}
          onclick={closeIssueChanges}><X size={16} /></button
        >
      </header>
      <div class="flex min-h-0 min-w-0 flex-1">
        <AgentRunChanges
          scopeId={issue.id}
          scopeKind="issue"
          runs={runs.data?.items ?? []}
          {repositories}
          width={issueChangesWidth}
          defaultScope="all"
          liveChangeSignal={issueLiveChangeSignal}
        />
      </div>
    </aside>
  </div>
{/if}
