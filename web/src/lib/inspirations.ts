import type { Versioned } from "./types";
import { APIError } from "./api";
export interface Inspiration extends Versioned {
  project_id: string;
  title: string;
  markdown: string;
  completed: boolean;
  completed_at: string | null;
}
export interface InspirationCard extends Omit<Inspiration, "markdown"> {
  preview: string;
}
export interface InspirationGroup {
  project_id: string;
  project_name: string;
  project_deleted: boolean;
  total: number;
  completed: number;
}
export interface InspirationPage {
  items: InspirationCard[];
  total: number;
}
export function noteColor(id: string) {
  let hash = 0;
  for (const c of id) hash = (hash * 31 + c.charCodeAt(0)) | 0;
  return Math.abs(hash) % 4;
}
export function inspirationPreview(markdown: string) {
  return markdown
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^\s{0,3}(?:#{1,6}|>|[-+*]|\d+\.)\s+/gm, "")
    .replace(/^\[ \]\s/gm, "☐ ")
    .replace(/^\[x\]\s/gim, "☑ ")
    .replace(/(?:\*\*|__|~~|`{1,3})/g, "");
}

export type InspirationDraft = Pick<Inspiration, "title" | "markdown" | "project_id" | "completed"> & {
  /** Input intent survives typing back to the original/default title. */
  titleRevision?: number;
};
export type InspirationPatch = Omit<InspirationDraft, "title" | "titleRevision"> & { title?: string };
export const noteDraft = (note: InspirationDraft): InspirationDraft => ({
  title: note.title.trim(),
  markdown: note.markdown,
  project_id: note.project_id,
  completed: note.completed,
  ...(note.titleRevision === undefined ? {} : { titleRevision: note.titleRevision })
});

function sameBody(left: Inspiration, right: Inspiration) {
  return left.id === right.id && left.markdown === right.markdown && left.project_id === right.project_id &&
    left.completed === right.completed && left.completed_at === right.completed_at;
}

// One serial save queue per document. Responses cannot clear later typing,
// target another note, or race two updates against the same version.
export class InspirationSaveQueue {
  private baseline: Inspiration;
  private titleRevision = 0;
  private pending?: Promise<void>;
  private stopped = false;
  constructor(
    private readonly noteID: string,
    initial: Inspiration,
    private readonly write: (
      id: string,
      version: number,
      draft: InspirationPatch
    ) => Promise<Inspiration>,
    private readonly onSaved: (note: Inspiration, submitted: InspirationDraft) => void,
    private readonly reload?: (id: string) => Promise<Inspiration>
  ) {
    this.baseline = initial;
  }
  private titleDirty(draft: InspirationDraft) {
    return draft.titleRevision === undefined ? draft.title.trim() !== this.baseline.title : draft.titleRevision > this.titleRevision;
  }
  dirty(draft: InspirationDraft) {
    return this.titleDirty(draft) || draft.markdown !== this.baseline.markdown ||
      draft.project_id !== this.baseline.project_id || draft.completed !== this.baseline.completed;
  }
  /** Never adopt a concurrent body/project/completion edit as our baseline. */
  acceptServer(note: Inspiration) {
    if (this.stopped || note.id !== this.noteID || note.version <= this.baseline.version || !sameBody(this.baseline, note)) return false;
    const previous = { ...noteDraft(this.baseline), titleRevision: this.titleRevision };
    this.baseline = note;
    this.onSaved(note, previous);
    return true;
  }
  stop() {
    this.stopped = true;
  }
  flush(read: () => InspirationDraft): Promise<void> {
    if (this.pending) return this.pending;
    this.pending = this.drain(read).finally(() => {
      this.pending = undefined;
    });
    return this.pending;
  }
  private async drain(read: () => InspirationDraft) {
    while (!this.stopped) {
      const draft = noteDraft(read());
      if (!this.dirty(draft)) return;
      const baseline = this.baseline;
      const patch: InspirationPatch = { markdown: draft.markdown, project_id: draft.project_id, completed: draft.completed,
        ...(this.titleDirty(draft) ? { title: draft.title } : {}) };
      let saved: Inspiration;
      try {
        saved = await this.write(this.noteID, baseline.version, patch);
      } catch (error) {
        if (this.stopped || !(error instanceof APIError) || error.status !== 409 || !this.reload) throw error;
        const latest = await this.reload(this.noteID).catch(() => null);
        if (!latest || latest.version <= baseline.version || !sameBody(baseline, latest)) throw error;
        this.acceptServer(latest);
        if (this.stopped) return;
        // One narrow title-only rebase. A second conflict remains visible.
        saved = await this.write(this.noteID, latest.version, patch);
      }
      const latest = this.baseline.version > saved.version && sameBody(saved, this.baseline) ? this.baseline : saved;
      this.baseline = latest;
      this.titleRevision = draft.titleRevision ?? this.titleRevision;
      if (!this.stopped) this.onSaved(latest, draft);
    }
  }
}
