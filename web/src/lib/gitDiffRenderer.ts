import {
  VirtualizedFileDiff,
  type FileDiffMetadata,
  type Virtualizer
} from "@pierre/diffs";
import type { WorkerPoolManager } from "@pierre/diffs/worker";
import { GIT_DIFF_THEMES } from "./gitDiffWorker";
import type { GitDiffLayout } from "./gitChanges";

export interface ImperativeGitDiffOptions {
  host: HTMLElement;
  metadata: FileDiffMetadata;
  layout: GitDiffLayout;
  themeType: "light" | "dark";
  virtualizer: Virtualizer;
  workerPool?: WorkerPoolManager;
}

/** Shared imperative Pierre/Shiki mount used by both live project Changes and
 * bounded Run history. Callers own the Virtualizer/WorkerPool lifetime. */
export function mountGitFileDiff(options: ImperativeGitDiffOptions): () => void {
  options.host.replaceChildren();
  const instance = new VirtualizedFileDiff(
    {
      theme: GIT_DIFF_THEMES,
      themeType: options.themeType,
      diffStyle: options.layout,
      overflow: "scroll",
      disableFileHeader: true,
      disableLineNumbers: false,
      disableBackground: false,
      diffIndicators: "bars",
      hunkSeparators: "line-info",
      collapsedContextThreshold: 1,
      expansionLineCount: 20,
      preferredHighlighter: "shiki-wasm",
      tokenizeMaxLength: 500 * 1024,
      tokenizeMaxLineLength: 10_000,
      lineDiffType: "word-alt",
      maxLineDiffLength: 1_000,
      unsafeCSS: `
        :host, [data-diffs] { --diffs-font-family: var(--font-mono); }
        pre { font-size: 12px !important; line-height: 20px !important; }
      `
    },
    options.virtualizer,
    { lineHeight: 20, diffHeaderHeight: 0, hunkLineCount: 80, spacing: 8 },
    options.workerPool
  );
  instance.render({ fileDiff: options.metadata, containerWrapper: options.host, forceRender: true });
  return () => {
    // VirtualizedFileDiff.cleanUp detaches this instance from queued/active
    // shared-pool work before its DOM is removed.
    instance.cleanUp();
    options.host.replaceChildren();
  };
}
