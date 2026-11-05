<script lang="ts">
  import { Button, Checkbox, FileInput, Input, Textarea } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import {
    Activity, Ban, Bot, Check, CheckCircle2, CircleAlert, CircleHelp, Clock3,
    Eye, GitCompareArrows, LoaderCircle, Paperclip, Pause, Play, Send, Square,
    Target, Trash2, User, X, XCircle
  } from "lucide-svelte";
  import { onDestroy, onMount, tick } from "svelte";
  import AgentActivityRow from "../components/AgentActivityRow.svelte";
  import AgentRunChanges from "../components/AgentRunChanges.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Loading from "../components/Loading.svelte";
  import RichText from "../components/RichText.svelte";
  import { api, idempotencyKey } from "../lib/api";
  import {
    imageAttachmentBlocks, maximumImageAttachmentCount, validateImageAttachmentFile
  } from "../lib/attachments";
  import { createFrameScheduler } from "../lib/frameScheduler";
  import {
    formatTime, isTerminal, statusLabel, visibleTextContent
  } from "../lib/helpers";
  import { t } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import {
    beginComposerSubmission, canRetainDraftAttachments, completeComposerSubmission,
    composerDraftKey, finishComposerSubmission, readComposerDraft, saveComposerDraft,
    subscribeComposerDraft, type ComposerDraft, type PendingComposerAttachment
  } from "../lib/composerDrafts";
  import { subscribeRunEvents } from "../lib/runEventStream";
  import { materializeRunActivity, projectRunActivity } from "../lib/runActivityProjection";
  import { goalBudgetExhausted } from "../lib/goalBudget";
  import { compactionPresentation, settledActivityStatus } from "../lib/compaction";
  import { diagnosticText, isFailureEvent, isRunTerminalEvent } from "../lib/runDiagnostics";
  import {
    BoundedRunEventBuffer, projectActivityRunEvent, RunChangesInvalidationHandler,
    type RunChangesInvalidationSignal
  } from "../lib/activityEvents";
  import {
    fetchInstanceRunsPage, instanceRunsQueryKey, mergeInstanceRunPages,
    type InstanceRunsPage
  } from "../lib/instanceRuns";
  import { createConcurrencyLimiter, type GitRepositorySummary } from "../lib/gitChanges";
  import { runGitChangesQueryKey, safeRunPath } from "../lib/runGitChanges";
  import { issueRunRequestBody } from "../lib/gitRepositorySelection";
  import type {
    Approval, CurrentIssueAgentGoal, IssueAgentGoal, IssueAgentInstance, Page, Run, RunEvent
  } from "../lib/types";

  interface SessionEntry {
    id: string;
    runId: string;
    kind: "user" | "assistant" | "reasoning" | "tool" | "change" | "approval" | "retry" | "error" | "compaction";
    label: string;
    text: string;
    detail?: string;
    occurredAt: string;
    status?: string;
    durationMs?: number;
    approval?: Approval;
    attachments?: SessionAttachment[];
  }

  type PendingAttachment = PendingComposerAttachment;

  interface SessionAttachment {
    mimeType: string;
    filename: string;
  }

  interface EventHistory {
    items: RunEvent[];
    buffer: BoundedRunEventBuffer;
    nextByRun: Record<string, string | undefined>;
    statusByRun?: Record<string, string>;
    evictedCount: number;
    evictedBytes: number;
    incomplete: boolean;
  }

  function instanceEventsQueryKey(instanceId: string) {
    return ["instance-events", instanceId] as const;
  }

  function eventHistory(
    buffer: BoundedRunEventBuffer,
    nextByRun: Record<string, string | undefined>,
    statusByRun: Record<string, string> = {}
  ): EventHistory {
    return {
      items: buffer.values(), buffer, nextByRun, statusByRun,
      evictedCount: buffer.evictedCount,
      evictedBytes: buffer.evictedBytes,
      incomplete: buffer.incomplete
    };
  }

  let {
    issueId, projectId, expectedIssueVersion, instance, agentName, sessionLabel,
    repositories, focusRunId = "", focusEntryAnchor = "", onClose, onInstanceChange
  }: {
    issueId: string;
    projectId: string;
    expectedIssueVersion: number;
    instance: IssueAgentInstance;
    agentName: string;
    sessionLabel: string;
    repositories: GitRepositorySummary[];
    focusRunId?: string;
    focusEntryAnchor?: string;
    onClose: () => void;
    onInstanceChange: () => void;
  } = $props();

  let prompt = $state("");
  let attachments = $state<PendingAttachment[]>([]);
  let draftSubmitting = $state(false);
  let destroyed = false;
  const draftKey = $derived(composerDraftKey(projectId, issueId, instance.id));
  $effect(() => subscribeComposerDraft(draftKey, (draft) => {
    prompt = draft.text;
    attachments = draft.attachments;
    goalArmed = draft.goalArmed;
    draftSubmitting = draft.submitting;
  }));

  function saveDraft() {
    saveComposerDraft(draftKey, { text: prompt, attachments, goalArmed });
  }
  let attachmentError = $state("");
  let attachmentPreview = $state<PendingAttachment>();
  let attachmentInput = $state<HTMLInputElement>();
  let attachmentDropDepth = $state(0);
  const goalBudgetStorageKey = "teamva.goal-token-budget";
  const goalBudgetEnabledStorageKey = "teamva.goal-token-budget-enabled";
  let goalArmed = $state(false);
  let goalBudgetEnabled = $state(loadGoalBudgetEnabled());
  let goalTokenBudget = $state(loadGoalTokenBudget());
  let activeTab = $state<"activity" | "changes">("activity");
  let changesFocusRunId = $state("");
  let liveChangeSignal = $state<RunChangesInvalidationSignal>();
  let observedActiveRunId = $state("");
  let loadingMoreEvents = $state(false);
  let eventPageController: AbortController | undefined;
  let loadingOlderRuns = $state(false);
  let runPageController: AbortController | undefined;
  let runHistoryError = $state<unknown>();
  let eventHistoryError = $state<unknown>();
  let eventRequestInstanceId = $state("");
  let observedEventRunIDs = $state("");
  let liveStreamRevision = $state(0);
  let changeInvalidationHandler = createChangeInvalidationHandler("");
  const actionControllers = new Set<AbortController>();
  let timeline = $state<HTMLDivElement>();
  let stickToBottom = $state(true);
  const timelineRenderStep = 250;
  const maximumRenderedTimelineEntries = 1_000;
  let timelineRenderLimit = $state(timelineRenderStep);
  let appliedFocusKey = $state("");
  const defaultSheetWidth = 620;
  const minimumSheetWidth = 420;
  const maximumSheetWidth = 1280;
  const sheetWidthStorageKey = "teamva.agent-session-width";
  let sheetWidth = $state(loadSheetWidth());
  let resizing = $state(false);
  let resizePointerId = -1;
  let resizeStartX = 0;
  let resizeStartWidth = defaultSheetWidth;
  let resizeCurrentWidth = defaultSheetWidth;
  let sheetPanel: HTMLElement;
  let closeButton: HTMLButtonElement;
  const sheetTitleId = $derived(`agent-session-title-${instance.id}`);
  const resizeFrame = createFrameScheduler<number>((width) => {
    // A direct style update avoids invalidating Svelte state for every pointer
    // event. The reactive value is committed once, when the gesture finishes.
    sheetPanel?.style.setProperty("width", width + "px");
  });

  function escapeSelector(value: string): string {
    return typeof CSS !== "undefined" && typeof CSS.escape === "function"
      ? CSS.escape(value)
      : value.replaceAll('"', '\\"');
  }

  function createChangeInvalidationHandler(instanceId: string): RunChangesInvalidationHandler {
    let handler: RunChangesInvalidationHandler;
    handler = new RunChangesInvalidationHandler((signal) => {
      if (changeInvalidationHandler === handler && eventRequestInstanceId === instanceId) {
        liveChangeSignal = signal;
      }
    });
    return handler;
  }

  $effect(() => {
    if (eventRequestInstanceId === instance.id) return;
    eventPageController?.abort();
    eventPageController = undefined;
    runPageController?.abort();
    runPageController = undefined;
    for (const controller of actionControllers) controller.abort();
    actionControllers.clear();
    eventRequestInstanceId = instance.id;
    changeInvalidationHandler = createChangeInvalidationHandler(instance.id);
    loadingMoreEvents = false;
    loadingOlderRuns = false;
    runHistoryError = undefined;
    eventHistoryError = undefined;
    observedEventRunIDs = "";
    activeTab = "activity";
    changesFocusRunId = "";
    liveChangeSignal = undefined;
    attachmentError = "";
    attachmentPreview = undefined;
    attachmentDropDepth = 0;
    timelineRenderLimit = timelineRenderStep;
    appliedFocusKey = "";
  });

  onDestroy(() => {
    destroyed = true;
    resizeFrame.cancel();
    eventPageController?.abort();
    runPageController?.abort();
    for (const controller of actionControllers) controller.abort();
  });

  async function runCancellableAction<T>(request: (signal: AbortSignal) => Promise<T>): Promise<T> {
    const controller = new AbortController();
    actionControllers.add(controller);
    try {
      return await request(controller.signal);
    } finally {
      actionControllers.delete(controller);
    }
  }

  function sessionAttachments(value: unknown): SessionAttachment[] {
    if (!Array.isArray(value)) return [];
    return value.flatMap((candidate) => {
      const record = asRecord(candidate);
      const mimeType = stringValue(record?.mime_type);
      const filename = stringValue(record?.filename);
      return mimeType && filename ? [{ mimeType, filename }] : [];
    });
  }

  function runAttachments(run: Run): SessionAttachment[] {
    return (run.input ?? []).flatMap((block) => block.type === "image" && block.mime_type && block.filename
      ? [{ mimeType: block.mime_type, filename: block.filename }]
      : []);
  }

  function readFileAsDataURL(file: File): Promise<string> {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => typeof reader.result === "string"
        ? resolve(reader.result)
        : reject(new Error(t("issues.attachmentReadFailed")));
      reader.onerror = () => reject(new Error(t("issues.attachmentReadFailed")));
      reader.readAsDataURL(file);
    });
  }

  function attachmentID(): string {
    if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
    return `attachment-${Date.now()}-${Math.random().toString(36).slice(2)}`;
  }

  async function addAttachmentFiles(files: File[]) {
    attachmentError = "";
    const targetKey = draftKey;
    if (goalArmed || liveGoal) {
      attachmentError = t("issues.attachmentsUnavailableForGoal");
      return;
    }
    const accepted: File[] = [];
    let totalBytes = attachments.reduce((sum, item) => sum + item.size, 0);
    for (const file of files) {
      const validation = validateImageAttachmentFile(
        file, attachments.length + accepted.length, totalBytes
      );
      if (validation) {
        attachmentError = validation === "too_many"
          ? t("issues.attachmentsTooMany", { count: maximumImageAttachmentCount })
          : validation === "unsupported"
            ? t("issues.attachmentUnsupported")
            : validation === "too_large"
              ? t("issues.attachmentTooLarge")
              : t("issues.attachmentsTooLarge");
        if (validation === "too_many" || validation === "total_too_large") break;
        continue;
      }
      accepted.push(file);
      totalBytes += file.size;
    }
    for (const file of accepted) {
      try {
        const dataURL = await readFileAsDataURL(file);
        const current = readComposerDraft(targetKey);
        const validation = validateImageAttachmentFile(file, current.attachments.length, current.attachments.reduce((sum, item) => sum + item.size, 0));
        if (validation || current.goalArmed) {
          if (!destroyed && draftKey === targetKey) attachmentError = t("issues.attachmentsTooLarge");
          continue;
        }
        const next = [...current.attachments, {
          id: attachmentID(), filename: file.name, mimeType: file.type,
          dataURL, size: file.size
        }];
        if (!canRetainDraftAttachments(targetKey, next)) {
          if (!destroyed && draftKey === targetKey) attachmentError = t("issues.draftAttachmentsLimit");
          continue;
        }
        saveComposerDraft(targetKey, { ...current, attachments: next });
      } catch {
        if (!destroyed && draftKey === targetKey) attachmentError = t("issues.attachmentReadFailed");
      }
    }
  }

  function removeAttachment(id: string) {
    attachments = attachments.filter((item) => item.id !== id);
    saveDraft();
    if (attachmentPreview?.id === id) attachmentPreview = undefined;
    attachmentError = "";
  }

  function attachmentDragEnter(event: DragEvent) {
    if (!Array.from(event.dataTransfer?.types ?? []).includes("Files")) return;
    event.preventDefault();
    event.stopPropagation();
    attachmentDropDepth++;
  }

  function attachmentDragOver(event: DragEvent) {
    const dataTransfer = event.dataTransfer;
    if (!dataTransfer || !Array.from(dataTransfer.types).includes("Files")) return;
    event.preventDefault();
    event.stopPropagation();
    dataTransfer.dropEffect = "copy";
  }

  function attachmentDragLeave(event: DragEvent) {
    if (!Array.from(event.dataTransfer?.types ?? []).includes("Files")) return;
    event.preventDefault();
    event.stopPropagation();
    attachmentDropDepth = Math.max(0, attachmentDropDepth - 1);
  }

  function attachmentDrop(event: DragEvent) {
    event.preventDefault();
    event.stopPropagation();
    attachmentDropDepth = 0;
    const files = Array.from(event.dataTransfer?.files ?? []);
    if (files.length) void addAttachmentFiles(files);
  }

  // 粘贴截图/文件时按附件处理；纯文本粘贴保留浏览器默认行为（不拦截）。
  function attachmentPaste(event: ClipboardEvent) {
    const files = Array.from(event.clipboardData?.files ?? []);
    if (!files.length) return;
    // 剪贴板同时含文件时阻止默认插入（避免把文件名等文本塞进输入框）。
    event.preventDefault();
    void addAttachmentFiles(files);
  }

  onMount(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    const handleDialogKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        if (attachmentPreview) {
          attachmentPreview = undefined;
          event.preventDefault();
          event.stopPropagation();
          return;
        }
        event.preventDefault();
        event.stopPropagation();
        onClose();
        return;
      }
      if (event.key !== "Tab") return;
      const focusables = sheetPanel?.querySelectorAll<HTMLElement>(
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
    void tick().then(() => closeButton?.focus());
    return () => {
      document.removeEventListener("keydown", handleDialogKey, true);
      if (previouslyFocused?.isConnected) previouslyFocused.focus();
    };
  });

  function loadGoalTokenBudget(): number {
    if (typeof window === "undefined") return 2_000_000;
    const saved = Number(window.localStorage.getItem(goalBudgetStorageKey));
    return Number.isSafeInteger(saved) && saved >= 1_000 && saved <= 100_000_000
      ? saved
      : 2_000_000;
  }

  function loadGoalBudgetEnabled(): boolean {
    if (typeof window === "undefined") return true;
    return window.localStorage.getItem(goalBudgetEnabledStorageKey) !== "false";
  }

  $effect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem(goalBudgetStorageKey, String(goalTokenBudget));
    window.localStorage.setItem(goalBudgetEnabledStorageKey, String(goalBudgetEnabled));
  });

  function availableSheetWidth(): number {
    if (typeof window === "undefined") return maximumSheetWidth;
    return Math.max(minimumSheetWidth, Math.min(maximumSheetWidth, window.innerWidth - 48));
  }

  function clampSheetWidth(value: number): number {
    return Math.round(Math.min(availableSheetWidth(), Math.max(minimumSheetWidth, value)));
  }

  function loadSheetWidth(): number {
    if (typeof window === "undefined") return defaultSheetWidth;
    const saved = Number(window.localStorage.getItem(sheetWidthStorageKey));
    return clampSheetWidth(Number.isFinite(saved) && saved > 0 ? saved : defaultSheetWidth);
  }

  function saveSheetWidth(value = sheetWidth) {
    if (typeof window !== "undefined") {
      window.localStorage.setItem(sheetWidthStorageKey, String(value));
    }
  }

  function setSheetWidth(value: number) {
    sheetWidth = clampSheetWidth(value);
    saveSheetWidth();
  }

  function startResize(event: PointerEvent) {
    if (event.button !== 0) return;
    event.preventDefault();
    resizing = true;
    resizePointerId = event.pointerId;
    resizeStartX = event.clientX;
    resizeStartWidth = sheetPanel?.getBoundingClientRect().width ?? sheetWidth;
    resizeCurrentWidth = resizeStartWidth;
    (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  }

  function resizeSheet(event: PointerEvent) {
    if (!resizing || event.pointerId !== resizePointerId) return;
    const nextWidth = clampSheetWidth(resizeStartWidth + resizeStartX - event.clientX);
    if (nextWidth === resizeCurrentWidth) return;
    resizeCurrentWidth = nextWidth;
    resizeFrame.schedule(nextWidth);
  }

  function finishResize(event: PointerEvent) {
    if (!resizing || event.pointerId !== resizePointerId) return;
    const target = event.currentTarget as HTMLElement;
    if (target.hasPointerCapture(event.pointerId)) target.releasePointerCapture(event.pointerId);
    resizeFrame.flush();
    sheetWidth = resizeCurrentWidth;
    resizing = false;
    resizePointerId = -1;
    saveSheetWidth(resizeCurrentWidth);
  }

  function resizeWithKeyboard(event: KeyboardEvent) {
    const step = event.shiftKey ? 40 : 16;
    if (event.key === "ArrowLeft") setSheetWidth(sheetWidth + step);
    else if (event.key === "ArrowRight") setSheetWidth(sheetWidth - step);
    else if (event.key === "Home") setSheetWidth(defaultSheetWidth);
    else return;
    event.preventDefault();
  }

  const runs = createQuery<InstanceRunsPage>(() => ({
    queryKey: instanceRunsQueryKey(instance.id),
    queryFn: ({ signal }) => fetchInstanceRunsPage(instance.id, "", signal),
    staleTime: 30_000,
    refetchOnWindowFocus: false
  }));
  const recentRuns = createQuery<InstanceRunsPage>(() => ({
    queryKey: ["instance-runs-recent", instance.id],
    // The polling cache must never retain the wire Page's unbounded input.
    queryFn: ({ signal }) => fetchInstanceRunsPage(instance.id, "", signal),
    enabled: Boolean(runs.data),
    // Query already suspends interval fetching in the background. Reading the
    // non-reactive document.hidden here can disable polling after returning.
    refetchInterval: 2_000,
    refetchOnWindowFocus: false
  }));
  const focusedRun = createQuery<Run>(() => ({
    queryKey: ["focused-run", focusRunId],
    queryFn: ({ signal }) => api<Run>(`/runs/${encodeURIComponent(focusRunId)}`, { signal }),
    enabled: Boolean(focusRunId),
    staleTime: 60_000,
    refetchOnWindowFocus: false
  }));

  $effect(() => {
    const recent = recentRuns.data?.items;
    if (!recent?.length) return;
    queryClient.setQueryData<InstanceRunsPage>(instanceRunsQueryKey(instance.id), (current) => {
      if (!current) return current;
      return mergeInstanceRunPages(current, recentRuns.data ?? { items: recent }, {
        mode: "prepend",
        nextCursor: current.next_cursor
      });
    });
  });
  const runItems = $derived(
    [...(runs.data?.items ?? []), ...(focusedRun.data && focusedRun.data.issue_agent_instance_id === instance.id ? [focusedRun.data] : [])]
      .filter((item, index, values) => values.findIndex((candidate) => candidate.id === item.id) === index)
      .sort((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at) || right.id.localeCompare(left.id))
  );

  $effect(() => {
    const focusKey = `${instance.id}:${focusRunId}:${focusEntryAnchor}`;
    if (!focusRunId || appliedFocusKey === focusKey || !timelineEntries.length) return;
    const matchingEntries = timelineEntries
      .map((entry, index) => ({ entry, index }))
      .filter(({ entry }) => entry.runId === focusRunId);
    const target = focusEntryAnchor === "input"
      ? matchingEntries.find(({ entry }) => entry.kind === "user")
      : focusEntryAnchor.startsWith("steer:")
        ? matchingEntries.find(({ entry }) => entry.id.includes(focusEntryAnchor))
        : focusEntryAnchor.startsWith("assistant")
          ? matchingEntries.find(({ entry }) => entry.kind === "assistant" && entry.id.includes(focusEntryAnchor))
            ?? [...matchingEntries].reverse().find(({ entry }) => entry.kind === "assistant")
          : matchingEntries[0];
    const targetIndex = target?.index ?? -1;
    if (targetIndex < 0) return;
    const needed = timelineEntries.length - targetIndex;
    timelineRenderLimit = Math.min(maximumRenderedTimelineEntries, Math.max(timelineRenderLimit, needed));
    stickToBottom = false;
    appliedFocusKey = focusKey;
    void tick().then(() => {
      const selector = focusEntryAnchor
        ? `[data-run-id="${escapeSelector(focusRunId)}"][data-entry-anchor="${escapeSelector(focusEntryAnchor)}"]`
        : `[data-run-id="${escapeSelector(focusRunId)}"]`;
      const target = timeline?.querySelector<HTMLElement>(selector)
        ?? timeline?.querySelector<HTMLElement>(`[data-run-id="${escapeSelector(focusRunId)}"]`);
      if (!target) return;
      target.scrollIntoView({ behavior: "smooth", block: "center" });
      target.classList.add("search-target-flash");
      window.setTimeout(() => target.classList.remove("search-target-flash"), 2200);
    });
  });
  const activeRun = $derived(runItems.find((item) => !isTerminal(item.status)));

  $effect(() => {
    const currentRunId = activeRun?.id ?? "";
    if (observedActiveRunId && observedActiveRunId !== currentRunId) {
      void queryClient.invalidateQueries({ queryKey: runGitChangesQueryKey(observedActiveRunId) });
      void queryClient.invalidateQueries({ queryKey: ["instance-git-changes", instance.id] });
    }
    observedActiveRunId = currentRunId;
  });

  const runIDs = $derived(runItems.map((item) => item.id).join(","));
  const runIDSet = $derived(new Set(runItems.map((item) => item.id)));
  const eventLoadLimited = createConcurrencyLimiter(2);

  const currentGoal = createQuery<CurrentIssueAgentGoal>(() => ({
    queryKey: ["instance-goal", instance.id],
    queryFn: () => api<CurrentIssueAgentGoal>("/agent-instances/" + instance.id + "/goal"),
    refetchInterval: 1_000
  }));
  const goal = $derived(currentGoal.data?.goal ?? null);
  const liveGoal = $derived(
    goal && ["active", "paused", "blocked", "budget_reached"].includes(goal.status) ? goal : null
  );

  const events = createQuery<EventHistory>(() => ({
    queryKey: instanceEventsQueryKey(instance.id),
    queryFn: async ({ signal }) => {
      const invalidations = changeInvalidationHandler;
      const existing = queryClient.getQueryData<EventHistory>(instanceEventsQueryKey(instance.id));
      const buffer = existing?.buffer ?? new BoundedRunEventBuffer();
      for (const event of existing?.items ?? []) buffer.upsert(event);
      const nextByRun: Record<string, string | undefined> = { ...(existing?.nextByRun ?? {}) };
      const statusByRun = { ...(existing?.statusByRun ?? {}) };
      const latestByRun = new Map<string, number>();
      for (const event of buffer.values()) {
        latestByRun.set(event.run_id, Math.max(latestByRun.get(event.run_id) ?? 0, event.seq));
      }
      const chronologicalRuns = [...runItems].reverse().filter((run) =>
        !Object.prototype.hasOwnProperty.call(nextByRun, run.id) ||
        !isTerminal(run.status) || statusByRun[run.id] !== run.status
      );
      // Process at most two bounded HTTP pages at a time. Raw page objects are
      // projected and released before the next pair, so a large Run list never
      // accumulates every response payload in Promise.all.
      for (let index = 0; index < chronologicalRuns.length; index += 2) {
        signal.throwIfAborted();
        const pair = chronologicalRuns.slice(index, index + 2);
        const pages = await Promise.all(pair.map((run) => eventLoadLimited(async () => ({
          run,
          page: await api<Page<RunEvent>>(`/runs/${encodeURIComponent(run.id)}/events?after=${latestByRun.get(run.id) ?? 0}&limit=100&view=activity`, { signal })
        }))));
        for (const { run, page } of pages) {
          const projectedEvents: RunEvent[] = [];
          for (const raw of page.items) {
            const event = projectActivityRunEvent(raw, run.id);
            if (!event) continue;
            buffer.upsert(event);
            projectedEvents.push(event);
          }
          invalidations.ingestPage(projectedEvents);
          // A live tail refresh must not overwrite an older, still-unconsumed
          // history cursor. Both paths share sequence-deduplicated retention.
          if (!nextByRun[run.id]) nextByRun[run.id] = page.next_cursor;
          statusByRun[run.id] = run.status;
        }
      }
      return eventHistory(buffer, nextByRun, statusByRun);
    },
    enabled: Boolean(runIDs),
    staleTime: 60_000,
    refetchOnWindowFocus: false
  }));
  const eventsReady = $derived(Boolean(events.data));

  $effect(() => {
    const history = events.data;
    const states = runItems.map((run) => `${run.id}:${isTerminal(run.status) ? run.status : "active"}`).join(",");
    if (!history || !runIDs || observedEventRunIDs === states) return;
    observedEventRunIDs = states;
    if (runItems.some((run) => !Object.prototype.hasOwnProperty.call(history.nextByRun, run.id) ||
      isTerminal(run.status) && history.statusByRun?.[run.id] !== run.status)) {
      void events.refetch();
    }
  });

  const approvals = createQuery<Page<Approval>>(() => ({
    queryKey: ["instance-approvals", instance.id, runIDs],
    queryFn: ({ signal }) => api<Page<Approval>>("/approvals", { signal }),
    enabled: Boolean(runIDs),
    refetchInterval: 1_000
  }));
  const instanceApprovals = $derived(
    (approvals.data?.items ?? []).filter((item) => runIDSet.has(item.run_id))
  );

  $effect(() => {
    void liveStreamRevision;
    const runId = activeRun?.id;
    if (!runId || !runIDs || !eventsReady) return;
    const queryKey = instanceEventsQueryKey(instance.id);
    const existing = queryClient.getQueryData<EventHistory>(queryKey)?.items ?? [];
    const afterSeq = existing
      .filter((event) => event.run_id === runId)
      .reduce((maximum, event) => Math.max(maximum, event.seq), 0);
    let latestSequence = afterSeq;
    const history = queryClient.getQueryData<EventHistory>(queryKey);
    const requestedInstanceId = instance.id;
    const buffer = history?.buffer ?? new BoundedRunEventBuffer();
    const invalidations = changeInvalidationHandler;
    for (const event of existing) buffer.upsert(event);
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    const flush = () => {
      flushTimer = undefined;
      queryClient.setQueryData<EventHistory>(queryKey, (current) =>
        eventHistory(buffer, current?.nextByRun ?? {}, current?.statusByRun)
      );
    };
    const unsubscribe = subscribeRunEvents({
      runId,
      afterSeq,
      onEvent: (event) => {
        invalidations.ingest(event);
        if (event.seq <= latestSequence || buffer.has(event.run_id, event.seq)) return;
        latestSequence = event.seq;
        buffer.upsert(event);
        if (!flushTimer) flushTimer = setTimeout(flush, 50);
      },
      onTerminal: () => {
        if (flushTimer) clearTimeout(flushTimer);
        flush();
        void events.refetch();
        void queryClient.invalidateQueries({ queryKey: instanceRunsQueryKey(instance.id) });
        void queryClient.invalidateQueries({ queryKey: ["issue-instances", issueId] });
        void queryClient.invalidateQueries({ queryKey: ["issue-comments", issueId] });
        void queryClient.invalidateQueries({ queryKey: ["instance-goal", instance.id] });
        void queryClient.invalidateQueries({ queryKey: runGitChangesQueryKey(runId) });
        void queryClient.invalidateQueries({ queryKey: ["instance-git-changes", instance.id] });
      },
      onState: (state) => {
        if (state !== "retrying") return;
        // EventSource errors include a closed terminal stream. Reconcile both
        // authorities, rather than retrying forever with a stale active Run.
        void runs.refetch();
        void events.refetch();
      },
      onGap: () => {
        // A persisted gap or an unrecognized event cannot be repaired by
        // replaying the same cursor forever. HTTP advances retained history;
        // then resubscribe from its reconciled high-water mark.
        void runs.refetch();
        void events.refetch().then((result) => {
          if (!result.isError && !destroyed && instance.id === requestedInstanceId) liveStreamRevision++;
        });
      }
    });
    return () => {
      if (flushTimer) {
        clearTimeout(flushTimer);
        flush();
      }
      unsubscribe();
    };
  });

  onMount(() => {
    const reconcileVisibleSession = () => {
      if (document.hidden) return;
      void runs.refetch();
      void recentRuns.refetch();
      void events.refetch();
      void currentGoal.refetch();
    };
    document.addEventListener("visibilitychange", reconcileVisibleSession);
    window.addEventListener("online", reconcileVisibleSession);
    return () => {
      document.removeEventListener("visibilitychange", reconcileVisibleSession);
      window.removeEventListener("online", reconcileVisibleSession);
    };
  });

  async function loadMoreEvents() {
    const history = events.data;
    if (!history || loadingMoreEvents) return;
    const pending = Object.entries(history.nextByRun).filter((entry): entry is [string, string] => Boolean(entry[1]));
    if (!pending.length) return;
    eventPageController?.abort();
    const controller = new AbortController();
    eventPageController = controller;
    const requestedInstanceId = instance.id;
    const requestedRunIDs = runIDs;
    const invalidations = changeInvalidationHandler;
    loadingMoreEvents = true;
    eventHistoryError = undefined;
    try {
      const buffer = history.buffer;
      const nextByRun = { ...history.nextByRun };
      for (let index = 0; index < pending.length; index += 2) {
        controller.signal.throwIfAborted();
        const pair = pending.slice(index, index + 2);
        const pages = await Promise.all(pair.map(([runId, after]) => eventLoadLimited(async () => ({
          runId, after,
          page: await api<Page<RunEvent>>(`/runs/${encodeURIComponent(runId)}/events?after=${encodeURIComponent(after)}&limit=100&view=activity`, { signal: controller.signal })
        }))));
        for (const { runId, after, page } of pages) {
          const projectedEvents: RunEvent[] = [];
          for (const event of page.items) {
            const projected = projectActivityRunEvent(event, runId);
            if (!projected) continue;
            buffer.upsert(projected);
            projectedEvents.push(projected);
          }
          invalidations.ingestPage(projectedEvents);
          // 没有进展的游标必须清空，否则自动加载会原地循环。
          nextByRun[runId] = page.next_cursor && page.next_cursor !== after
            ? page.next_cursor
            : undefined;
        }
      }
      if (controller.signal.aborted || instance.id !== requestedInstanceId || runIDs !== requestedRunIDs) return;
      queryClient.setQueryData<EventHistory>(
        instanceEventsQueryKey(requestedInstanceId),
        eventHistory(buffer, nextByRun,
          queryClient.getQueryData<EventHistory>(instanceEventsQueryKey(requestedInstanceId))?.statusByRun)
      );
    } catch (error) {
      if (!(error instanceof DOMException && error.name === "AbortError")) eventHistoryError = error;
    } finally {
      if (eventPageController === controller) {
        eventPageController = undefined;
        loadingMoreEvents = false;
      }
    }
  }

  async function loadOlderRuns() {
    const cursor = runs.data?.next_cursor;
    if (!cursor || loadingOlderRuns) return;
    runPageController?.abort();
    const controller = new AbortController();
    runPageController = controller;
    const requestedInstanceId = instance.id;
    const previousScrollHeight = timeline?.scrollHeight ?? 0;
    loadingOlderRuns = true;
    runHistoryError = undefined;
    try {
      const page = await fetchInstanceRunsPage(requestedInstanceId, cursor, controller.signal);
      if (controller.signal.aborted || instance.id !== requestedInstanceId) return;
      queryClient.setQueryData<InstanceRunsPage>(instanceRunsQueryKey(requestedInstanceId), (current) =>
        mergeInstanceRunPages(current, page, { mode: "append", nextCursor: page.next_cursor })
      );
      if (timeline && !stickToBottom) {
        await tick();
        timeline.scrollTop += Math.max(0, timeline.scrollHeight - previousScrollHeight);
      }
    } catch (error) {
      if (!(error instanceof DOMException && error.name === "AbortError")) runHistoryError = error;
    } finally {
      if (runPageController === controller) {
        runPageController = undefined;
        loadingOlderRuns = false;
      }
    }
  }

  function asRecord(value: unknown): Record<string, unknown> | null {
    return value !== null && typeof value === "object" && !Array.isArray(value)
      ? value as Record<string, unknown>
      : null;
  }

  function stringValue(value: unknown): string {
    return typeof value === "string" ? value : "";
  }

  function numberValue(value: unknown): number | undefined {
    return typeof value === "number" && Number.isFinite(value) ? value : undefined;
  }

  function safeJSON(value: unknown): string {
    if (value === undefined || value === null || value === "") return "";
    if (typeof value === "string") return value;
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }

  function visibleRunInput(run: Run): string {
    // The API supplies genuine user input only. Never reconstruct a message
    // from the model's Issue context or public comment history.
    return visibleTextContent(run.input).trim();
  }

  function changedPaths(item: Record<string, unknown>): string[] {
    const candidates = (Array.isArray(item.changes) ? item.changes : [])
      .map((change) => stringValue(asRecord(change)?.path));
    if (Array.isArray(item.paths)) candidates.push(...item.paths.map(stringValue));
    candidates.push(stringValue(item.path));
    return [...new Set(candidates
      .filter((path) => path && !protocolNoise(path))
      .map(safeRunPath)
      .filter(Boolean))];
  }

  function changedPathSummary(paths: string[]): string {
    const shown = paths.slice(0, 3).join(", ");
    return paths.length > 3 ? `${shown} … (+${paths.length - 3})` : shown;
  }

  function protocolNoise(value: unknown): boolean {
    const text = typeof value === "string" ? value : safeJSON(value);
    return text.includes(".teamva/tool-requests/")
      || text.includes(".teamva/tool-results/")
      || text.includes("TEAMVA_TOOL_REQUEST");
  }

  function firstActionValue(value: unknown): string {
    if (typeof value === "string") return value;
    const record = asRecord(value);
    if (!record) return "";
    for (const key of ["command", "cmd", "filePath", "file_path", "path", "cwd", "pattern", "query", "description", "title"]) {
      const candidate = stringValue(record[key]);
      if (candidate.trim()) return candidate;
    }
    return "";
  }

  function approvalText(payload: Record<string, unknown>): string {
    const action = asRecord(payload.normalized_action);
    return firstActionValue(action)
      || stringValue(payload.action_kind)
      || t("issues.sessionApproval");
  }

  function nestedErrorText(payload: Record<string, unknown>): string {
    return diagnosticText(payload, t("issues.sessionFailed"));
  }

  const collaborationToolLabels: Record<string, string> = {
    issue_comment_create: "issues.sessionCollabComment",
    issue_comment_update_own: "issues.sessionCollabCommentUpdate",
    issue_get: "issues.sessionCollabIssueGet",
    issue_agents_list: "issues.sessionCollabAgentsList",
    issue_agent_status: "issues.sessionCollabAgentStatus",
    issue_agent_context: "issues.sessionCollabAgentContext"
  };

  function providerToolLabel(tool: string): string {
    const value = tool.toLocaleLowerCase();
    const collaboration = collaborationToolLabels[value];
    if (collaboration) return t(collaboration);
    if (value.includes("bash") || value.includes("shell") || value.includes("command") || value.includes("terminal")) {
      return t("issues.sessionShellCommand");
    }
    if (value === "read" || value.includes("read_file") || value.includes("readfile")) {
      return t("issues.sessionReadFile");
    }
    if (value.includes("write") || value.includes("edit") || value.includes("patch")) {
      return t("issues.sessionWriteFile");
    }
    if (value.includes("grep") || value.includes("glob") || value.includes("search") || value.includes("find")) {
      return t("issues.sessionSearch");
    }
    return tool || t("issues.sessionTool");
  }

  function toolSummary(tool: string, input: unknown): string {
    const record = asRecord(input);
    return stringValue(record?.summary) || firstActionValue(input) || tool;
  }

  // provider.tool（FileBridge 事件）同时携带参数摘要与返回摘要。
  function toolEventDetail(payload: Record<string, unknown>): string {
    const sections: string[] = [];
    const input = asRecord(payload.input);
    if (input) sections.push("参数\n" + safeJSON(input));
    const output = payload.output ?? payload.result ?? payload.error;
    if (output !== undefined && output !== null && output !== "") {
      sections.push("返回\n" + safeJSON(output));
    }
    if (typeof payload.exit_code === "number") sections.push(safeJSON({ exit_code: payload.exit_code }));
    return sections.join("\n\n");
  }

  function moment(value: unknown): number | undefined {
    const numeric = numberValue(value);
    if (numeric !== undefined) return numeric;
    if (typeof value !== "string") return undefined;
    const parsed = Date.parse(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  }

  function durationFrom(value: Record<string, unknown>): number | undefined {
    const direct = numberValue(value.durationMs) ?? numberValue(value.duration_ms);
    if (direct !== undefined) return Math.max(0, direct);
    const time = asRecord(value.time);
    const start = moment(time?.start ?? time?.started_at);
    const end = moment(time?.end ?? time?.finished_at);
    return start !== undefined && end !== undefined && end >= start ? end - start : undefined;
  }

  function runScopedID(runId: string, value: string): string {
    return `${runId}\u0000${value}`;
  }

  function approvalStatusLabel(status: string): string {
    switch (status) {
      case "pending": return t("issues.sessionApprovalPending");
      case "approved": return t("issues.sessionApprovalApproved");
      case "denied": return t("issues.sessionApprovalDenied");
      case "expired": return t("issues.sessionApprovalExpired");
      case "cancelled": return t("issues.sessionApprovalCancelled");
      default: return t("issues.sessionApprovalUnavailable");
    }
  }

  function formatGoalTokens(value: number): string {
    if (value >= 1_000_000) return (value / 1_000_000).toFixed(value >= 10_000_000 ? 0 : 1) + "M";
    if (value >= 1_000) return (value / 1_000).toFixed(value >= 10_000 ? 0 : 1) + "K";
    return String(value);
  }

  function goalStatusLabel(item: IssueAgentGoal): string {
    if (item.status === "active" && item.phase === "evaluating") return t("issues.goalEvaluating");
    if (item.status === "active" && item.phase === "finalizing") return t("issues.goalFinalizing");
    switch (item.status) {
      case "active": return t("issues.goalActive");
      case "paused": return t("issues.goalPaused");
      case "blocked": return t("issues.goalBlocked");
      case "budget_reached": return t("issues.goalBudgetReached");
      case "completed": return t("issues.goalCompleted");
      default: return t("issues.goalCancelled");
    }
  }

  function goalStatusClass(item: IssueAgentGoal): string {
    if (item.status === "completed") return "text-success";
    if (item.status === "blocked" || item.status === "budget_reached") return "text-warning";
    if (item.status === "paused" || item.status === "cancelled") return "text-muted";
    return "text-accent";
  }

  const timelineEntries = $derived.by(() => {
    const allEvents = projectRunActivity(events.data?.items ?? []);
    const eventsByRun = new Map<string, RunEvent[]>();
    const latestToolEvent = new Map<string, string>();
    const latestRetryEvent = new Map<string, string>();
    const latestCompactionEvent = new Map<string, string>();
    const finalMessageRuns = new Set<string>();

    for (const event of allEvents) {
      const runEvents = eventsByRun.get(event.run_id) ?? [];
      runEvents.push(event);
      eventsByRun.set(event.run_id, runEvents);
    }

    for (const event of allEvents) {
      if (event.event_type === "output.snapshot" && stringValue(event.payload.text)) finalMessageRuns.add(event.run_id);
      if (event.event_type === "provider.tool") {
        const toolId = stringValue(event.payload.id)
          || stringValue(event.payload.call_id)
          || stringValue(event.payload.request_id);
        if (toolId) latestToolEvent.set(runScopedID(event.run_id, toolId), event.id);
      }
      if (event.payload.willRetry === true) latestRetryEvent.set(event.run_id, event.id);
      if (event.event_type === "context.compaction") {
        const compactionID = stringValue(event.payload.id) || event.id;
        latestCompactionEvent.set(event.run_id + ":" + compactionID, event.id);
      }
    }

    const approvalByProviderRef = new Map<string, Approval>();
    for (const item of instanceApprovals) {
      if (item.provider_approval_ref) {
        approvalByProviderRef.set(item.run_id + ":" + item.provider_approval_ref, item);
      }
    }

    const entries: SessionEntry[] = [];
    const chronologicalRuns = [...runItems].sort(
      (left, right) => Date.parse(left.created_at) - Date.parse(right.created_at)
    );

    for (const run of chronologicalRuns) {
      const entryID = (value: string) => runScopedID(run.id, value);
      const input = visibleRunInput(run);
      const initialAttachments = runAttachments(run);
      if (input || initialAttachments.length) {
        entries.push({
          id: entryID("input"),
          runId: run.id,
          kind: "user",
          label: t("issues.you"),
          text: input,
          attachments: initialAttachments,
          occurredAt: run.created_at
        });
      }

      // Initial pages, load-more pages and the guarded SSE append are each
      // sequence-ordered per Run, so filtering through this index preserves
      // order without sorting the complete event history on every SSE event.
      const runEvents = eventsByRun.get(run.id) ?? [];
      const runActive = !isTerminal(run.status) && !runEvents.some(isRunTerminalEvent);
      let assistantText = "";
      let assistantStartedAt = run.created_at;
      let runHasTerminalError = false;

      const flushAssistant = (id: string) => {
        if (!assistantText.trim()) return;
        entries.push({
          id: entryID(id),
          runId: run.id,
          kind: "assistant",
          label: agentName,
          text: assistantText,
          occurredAt: assistantStartedAt
        });
        assistantText = "";
      };

      const commitAssistantSnapshot = (id: string, message: string, occurredAt: string) => {
        if (assistantText.trim()) {
          const streamed = assistantText;
          if (message.startsWith(streamed) || streamed.startsWith(message)) {
            assistantText = message;
            flushAssistant(id);
            return;
          }
          flushAssistant("assistant-delta-before:" + id);
        }
        entries.push({
          id: entryID(id),
          runId: run.id,
          kind: "assistant",
          label: agentName,
          text: message,
          occurredAt
        });
      };

      for (const event of runEvents) {
        if (event.event_type === "user.steer") {
          flushAssistant("assistant-before:" + event.id);
          const text = stringValue(event.payload.text);
          const steerAttachments = sessionAttachments(event.payload.attachments);
          if ((text || steerAttachments.length) && event.payload.internal !== true) {
            entries.push({
              id: entryID("steer:" + event.id),
              runId: run.id,
              kind: "user",
              label: t("issues.you"),
              text,
              attachments: steerAttachments,
              occurredAt: event.occurred_at
            });
          }
          continue;
        }

        if (event.event_type === "context.compaction") {
          const compactionID = stringValue(event.payload.id) || event.id;
          if (latestCompactionEvent.get(event.run_id + ":" + compactionID) !== event.id) continue;
          flushAssistant("assistant-before:" + event.id);
          const compaction = compactionPresentation(stringValue(event.payload.status), runActive);
          const message = diagnosticText(event.payload);
          entries.push({
            id: entryID("compaction:" + compactionID),
            runId: run.id,
            kind: "compaction",
            label: compaction.label,
            text: message || compaction.text,
            detail: message || undefined,
            occurredAt: event.occurred_at,
            status: compaction.status
          });
          continue;
        }

        if (event.event_type === "output.delta") {
          if (!isTerminal(run.status) || !finalMessageRuns.has(run.id)) {
            const chunk = stringValue(event.payload.text);
            if (chunk) {
              if (!assistantText) assistantStartedAt = event.occurred_at;
              assistantText += chunk;
            }
          }
          continue;
        }

        if (event.event_type === "output.snapshot") {
          const message = stringValue(event.payload.text);
          if (message) commitAssistantSnapshot("assistant:" + (stringValue(event.payload.id) || event.id), message, event.occurred_at);
          continue;
        }

        if (event.event_type === "reasoning.snapshot" || event.event_type === "files.changed") {
          const thinking = event.event_type === "reasoning.snapshot";
          const paths = thinking ? [] : changedPaths(event.payload);
          if (!thinking && paths.length === 0) continue;
          flushAssistant("assistant-before:" + event.id);
          entries.push({ id: entryID(event.event_type + ":" + (stringValue(event.payload.id) || event.id)), runId: run.id,
            kind: thinking ? "reasoning" : "change", label: t(thinking ? "issues.sessionThinking" : "issues.sessionChanges"),
            text: thinking ? stringValue(event.payload.text) : changedPathSummary(paths), occurredAt: event.occurred_at,
            status: settledActivityStatus(stringValue(event.payload.status), runActive), durationMs: durationFrom(event.payload) });
          continue;
        }

        if (event.event_type === "provider.tool") {
          const toolId = stringValue(event.payload.id)
            || stringValue(event.payload.call_id)
            || stringValue(event.payload.request_id);
          if (toolId && latestToolEvent.get(runScopedID(event.run_id, toolId)) !== event.id) continue;
          const tool = stringValue(event.payload.tool);
          const input = event.payload.input ?? event.payload.args;
          const summary = toolSummary(tool, input);
          flushAssistant("assistant-before:" + event.id);
          entries.push({
            id: entryID("provider-tool:" + (toolId || event.id)),
            runId: run.id,
            kind: "tool",
            label: providerToolLabel(tool),
            text: summary,
            detail: toolEventDetail(event.payload),
            occurredAt: event.occurred_at,
            status: settledActivityStatus(stringValue(event.payload.status), runActive),
            durationMs: durationFrom(event.payload)
          });
          continue;
        }

        if (event.event_type === "approval.requested") {
          flushAssistant("assistant-before:" + event.id);
          const providerRef = stringValue(event.payload.request_id);
          const matched = approvalByProviderRef.get(run.id + ":" + providerRef)
            ?? instanceApprovals.find((item) =>
              item.run_id === run.id
              && item.action_kind === stringValue(event.payload.action_kind)
            );
          entries.push({
            id: entryID("approval:" + event.id),
            runId: run.id,
            kind: "approval",
            label: t("issues.sessionApproval"),
            text: approvalText(event.payload),
            occurredAt: event.occurred_at,
            status: matched?.status ?? "unavailable",
            approval: matched
          });
          continue;
        }

        if (event.payload.willRetry === true) {
          if (latestRetryEvent.get(run.id) === event.id) {
            flushAssistant("assistant-before:" + event.id);
            entries.push({
              id: entryID("retry:" + event.id),
              runId: run.id,
              kind: "retry",
              label: t("issues.sessionRetrying"),
              text: nestedErrorText(event.payload),
              detail: nestedErrorText(event.payload),
              occurredAt: event.occurred_at,
              status: settledActivityStatus("running", runActive)
            });
          }
          continue;
        }

        if (isFailureEvent(event)) {
          flushAssistant("assistant-before:" + event.id);
          entries.push({
            id: entryID("error:" + event.id),
            runId: run.id,
            kind: "error",
            label: t("issues.sessionFailed"),
            text: nestedErrorText(event.payload),
            detail: nestedErrorText(event.payload),
            occurredAt: event.occurred_at
          });
          runHasTerminalError = true;
        }
      }
      flushAssistant("assistant-delta:" + run.id);

      if (run.status === "failed" && !runHasTerminalError) {
        const reason = run.terminal_reason || run.status;
        entries.push({
          id: entryID("run-error"),
          runId: run.id,
          kind: "error",
          label: t("issues.sessionFailed"),
          text: reason,
          occurredAt: run.finished_at ?? run.updated_at
        });
      }
    }
    return entries;
  });
  const renderedTimelineEntries = $derived(timelineEntries.slice(-timelineRenderLimit));
  const hiddenTimelineEntryCount = $derived(Math.max(0, timelineEntries.length - renderedTimelineEntries.length));
  const canRenderOlderTimelineEntries = $derived(
    hiddenTimelineEntryCount > 0 && timelineRenderLimit < maximumRenderedTimelineEntries
  );

  function upsertRun(item: Run, fallbackInstanceId = instance.id) {
    const targetInstanceId = item.issue_agent_instance_id || fallbackInstanceId;
    queryClient.setQueryData<InstanceRunsPage>(instanceRunsQueryKey(targetInstanceId), (page) =>
      mergeInstanceRunPages(page, { items: [item] }, {
        mode: "prepend",
        nextCursor: page?.next_cursor
      })
    );
  }

  interface SendInput {
    draftKey: string;
    snapshot: ComposerDraft;
    issueId: string;
    instanceId: string;
    activeRunId?: string;
    expectedIssueVersion: number;
    goalTokenBudget?: number;
    key: string;
  }

  function submitMessage(event?: SubmitEvent) {
    event?.preventDefault();
    if (!canSend || draftSubmitting) return;
    saveDraft();
    const snapshot = readComposerDraft(draftKey);
    if (!beginComposerSubmission(draftKey)) return;
    send.mutate({
      draftKey, snapshot, issueId, instanceId: instance.id, activeRunId: activeRun?.id,
      expectedIssueVersion,
      goalTokenBudget: goalArmed && goalBudgetEnabled ? goalTokenBudget : undefined,
      key: idempotencyKey(activeRun ? "side-sheet-steer" : "side-sheet")
    });
  }

  // Sending is not aborted when a sheet closes: its late response settles only
  // the captured draft, so reopening cannot resend a still-pending submission.
  const send = createMutation<Run, Error, SendInput>(() => ({
    mutationFn: (input) => input.activeRunId
      ? api<Run>("/runs/" + input.activeRunId + "/steer", {
          body: { input: input.snapshot.text.trim(), attachments: imageAttachmentBlocks(input.snapshot.attachments) },
          headers: { "Idempotency-Key": input.key }
        })
      : (() => {
        return api<Run>("/issues/" + input.issueId + "/runs", {
          body: issueRunRequestBody({
            instanceId: input.instanceId,
            input: input.snapshot.text.trim(),
            attachments: imageAttachmentBlocks(input.snapshot.attachments),
            expectedIssueVersion: input.expectedIssueVersion,
            goal: input.snapshot.goalArmed || undefined,
            goalTokenBudget: input.goalTokenBudget
          }),
          headers: { "Idempotency-Key": input.key }
        });
      })(),
    onSuccess: (item, input) => {
      const targetInstanceId = item.issue_agent_instance_id || input.instanceId;
      completeComposerSubmission(input.draftKey, input.snapshot);
      upsertRun(item, input.instanceId);
      if (!destroyed && draftKey === input.draftKey) {
        attachmentError = "";
        attachmentPreview = undefined;
        stickToBottom = true;
        onInstanceChange();
      }
      queryClient.invalidateQueries({ queryKey: ["issue-instances", input.issueId] });
      queryClient.invalidateQueries({ queryKey: instanceRunsQueryKey(targetInstanceId) });
      queryClient.invalidateQueries({ queryKey: ["instance-events", targetInstanceId] });
      queryClient.invalidateQueries({ queryKey: ["instance-goal", targetInstanceId] });
    },
    onSettled: (_item, _error, input) => finishComposerSubmission(input.draftKey)
  }));

  const revise = createMutation<Run, Error, { issueId: string; instanceId: string; expectedIssueVersion: number }>(() => ({
    mutationFn: (input) => runCancellableAction((signal) => api<Run>("/issues/" + input.issueId + "/runs", {
        body: issueRunRequestBody({
          instanceId: input.instanceId,
          intent: "revise_comment",
          internal: true,
          expectedIssueVersion: input.expectedIssueVersion
        }),
        headers: { "Idempotency-Key": idempotencyKey("revise") },
        signal
      })),
    onSuccess: (item, input) => {
      const targetInstanceId = item.issue_agent_instance_id || input.instanceId;
      upsertRun(item, input.instanceId);
      if (!destroyed && instance.id === input.instanceId) {
        stickToBottom = true;
        onInstanceChange();
      }
      queryClient.invalidateQueries({ queryKey: ["issue-comments", input.issueId] });
      queryClient.invalidateQueries({ queryKey: instanceRunsQueryKey(targetInstanceId) });
      queryClient.invalidateQueries({ queryKey: ["instance-events", targetInstanceId] });
    }
  }));

  const stop = createMutation<Run, Error, { instanceId: string; runId: string }>(() => ({
    mutationFn: (input) => {
      return runCancellableAction((signal) => api<Run>("/runs/" + input.runId + "/stop", {
        body: { mode: "graceful" }, signal
      }));
    },
    onSuccess: (item, input) => {
      if (!destroyed && instance.id === input.instanceId) onInstanceChange();
      queryClient.invalidateQueries({ queryKey: instanceRunsQueryKey(item.issue_agent_instance_id || input.instanceId) });
    }
  }));

  const changeGoal = createMutation<
    IssueAgentGoal,
    Error,
    { action: "pause" | "resume" | "cancel"; goal: IssueAgentGoal; instanceId: string; issueId: string }
  >(() => ({
    mutationFn: ({ action, goal: item }) => api<IssueAgentGoal>("/goals/" + item.id + "/" + action, {
      body: { expected_version: item.version }
    }),
    onSuccess: (item, variables) => {
      queryClient.setQueryData<CurrentIssueAgentGoal>(
        ["instance-goal", variables.instanceId],
        variables.action === "cancel" ? { goal: null } : { goal: item }
      );
      if (!destroyed && instance.id === variables.instanceId) onInstanceChange();
      void queryClient.invalidateQueries({ queryKey: ["instance-runs", variables.instanceId] });
      void queryClient.invalidateQueries({ queryKey: ["issue-instances", variables.issueId] });
      void queryClient.invalidateQueries({ queryKey: ["issue-comments", variables.issueId] });
      void queryClient.invalidateQueries({ queryKey: ["instance-goal", variables.instanceId] });
    }
  }));

  const decideApproval = createMutation<Approval, Error, { item: Approval; decision: "approved" | "denied"; instanceId: string; issueId: string }>(() => ({
    mutationFn: ({ item, decision }) => runCancellableAction((signal) => api<Approval>("/approvals/" + item.id + "/decisions", {
      body: {
        expected_version: item.version,
        action_digest: item.action_digest,
        decision,
        scope: "once"
      },
      signal
    })),
    onSuccess: (_item, input) => {
      queryClient.invalidateQueries({ queryKey: ["approvals"] });
      queryClient.invalidateQueries({ queryKey: ["instance-approvals", input.instanceId] });
      queryClient.invalidateQueries({ queryKey: instanceRunsQueryKey(input.instanceId) });
      queryClient.invalidateQueries({ queryKey: ["issue-runs", input.issueId] });
      queryClient.invalidateQueries({ queryKey: ["issue-instances", input.issueId] });
    }
  }));

  // The opened sidebar holds the initial instance object. Once Run data is
  // loaded, its terminal state is authoritative; a stale instance snapshot
  // must not leave the spinner/Stop button running forever.
  const busy = $derived(Boolean(activeRun) || liveGoal?.status === "active" ||
    (runs.isLoading && instance.status === "running"));
  const goalObjectiveLength = $derived([...prompt].length);
  const goalObjectiveTooLong = $derived(goalArmed && goalObjectiveLength > 5_000);
  const canSend = $derived(
    Boolean(prompt.trim() || attachments.length) && !draftSubmitting && !currentGoal.isPending &&
    !goalObjectiveTooLong && !liveGoal && (!goalArmed || (Boolean(prompt.trim()) && !attachments.length))
  );
  const canRevise = $derived(!busy && !liveGoal && !revise.isPending);
  const canStop = $derived(Boolean(activeRun) && !stop.isPending);
  const queryError = $derived(
    runs.error ?? recentRuns.error ?? focusedRun.error ?? events.error ?? approvals.error ?? runHistoryError ?? eventHistoryError ??
    currentGoal.error
  );
  const composerError = $derived(
    (send.variables?.draftKey === draftKey ? send.error : null) ??
    (revise.variables?.instanceId === instance.id ? revise.error : null) ??
    (stop.variables?.instanceId === instance.id ? stop.error : null) ??
    (changeGoal.variables?.instanceId === instance.id ? changeGoal.error : null) ??
    (decideApproval.variables?.instanceId === instance.id ? decideApproval.error : null)
  );

  function retryQueries() {
    runHistoryError = undefined;
    eventHistoryError = undefined;
    void runs.refetch();
    void recentRuns.refetch();
    if (focusRunId) void focusedRun.refetch();
    if (runIDs) {
      void events.refetch();
      void approvals.refetch();
    }
    void currentGoal.refetch();
  }
  const hasOlderRuns = $derived(Boolean(runs.data?.next_cursor && !runs.data?.history_limited));
  const runHistoryLimited = $derived(Boolean(runs.data?.history_limited || recentRuns.data?.history_limited));
  const runContentLimited = $derived(Boolean(runs.data?.content_limited || recentRuns.data?.content_limited));
  const hasMoreEvents = $derived(Object.values(events.data?.nextByRun ?? {}).some(Boolean));
  const eventHistoryLimited = $derived(Boolean(events.data?.incomplete));

  // 分页按底层事件条数（100/页），一次普通 Run 就可能产生几百条流式事件，
  // 界面上却只有几条消息。小会话在后台自动补齐，只有累计事件超过预算的
  // 长会话才需要用户手动“加载更多活动”。
  const autoLoadEntryBudget = 150;
  $effect(() => {
    const history = events.data;
    if (!history || !runIDs || loadingMoreEvents || eventHistoryError) return;
    if (!Object.values(history.nextByRun).some(Boolean)) return;
    // Many thousands of tiny deltas can describe only two visible messages.
    // Stop by semantic entries/retention, not by raw chunk count.
    if (timelineEntries.length >= autoLoadEntryBudget || history.incomplete || history.buffer.bytes >= 3 * 1024 * 1024) return;
    void loadMoreEvents();
  });

  function openChangesForRun(runId: string) {
    changesFocusRunId = runId;
    activeTab = "changes";
  }

  function navigateSessionTabs(event: KeyboardEvent) {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    activeTab = activeTab === "activity" ? "changes" : "activity";
    if (activeTab === "changes") changesFocusRunId = "";
    event.preventDefault();
    void tick().then(() => document.getElementById(`${activeTab}-tab-${instance.id}`)?.focus());
  }

  function updateScrollLock() {
    if (!timeline) return;
    stickToBottom = timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight < 72;
  }

  $effect(() => {
    void timelineEntries.length;
    void busy;
    if (activeTab === "activity" && timeline && stickToBottom) {
      void tick().then(() => {
        if (!timeline) return;
        if (typeof timeline.scrollTo === "function") timeline.scrollTo({ top: timeline.scrollHeight });
        else timeline.scrollTop = timeline.scrollHeight;
      });
    }
  });
</script>

<div class="session-sheet-backdrop fixed inset-0 z-50 flex justify-end overflow-hidden bg-black/20" class:select-none={resizing} role="dialog" aria-modal="true" aria-labelledby={sheetTitleId}>
  <button class="absolute inset-0 cursor-default border-0 bg-transparent p-0" type="button" aria-label={t("common.close")} onclick={onClose}></button>
  <aside
    class="session-sheet-panel relative flex h-full min-w-0 max-w-full flex-col overflow-hidden border-l border-border bg-surface"
    class:session-sheet-resizing={resizing}
    class:select-none={resizing}
    style:width={sheetWidth + "px"}
    bind:this={sheetPanel}
    data-project-id={projectId}
  >
    <button
      class="session-sheet-resizer group/resizer absolute -left-1 top-0 z-20 hidden h-full w-2 cursor-col-resize touch-none border-0 p-0 md:block"
      class:bg-accent-subtle={resizing}
      type="button"
      aria-label={t("issues.resizeSession")}
      title={t("issues.resizeSession")}
      onpointerdown={startResize}
      onpointermove={resizeSheet}
      onpointerup={finishResize}
      onpointercancel={finishResize}
      onkeydown={resizeWithKeyboard}
      ondblclick={() => setSheetWidth(defaultSheetWidth)}
    >
      <span class="absolute inset-y-0 left-1/2 w-px -translate-x-1/2 transition-colors group-hover/resizer:bg-accent group-focus/resizer:bg-accent {resizing ? 'bg-accent' : 'bg-transparent'}"></span>
    </button>

    <header class="flex min-h-14 min-w-0 items-center justify-between gap-2 border-b border-border px-4 py-2.5">
      <div class="flex min-w-0 items-center gap-2.5">
        <span class="grid size-7 shrink-0 place-items-center rounded-full bg-accent-subtle text-accent">
          <Bot size={14} />
        </span>
        <div class="min-w-0">
          <h3 id={sheetTitleId} class="truncate text-sm font-semibold tracking-[-0.01em]">{sessionLabel}</h3>
          <p class="flex items-center gap-1.5 text-2xs text-muted">
            <span class="size-1.5 rounded-full {busy ? 'animate-pulse bg-accent' : 'bg-border-strong'}"></span>
            {busy ? t("issues.agentWorking") : t("issues.agentIdle")}
            {#if instance.last_activity_at}<span>· {formatTime(instance.last_activity_at)}</span>{/if}
          </p>
        </div>
      </div>
      <div class="flex items-center gap-1">
        {#if busy}
          <Button
            variant="danger" controlSize="sm"
            type="button"
            disabled={!canStop}
            onclick={() => activeRun && stop.mutate({ instanceId: instance.id, runId: activeRun.id })}
          >
            <Square size={12} />{t("issues.stop")}
          </Button>
        {/if}
        <button
          class="inline-grid size-8 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-secondary hover:bg-surface-hover hover:text-text"
          type="button"
          bind:this={closeButton}
          aria-label={t("common.close")}
          onclick={onClose}
        >
          <X size={16} />
        </button>
      </div>
    </header>

    <div class="grid min-h-10 shrink-0 grid-cols-2 border-b border-border bg-surface-subtle p-1.5" role="tablist" tabindex="-1" aria-label={t("issues.agentSessionTabs")} onkeydown={navigateSessionTabs}>
      <button
        type="button"
        role="tab"
        id={`activity-tab-${instance.id}`}
        aria-controls={`activity-panel-${instance.id}`}
        tabindex={activeTab === "activity" ? 0 : -1}
        class="inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-sm border-0 text-xs font-semibold {activeTab === 'activity' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted hover:text-text'}"
        aria-selected={activeTab === "activity"}
        onclick={() => (activeTab = "activity")}
      ><Activity size={13} />{t("issues.activityTab")}</button>
      <button
        type="button"
        role="tab"
        id={`changes-tab-${instance.id}`}
        aria-controls={`changes-panel-${instance.id}`}
        tabindex={activeTab === "changes" ? 0 : -1}
        class="inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-sm border-0 text-xs font-semibold {activeTab === 'changes' ? 'bg-surface text-text shadow-sm' : 'bg-transparent text-muted hover:text-text'}"
        aria-selected={activeTab === "changes"}
        onclick={() => { changesFocusRunId = ""; activeTab = "changes"; }}
      ><GitCompareArrows size={13} />{t("issues.changesTab")}</button>
    </div>

    {#if queryError}<div class="shrink-0 px-4 pt-3"><ErrorNotice error={queryError} onRetry={retryQueries} /></div>{/if}
    {#if activeTab === "activity"}
    <div id={`activity-panel-${instance.id}`} role="tabpanel" aria-labelledby={`activity-tab-${instance.id}`} class="session-sheet-timeline min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-x-none px-4 py-3" bind:this={timeline} onscroll={updateScrollLock}>
      {#if hasOlderRuns}
        <Button type="button" controlSize="sm" class="mx-auto mb-3 flex" disabled={loadingOlderRuns} onclick={loadOlderRuns}>
          {#if loadingOlderRuns}<LoaderCircle size={12} class="mr-1.5 animate-spin" />{/if}{t("issues.loadOlderTurns")}
        </Button>
      {:else if runHistoryLimited}
        <p class="mb-3 text-center text-2xs leading-relaxed text-warning" role="status">{t("issues.sessionHistoryLimited")}</p>
      {/if}
      {#if runContentLimited}
        <p class="mb-3 text-center text-2xs leading-relaxed text-warning" role="status">{t("issues.sessionRunContentLimited")}</p>
      {/if}
      {#if eventHistoryLimited}
        <p class="mb-3 text-center text-2xs leading-relaxed text-warning" role="status">
          {t("issues.activityHistoryLimited", { count: events.data?.evictedCount ?? 0 })}
        </p>
      {/if}
      <!-- Approval metadata must not block already available execution output. -->
      {#if runs.isLoading || (runIDs && events.isLoading)}
        <Loading label={t("issues.loadingSession")} />
      {:else if !runItems.length}
        <div class="grid place-items-center py-10 text-2xs text-muted">{t("issues.noActivity")}</div>
      {:else if timelineEntries.length || busy}
        <!-- @ inputs are private context; show activity feedback before the first native item. -->
        {#if canRenderOlderTimelineEntries}
          <Button type="button" controlSize="sm" class="mx-auto mb-3 flex" onclick={() => (timelineRenderLimit = Math.min(maximumRenderedTimelineEntries, timelineRenderLimit + timelineRenderStep))}>
            {t("issues.showOlderLoadedActivity", { count: Math.min(timelineRenderStep, hiddenTimelineEntryCount) })}
          </Button>
        {:else if hiddenTimelineEntryCount > 0}
          <p class="mb-3 text-center text-2xs leading-relaxed text-warning" role="status">{t("issues.activityRenderLimited", { count: hiddenTimelineEntryCount })}</p>
        {/if}
        <ol class="grid min-w-0 max-w-full gap-2 overflow-x-hidden" aria-label={t("issues.sessionTimeline")}>
          {#each renderedTimelineEntries as entry (entry.id)}
            {#if entry.kind === "user"}
              <li class="session-timeline-entry flex min-w-0 max-w-full justify-end pl-16" data-run-id={entry.runId} data-entry-anchor={entry.id.endsWith("\u0000input") ? "input" : entry.id.includes("\u0000steer:") ? entry.id.split("\u0000").at(-1) : undefined}>
                <article class="min-w-0 max-w-[88%] overflow-hidden rounded-lg bg-surface-subtle px-3 py-2">
                  <div class="mb-1 flex items-center justify-end gap-2 text-2xs text-muted">
                    <span class="flex items-center gap-1"><User size={11} />{entry.label}</span>
                    <time>{formatTime(entry.occurredAt)}</time>
                  </div>
                  {#if entry.text}<p class="m-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-xs leading-[1.65] text-text">{entry.text}</p>{/if}
                  {#if entry.attachments?.length}
                    <div class="mt-2 flex flex-wrap justify-end gap-1.5" aria-label={t("issues.sentAttachments")}>
                      {#each entry.attachments as attachment}
                        <span class="inline-flex max-w-full items-center gap-1 rounded-sm border border-border bg-surface px-2 py-1 text-2xs text-secondary">
                          <Paperclip size={10} class="shrink-0" />
                          <span class="truncate">{attachment.filename}</span>
                        </span>
                      {/each}
                    </div>
                  {/if}
                </article>
              </li>
            {:else if entry.kind === "assistant"}
              <li class="session-timeline-entry grid min-w-0 max-w-full grid-cols-[22px_minmax(0,1fr)] items-start gap-2 overflow-hidden pr-4" data-run-id={entry.runId} data-entry-anchor={entry.id.split("\u0000").at(-1)}>
                <span class="mt-0.5 grid size-[22px] place-items-center rounded-full bg-accent-subtle text-accent"><Bot size={12} /></span>
                <article class="min-w-0 pt-0.5">
                  <div class="mb-1 flex items-center gap-2 text-2xs text-muted">
                    <strong class="font-medium text-secondary">{entry.label}</strong>
                    <time>{formatTime(entry.occurredAt)}</time>
                  </div>
                  <RichText text={entry.text} />
                </article>
              </li>
            {:else if entry.kind === "approval"}
              <li class="session-timeline-entry ml-6 min-w-0 max-w-full overflow-hidden border-l-2 border-warning/55 py-1 pl-3" data-run-id={entry.runId}>
                <div class="flex min-w-0 max-w-full items-start gap-2">
                  <span class="mt-0.5 grid size-5 shrink-0 place-items-center text-warning">
                    {#if entry.status === "approved"}<CheckCircle2 size={15} />
                    {:else if entry.status === "denied"}<XCircle size={15} />
                    {:else if entry.status === "pending"}<CircleAlert size={15} />
                    {:else}<Clock3 size={15} />{/if}
                  </span>
                  <div class="min-w-0 flex-1">
                    <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-2xs">
                      <strong class="text-text">{entry.label}</strong>
                      <span class="{entry.status === 'approved' ? 'text-success' : entry.status === 'denied' ? 'text-danger' : 'text-warning'}">{approvalStatusLabel(entry.status ?? "")}</span>
                      <time class="ml-auto text-muted">{formatTime(entry.occurredAt)}</time>
                    </div>
                    <code class="mt-1 block max-w-full whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-[11px] leading-relaxed text-code">{entry.text}</code>
                    {#if entry.status === "pending" && entry.approval}
                      <div class="mt-2 flex gap-2">
                        <Button
                          controlSize="sm"
                          type="button"
                          disabled={decideApproval.isPending}
                          onclick={() => decideApproval.mutate({ item: entry.approval!, decision: "denied", instanceId: instance.id, issueId })}
                        >
                          <X size={13} />{t("issues.denyAction")}
                        </Button>
                        <Button
                          variant="primary" controlSize="sm"
                          type="button"
                          disabled={decideApproval.isPending}
                          onclick={() => decideApproval.mutate({ item: entry.approval!, decision: "approved", instanceId: instance.id, issueId })}
                        >
                          <Check size={13} />{t("issues.allowAction")}
                        </Button>
                      </div>
                    {/if}
                  </div>
                </div>
              </li>
            {:else}
              <li class="session-timeline-entry ml-6 min-w-0 max-w-full overflow-hidden" data-run-id={entry.runId}>
                <AgentActivityRow
                  kind={entry.kind}
                  label={entry.label}
                  text={entry.text}
                  detail={entry.detail}
                  occurredAt={entry.occurredAt}
                  status={entry.status}
                  durationMs={entry.durationMs}
                  actionLabel={entry.kind === "change" ? t("issues.viewInChanges") : undefined}
                  onAction={entry.kind === "change" ? () => openChangesForRun(entry.runId) : undefined}
                />
              </li>
            {/if}
          {/each}
          {#if busy}
            <li class="session-timeline-entry ml-6 min-w-0 max-w-full overflow-hidden" data-run-id={activeRun?.id ?? ""} aria-live="polite">
              <div class="flex min-h-7 items-center gap-1.5 px-0.5 py-1 text-2xs text-secondary">
                <LoaderCircle size={13} class="animate-spin text-accent" />
                <span>{t("issues.sessionGenerating")}</span>
              </div>
            </li>
          {/if}
        </ol>
        {#if hasMoreEvents}
          <Button type="button" controlSize="sm" class="mx-auto mt-3 flex" disabled={loadingMoreEvents} onclick={loadMoreEvents}>
            {#if loadingMoreEvents}<LoaderCircle size={12} class="mr-1.5 animate-spin" />{/if}{t("issues.loadMoreActivity")}
          </Button>
        {/if}
      {:else}
        <p class="text-xs leading-relaxed text-muted">{t("issues.sessionHint")}</p>
      {/if}
    </div>

    <form
      class="relative min-w-0 overflow-hidden border-t border-border bg-surface px-4 py-3"
      ondragenter={attachmentDragEnter}
      ondragover={attachmentDragOver}
      ondragleave={attachmentDragLeave}
      ondrop={attachmentDrop}
      onsubmit={submitMessage}
    >
      {#if composerError}<div data-testid="session-composer-error"><ErrorNotice error={composerError} /></div>{/if}
      {#if attachmentDropDepth > 0}
        <div class="pointer-events-none absolute inset-2 z-20 grid place-items-center rounded-md border-2 border-dashed border-accent bg-accent-subtle/95 text-sm font-medium text-accent" role="status">
          {t("issues.dropImagesHere")}
        </div>
      {/if}
      <FileInput
        class="sr-only"
        bind:ref={attachmentInput}
        type="file"
        disabled={goalArmed || Boolean(liveGoal)}
        accept="image/png,image/jpeg,image/gif,image/webp"
        multiple
        aria-label={t("issues.addImages")}
        onchange={(event) => {
          const target = event.currentTarget;
          void addAttachmentFiles(Array.from(target.files ?? []));
          target.value = "";
        }}
      />
      {#if attachments.length}
        <div class="mb-2 flex min-w-0 flex-wrap gap-2" aria-label={t("issues.pendingAttachments")}>
          {#each attachments as attachment (attachment.id)}
            <article class="group relative h-20 w-20 overflow-hidden rounded-md border border-border bg-surface-subtle">
              <button
                type="button"
                class="block size-full cursor-zoom-in border-0 bg-transparent p-0"
                aria-label={t("issues.previewImage", { name: attachment.filename })}
                title={attachment.filename}
                onclick={() => (attachmentPreview = attachment)}
              >
                <img class="size-full object-cover" src={attachment.dataURL} alt={attachment.filename} />
              </button>
              <button
                type="button"
                class="absolute right-1 top-1 grid size-6 cursor-pointer place-items-center rounded-full border-0 bg-ink/85 text-canvas opacity-90 shadow-sm hover:bg-danger"
                aria-label={t("issues.removeImage", { name: attachment.filename })}
                onclick={() => removeAttachment(attachment.id)}
              ><Trash2 size={12} /></button>
            </article>
          {/each}
        </div>
      {/if}
      {#if attachmentError}<p class="mb-2 text-2xs leading-relaxed text-danger" role="alert">{attachmentError}</p>{/if}
      <Textarea
        class="min-h-[64px] resize-none"
        value={prompt}
        oninput={(event) => {
          prompt = event.currentTarget.value;
          saveDraft();
        }}
        aria-label={t("issues.sessionInput")}
        placeholder={busy ? t("issues.busyPlaceholder") : t("issues.sessionPlaceholder")}
        rows={2}
        onpaste={attachmentPaste}
        onkeydown={(event: KeyboardEvent) => {
          if (event.isComposing) return;
          if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
            submitMessage();
            event.preventDefault();
          }
        }}
      ></Textarea>
      {#if goal}
        <div
          class="mt-2 flex min-w-0 items-center gap-2 rounded-sm border border-border bg-surface-subtle px-2.5 py-2"
          aria-label={t("issues.goalStatus")}
          title={goal.objective}
        >
          <Target size={14} class="shrink-0 {goalStatusClass(goal)}" />
          <div class="min-w-0 flex-1">
            <p class="m-0 truncate text-xs text-text">{goal.note || goal.objective}</p>
            <p class="m-0 mt-0.5 flex flex-wrap items-center gap-x-2 text-2xs text-muted">
              <span class={goalStatusClass(goal)}>{goalStatusLabel(goal)}</span>
              <span>{t("issues.goalTurns", { used: goal.continuations_used, max: goal.max_continuations })}</span>
              {#if goal.token_budget}
                <span>{formatGoalTokens(goal.tokens_used)} / {formatGoalTokens(goal.token_budget)}</span>
              {:else if goal.tokens_used > 0}
                <span>{formatGoalTokens(goal.tokens_used)} {t("issues.goalTokens")}</span>
              {/if}
            </p>
            {#if goalBudgetExhausted(goal)}
              <p class="m-0 mt-1 text-2xs leading-relaxed text-muted">{t("goalBudget.exhausted")}</p>
            {/if}
          </div>
          {#if liveGoal}
            <div class="flex shrink-0 items-center gap-1">
              {#if liveGoal.status === "active"}
                <button
                  class="inline-grid size-7 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-muted hover:bg-surface-hover hover:text-text disabled:opacity-50"
                  type="button"
                  title={t("issues.goalPause")}
                  aria-label={t("issues.goalPause")}
                  disabled={changeGoal.isPending}
                  onclick={() => changeGoal.mutate({ action: "pause", goal: liveGoal, instanceId: instance.id, issueId })}
                ><Pause size={13} /></button>
              {:else}
                <button
                  class="inline-grid size-7 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-muted hover:bg-surface-hover hover:text-text disabled:opacity-50"
                  type="button"
                  title={t("issues.goalResume")}
                  aria-label={t("issues.goalResume")}
                  disabled={changeGoal.isPending || goalBudgetExhausted(liveGoal)}
                  onclick={() => { if (liveGoal && !goalBudgetExhausted(liveGoal)) changeGoal.mutate({ action: "resume", goal: liveGoal, instanceId: instance.id, issueId }); }}
                ><Play size={13} /></button>
              {/if}
              <button
                class="inline-grid size-7 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-muted hover:bg-danger-subtle hover:text-danger disabled:opacity-50"
                type="button"
                title={t("issues.goalCancel")}
                aria-label={t("issues.goalCancel")}
                disabled={changeGoal.isPending}
                onclick={() => changeGoal.mutate({ action: "cancel", goal: liveGoal, instanceId: instance.id, issueId })}
              ><Ban size={13} /></button>
            </div>
          {/if}
        </div>
      {/if}

      {#if goalArmed}
        <div class="mt-2 flex flex-wrap items-center gap-2 rounded-sm border border-accent/30 bg-accent-subtle px-2.5 py-2 text-2xs text-secondary">
          <span class="font-medium text-accent">{t("issues.goalArmed")}</span>
          <label class="ml-auto inline-flex cursor-pointer items-center gap-1.5">
            <Checkbox type="checkbox" bind:checked={goalBudgetEnabled} />
            <span>{t("issues.goalTokenBudget")}</span>
          </label>
          {#if goalBudgetEnabled}
            <Input controlSize="sm"
              class="w-28 px-2 py-1 text-right text-xs tabular-nums"
              type="number"
              min="1000"
              max="100000000"
              step="1"
              bind:value={goalTokenBudget}
              aria-label={t("issues.goalTokenBudget")}
              onchange={() => {
                goalTokenBudget = Math.max(1_000, Math.min(100_000_000, Math.round(goalTokenBudget || 2_000_000)));
              }}
            />
          {/if}
        </div>
      {/if}

      <div class="mt-2 flex min-w-0 flex-wrap items-center justify-between gap-2">
        <div class="flex min-w-0 items-center gap-2">
          <Button controlSize="sm"
            type="button"
            title={t("issues.addImages")}
            aria-label={t("issues.addImages")}
            disabled={goalArmed || Boolean(liveGoal) || attachments.length >= maximumImageAttachmentCount}
            onclick={() => attachmentInput?.click()}
          >
            <Paperclip size={13} />
            {attachments.length ? `${attachments.length}/${maximumImageAttachmentCount}` : t("issues.attach")}
          </Button>
          <button
            class="inline-flex min-h-8 cursor-pointer items-center gap-1.5 rounded-sm border px-2.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50 {goalArmed ? 'border-accent/40 bg-accent-subtle text-accent' : 'border-border bg-surface text-secondary hover:bg-surface-hover hover:text-text'}"
            type="button"
            aria-pressed={goalArmed}
            title={goalArmed ? t("issues.goalDisarm") : t("issues.goalArm")}
            disabled={busy || Boolean(liveGoal) || currentGoal.isPending || draftSubmitting || Boolean(attachments.length)}
            onclick={() => {
              goalArmed = !goalArmed;
              saveDraft();
            }}
          >
            <Target size={13} />
            {goalArmed ? t("issues.goalArmedShort") : t("issues.goal")}
          </button>
          {#if goalArmed && goalObjectiveLength > 0}
            <span class="shrink-0 text-2xs tabular-nums {goalObjectiveTooLong ? 'text-danger' : 'text-muted'}">
              {goalObjectiveLength}/5000
            </span>
          {/if}
        </div>
        <div class="ml-auto flex shrink-0 items-center gap-2">
          <Button
            type="button"
            aria-describedby="revise-comment-tooltip"
            disabled={!canRevise}
            onclick={() => revise.mutate({ issueId, instanceId: instance.id, expectedIssueVersion })}
          >
            <span class="group/revise-help relative grid size-4 shrink-0 place-items-center">
              <CircleHelp size={14} aria-hidden="true" />
              <span
                id="revise-comment-tooltip"
                class="pointer-events-none absolute bottom-[calc(100%+8px)] left-1/2 z-30 w-64 -translate-x-1/2 rounded-sm bg-ink px-3 py-2 text-left text-2xs font-normal leading-relaxed text-canvas opacity-0 shadow-raised transition-opacity group-hover/revise-help:opacity-100"
                role="tooltip"
              >
                {t("issues.reviseComment")}
              </span>
            </span>
            {#if revise.isPending}<LoaderCircle size={13} class="animate-spin" />{/if}
            {revise.isPending ? t("issues.revising") : t("issues.revise")}
          </Button>
          <Button type="submit" variant="primary"
            class="text-xs"
            disabled={!canSend}
          >
            <Send size={13} />{t("issues.send")}
          </Button>
        </div>
      </div>
    </form>
    {#if attachmentPreview}
      <div class="absolute inset-0 z-40 grid place-items-center bg-black/70 p-6" role="dialog" aria-modal="true" aria-label={t("issues.imagePreview")}>
        <button class="absolute inset-0 cursor-zoom-out border-0 bg-transparent" type="button" aria-label={t("common.close")} onclick={() => (attachmentPreview = undefined)}></button>
        <div class="relative z-10 flex max-h-full max-w-full flex-col items-center gap-3">
          <img class="max-h-[calc(100vh-8rem)] max-w-full rounded-md object-contain shadow-raised" src={attachmentPreview.dataURL} alt={attachmentPreview.filename} />
          <div class="flex max-w-full items-center gap-2 rounded-full bg-ink/90 px-3 py-1.5 text-xs text-canvas">
            <Eye size={12} class="shrink-0" /><span class="truncate">{attachmentPreview.filename}</span>
            <button class="ml-1 grid size-6 cursor-pointer place-items-center rounded-full border-0 bg-canvas/10 text-canvas hover:bg-canvas/20" type="button" aria-label={t("common.close")} onclick={() => (attachmentPreview = undefined)}><X size={13} /></button>
          </div>
        </div>
      </div>
    {/if}
    {:else}
      <div id={`changes-panel-${instance.id}`} role="tabpanel" aria-labelledby={`changes-tab-${instance.id}`} class="flex min-h-0 min-w-0 flex-1">
        <AgentRunChanges
          scopeId={instance.id}
          runs={runItems}
          {repositories}
          width={sheetWidth}
          focusRunId={changesFocusRunId}
          {liveChangeSignal}
          {hasOlderRuns}
          {runHistoryLimited}
          {loadingOlderRuns}
          onLoadOlderRuns={loadOlderRuns}
        />
      </div>
    {/if}
  </aside>
</div>
