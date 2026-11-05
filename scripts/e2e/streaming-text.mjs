// Compare displayed prose, not Markdown list markers rendered by CSS. Exact
// native-delta/snapshot and persisted-response checks remain separate.
export const normalizeDisplayText = (value) => String(value ?? "")
  .replace(/^\s*(?:[-+*]|\d+[.)])\s+/gm, "")
  .replace(/\s+/g, "")
  .replace(/[*`#]/g, "");

export function toolOutput(row) {
  if (!row || row.kind !== "tool") return "";
  try { const value = JSON.parse(row.detail); return JSON.stringify(value.output ?? value.error ?? ""); }
  catch { return row.detail.match(/(?:^|\n\n)返回\n([\s\S]*?)(?=\n\n|$)/)?.[1] ?? ""; }
}
export const firstPhasedTool = (rows) => rows.find((r) => r.kind === "tool" && r.text.includes("STREAM_TOOL_A") && r.text.includes("STREAM_TOOL_B"));
