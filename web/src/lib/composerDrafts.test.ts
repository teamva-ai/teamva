import { afterEach, describe, expect, it, vi } from "vitest";
import {
  beginComposerSubmission, canRetainDraftAttachments, clearComposerDrafts,
  completeComposerSubmission, composerDraftKey, finishComposerSubmission,
  maximumRetainedDraftImageCharacters, readComposerDraft, saveComposerDraft,
  subscribeComposerDraft, composerIdempotencyKey, type PendingComposerAttachment
} from "./composerDrafts";

afterEach(clearComposerDrafts);

describe("in-memory composer drafts", () => {
  it("isolates projects, issues, and instances and survives unsubscribe/reopen", () => {
    const key = composerDraftKey("project-a", "issue-a", "instance-a");
    const listener = vi.fn();
    const unsubscribe = subscribeComposerDraft(key, listener);
    saveComposerDraft(key, { text: "草稿" });
    unsubscribe();
    const calls = listener.mock.calls.length;
    saveComposerDraft(key, { text: "保留" });
    expect(listener).toHaveBeenCalledTimes(calls);
    expect(readComposerDraft(key).text).toBe("保留");
    expect(readComposerDraft(composerDraftKey("project-b", "issue-a", "instance-a")).text).toBe("");
    expect(readComposerDraft(composerDraftKey("project-a", "issue-b", "instance-a")).text).toBe("");
    expect(readComposerDraft(composerDraftKey("project-a", "issue-a", "instance-b")).text).toBe("");
  });

  it("locks synchronously across remounts and clears only the submitted revision/attachment IDs", () => {
    const key = composerDraftKey("project", "issue", "instance");
    const attachment = (id: string): PendingComposerAttachment => ({ id, filename: id, mimeType: "image/png", dataURL: "data:image/png;base64,AA==", size: 1 });
    saveComposerDraft(key, { text: "A", attachments: [attachment("a")] });
    const submitted = readComposerDraft(key);
    expect(beginComposerSubmission(key)).toBe(true);
    expect(beginComposerSubmission(key)).toBe(false);
    expect(readComposerDraft(key).submitting).toBe(true);
    saveComposerDraft(key, { text: "B", attachments: [attachment("a"), attachment("b")] });
    completeComposerSubmission(key, submitted);
    finishComposerSubmission(key);
    expect(readComposerDraft(key)).toMatchObject({ text: "B", submitting: false, attachments: [{ id: "b" }] });
    const next = readComposerDraft(key);
    saveComposerDraft(key, { text: "A", attachments: next.attachments });
    saveComposerDraft(key, { text: "B", attachments: next.attachments });
    completeComposerSubmission(key, next);
    expect(readComposerDraft(key).text).toBe("B");
  });

  it("bounds retained image data without evicting pending drafts and never persists text or base64", () => {
    const key = composerDraftKey("project", "issue");
    const attachment: PendingComposerAttachment = {
      id: "large", filename: "large.png", mimeType: "image/png", size: 1,
      dataURL: "x".repeat(maximumRetainedDraftImageCharacters)
    };
    const beforeLocal = window.localStorage.length;
    const beforeSession = window.sessionStorage.length;
    saveComposerDraft(key, { text: "session-only", attachments: [attachment] });
    expect(canRetainDraftAttachments(composerDraftKey("project", "other"), [{ ...attachment, dataURL: "x" }])).toBe(false);
    expect(readComposerDraft(key).attachments[0].id).toBe("large");
    expect(window.localStorage.length).toBe(beforeLocal);
    expect(window.sessionStorage.length).toBe(beforeSession);
    const event = new Event("beforeunload", { cancelable: true });
    expect(window.dispatchEvent(event)).toBe(false);
  });

  it("retains work mode without text, deep-copies dependencies and protects work-only edits from late success", () => {
    const key = composerDraftKey("p", "i");
    const work = { key: "a", agent_id: "agent", title: "First", instruction: "", after: ["b"] };
    saveComposerDraft(key, { text: "", mode: "work_items", workItems: [work] });
    const snapshot = readComposerDraft(key);
    snapshot.workItems[0].after.push("c");
    expect(readComposerDraft(key).workItems[0].after).toEqual(["b"]);
    const requestKey = composerIdempotencyKey(key);
    expect(composerIdempotencyKey(key)).toBe(requestKey);
    saveComposerDraft(key, { ...readComposerDraft(key), workItems: [{ ...work, title: "Newer" }] });
    expect(composerIdempotencyKey(key)).not.toBe(requestKey);
    completeComposerSubmission(key, snapshot);
    expect(readComposerDraft(key)).toMatchObject({ mode: "work_items", workItems: [{ title: "Newer" }] });
    const next = readComposerDraft(key);
    completeComposerSubmission(key, next);
    expect(readComposerDraft(key)).toMatchObject({ text: "", mode: "comment", workItems: [] });
    saveComposerDraft(key, { text: "legacy" });
    expect(readComposerDraft(key)).toMatchObject({ text: "legacy", mode: "comment", workItems: [] });
  });
});
