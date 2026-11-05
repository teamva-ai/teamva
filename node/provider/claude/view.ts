import type { ProviderView } from "../view";

// Claude's native protocol is decoded and redacted in the adapter. The shared
// semantic-event renderer owns text, reasoning, tool, usage and compaction UI;
// no native/raw payload or credential metadata is decoded in the browser.
export default {
  materialize() { return false; },
  projection() { return () => undefined; },
} satisfies ProviderView;
