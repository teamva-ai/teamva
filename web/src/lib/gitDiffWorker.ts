import { WorkerPoolManager } from "@pierre/diffs/worker";
import ShikiWorkerURL from "@pierre/diffs/worker/worker.js?worker&url";

export const GIT_DIFF_THEMES = {
  dark: "github-dark",
  light: "github-light"
} as const;

let sharedPool: WorkerPoolManager | undefined;
let sharedReferences = 0;

export function createGitDiffWorkerPool(): WorkerPoolManager | undefined {
  if (typeof Worker === "undefined") return undefined;
  const pool = new WorkerPoolManager(
    {
      workerFactory: () => new Worker(ShikiWorkerURL, { type: "module" }),
      // Two workers bound memory use and satisfy the diff-prefetch concurrency budget.
      poolSize: 2,
      totalASTLRUCacheSize: 25
    },
    {
      theme: GIT_DIFF_THEMES,
      langs: [
        "text", "go", "javascript", "typescript", "python", "ruby", "rust", "java", "kotlin",
        "c", "cpp", "css", "scss", "html", "svg", "json", "yaml", "toml", "xml",
        "markdown", "shell", "sql"
      ],
      preferredHighlighter: "shiki-wasm",
      lineDiffType: "word-alt",
      maxLineDiffLength: 1_000,
      tokenizeMaxLineLength: 10_000
    }
  );
  void pool.initialize().catch(() => {
    // FileDiff has its own plain-text error path; failed workers are terminated
    // rather than left running after the page closes.
    pool.terminate();
  });
  return pool;
}

export interface GitDiffWorkerLease {
  pool?: WorkerPoolManager;
  release(): void;
}

/** One bounded Shiki/Pierre pool for every mounted diff surface. Individual
 * cards cancel their renderer tasks during cleanup; the last surface release
 * terminates the workers and their AST caches. */
export function acquireGitDiffWorkerPool(): GitDiffWorkerLease {
  if (!sharedPool) sharedPool = createGitDiffWorkerPool();
  if (sharedPool) sharedReferences += 1;
  const leased = sharedPool;
  let released = false;
  return {
    pool: leased,
    release() {
      if (released || !leased) return;
      released = true;
      sharedReferences = Math.max(0, sharedReferences - 1);
      if (sharedReferences === 0 && sharedPool === leased) {
        sharedPool.terminate();
        sharedPool = undefined;
      }
    }
  };
}
