import type { MentionRange } from "./mentions";
import { v4 } from "uuid";
import { copyIssueWorkItems } from "./issueWork";
import type { IssueWorkItemSpec } from "./types";

export interface PendingComposerAttachment {
  id: string;
  filename: string;
  mimeType: string;
  dataURL: string;
  size: number;
}

export interface ComposerDraftInput {
  text: string;
  mentions?: MentionRange[];
  attachments?: PendingComposerAttachment[];
  goalArmed?: boolean;
  mode?: "comment" | "work_items";
  workItems?: IssueWorkItemSpec[];
  workPlanInput?: string | null;
}

export interface ComposerDraft {
  text: string;
  mentions: MentionRange[];
  attachments: PendingComposerAttachment[];
  goalArmed: boolean;
  mode: "comment" | "work_items";
  workItems: IssueWorkItemSpec[];
  workPlanInput: string | null;
  idempotencyKey: string;
  textRevision: number;
  submitting: boolean;
}

// Session memory only: never put arbitrary user text or image data in browser storage.
// A global image budget prevents many closed sheets retaining unlimited base64 data.
export const maximumRetainedDraftImageCharacters = 8 * 1024 * 1024;
const drafts = new Map<string, ComposerDraft>();
const pending = new Set<string>();
const listeners = new Map<string, Set<(draft: ComposerDraft) => void>>();
let nextRevision = 1;
let unloadTarget: Window | undefined;

export function composerDraftKey(projectId: string, issueId: string, instanceId?: string): string {
  return JSON.stringify([projectId, issueId, instanceId ?? "comment"]);
}

export function readComposerDraft(key: string): ComposerDraft {
  const draft = drafts.get(key);
  return {
    text: draft?.text ?? "",
    mentions: draft?.mentions.map((item) => ({ ...item })) ?? [],
    attachments: draft?.attachments.map((item) => ({ ...item })) ?? [],
    goalArmed: draft?.goalArmed ?? false,
    mode: draft?.mode ?? "comment",
    workItems: copyIssueWorkItems(draft?.workItems ?? []),
    workPlanInput: draft?.workPlanInput ?? null,
    idempotencyKey: draft?.idempotencyKey ?? "",
    textRevision: draft?.textRevision ?? 0,
    submitting: pending.has(key)
  };
}

function beforeUnload(event: BeforeUnloadEvent) {
  if (!drafts.size) return;
  event.preventDefault();
  event.returnValue = "";
}

function updateUnloadGuard() {
  if (drafts.size && !unloadTarget && typeof window !== "undefined") {
    unloadTarget = window;
    unloadTarget.addEventListener("beforeunload", beforeUnload);
  } else if (!drafts.size && unloadTarget) {
    unloadTarget.removeEventListener("beforeunload", beforeUnload);
    unloadTarget = undefined;
  }
}

function notify(key: string) {
  for (const listener of listeners.get(key) ?? []) listener(readComposerDraft(key));
}

export function subscribeComposerDraft(key: string, listener: (draft: ComposerDraft) => void): () => void {
  const group = listeners.get(key) ?? new Set();
  group.add(listener);
  listeners.set(key, group);
  listener(readComposerDraft(key));
  return () => {
    group.delete(listener);
    if (!group.size) listeners.delete(key);
  };
}

export function canRetainDraftAttachments(key: string, attachments: PendingComposerAttachment[]): boolean {
  let characters = attachments.reduce((sum, item) => sum + item.dataURL.length, 0);
  for (const [otherKey, draft] of drafts) {
    if (otherKey !== key) characters += draft.attachments.reduce((sum, item) => sum + item.dataURL.length, 0);
  }
  return characters <= maximumRetainedDraftImageCharacters;
}

export function saveComposerDraft(key: string, input: ComposerDraftInput): ComposerDraft {
  const previous = readComposerDraft(key);
  const mentions = input.mentions?.map((item) => ({ ...item })) ?? [];
  const attachments = input.attachments?.map((item) => ({ ...item })) ?? [];
  const workItems = copyIssueWorkItems(input.workItems ?? []);
  const mode = input.mode ?? "comment";
  const workPlanInput = input.workPlanInput ?? null;
  if (!canRetainDraftAttachments(key, attachments)) throw new Error("draft_image_budget_exceeded");
  const textChanged = input.text !== previous.text || JSON.stringify(mentions) !== JSON.stringify(previous.mentions) ||
    mode !== previous.mode || JSON.stringify(workItems) !== JSON.stringify(previous.workItems) || workPlanInput !== previous.workPlanInput;
  const draft: ComposerDraft = {
    text: input.text, mentions, attachments, goalArmed: input.goalArmed ?? false,
    mode, workItems, workPlanInput, idempotencyKey: textChanged ? "" : previous.idempotencyKey,
    textRevision: textChanged ? nextRevision++ : previous.textRevision,
    submitting: pending.has(key)
  };
  if (draft.text || draft.attachments.length || draft.goalArmed || draft.mode === "work_items" || draft.workItems.length) drafts.set(key, draft);
  else drafts.delete(key);
  updateUnloadGuard();
  notify(key);
  return readComposerDraft(key);
}

/** Reuse the same key after a lost response, including after closing/reopening the task. */
export function composerIdempotencyKey(key: string): string {
  const draft = drafts.get(key);
  if (!draft) throw new Error("composer_draft_missing");
  draft.idempotencyKey ||= `issue-work-${v4()}`;
  return draft.idempotencyKey;
}

/** Lock synchronously, before a mutation's reactive isPending flag is updated. */
export function beginComposerSubmission(key: string): boolean {
  if (pending.has(key)) return false;
  pending.add(key);
  notify(key);
  return true;
}

export function finishComposerSubmission(key: string) {
  pending.delete(key);
  notify(key);
}

/** A late response may remove only its submitted text revision and attachment IDs. */
export function completeComposerSubmission(key: string, snapshot: ComposerDraft) {
  const current = readComposerDraft(key);
  const clearText = current.textRevision === snapshot.textRevision;
  const sentIDs = new Set(snapshot.attachments.map((item) => item.id));
  saveComposerDraft(key, {
    text: clearText ? "" : current.text,
    mentions: clearText ? [] : current.mentions,
    mode: clearText ? "comment" : current.mode,
    workItems: clearText ? [] : current.workItems,
    workPlanInput: clearText ? null : current.workPlanInput,
    attachments: current.attachments.filter((item) => !sentIDs.has(item.id)),
    goalArmed: snapshot.goalArmed ? false : current.goalArmed
  });
}

export function clearComposerDrafts() {
  const keys = new Set([...drafts.keys(), ...pending]);
  drafts.clear();
  pending.clear();
  updateUnloadGuard();
  for (const key of keys) notify(key);
}
