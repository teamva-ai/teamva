import { readFile } from "node:fs/promises";
import { resolve, dirname } from "node:path";
import { describe, expect, it } from "vitest";

describe("global loading animation", () => {
  it("defines explicit WebKit-safe spinner endpoints and preserves progress motion", async () => {
    const css = await readFile(resolve(process.cwd(), "src/app.css"), "utf8");
    expect(css).toMatch(/@keyframes teamva-spin\s*\{[\s\S]*?from\s*\{\s*transform:\s*rotate\(0deg\)/);
    expect(css).toMatch(/@keyframes teamva-spin\s*\{[\s\S]*?to\s*\{\s*transform:\s*rotate\(360deg\)/);
    expect(css).toMatch(/\.animate-spin\s*\{[\s\S]*?animation:\s*teamva-spin 800ms linear infinite/);
    expect(css).toMatch(/prefers-reduced-motion:[\s\S]*?\.animate-spin\s*\{[\s\S]*?animation-iteration-count:\s*infinite !important/);
  });
});

describe("global keyboard focus", () => {
  it("keeps a theme ring for non-input keyboard targets and excludes every input-like target", async () => {
    const css = await readFile(resolve(process.cwd(), "src/app.css"), "utf8");
    expect(css).toContain(":focus-visible:not(:is(input, select, textarea, [contenteditable]");
    expect(css).toMatch(/outline:\s*2px solid var\(--color-accent-text\) !important;\s*outline-offset:\s*2px !important/);
    expect(css).toContain(".am-model-picker__trigger");
    expect(css).toContain(".cm-editor, .cm-content, .ProseMirror");
    expect(css).toMatch(/\.text-accent\s*\{\s*color: var\(--color-accent-text\)/);
  });

  it("applies a state-independent no-outline policy before all utility layers", async () => {
    const css = await readFile(resolve(process.cwd(), "src/app.css"), "utf8");
    const controls = await readFile(resolve(process.cwd(), "src/ui/controls.css"), "utf8");
    expect(css.indexOf("@layer input-policy, theme, base, components, utilities;")).toBeLessThan(css.indexOf('@import "tailwindcss"'));
    expect(controls).toMatch(/@layer input-policy\s*\{[\s\S]*?:is\(input, select, textarea, \[contenteditable\][\s\S]*?\)\s*\{\s*outline: none !important;\s*outline-width: 0 !important;\s*outline-offset: 0 !important;\s*box-shadow: none !important;/);
    expect(controls).not.toMatch(/outline:\s*(?:[1-9]\d*(?:px|rem)|auto)/);
    for (const selector of [".am-select", ".am-switch", ".am-model-picker", ".am-field__control", ".am-model-picker__button", '[role="radio"]', '[role="checkbox"]']) expect(controls).toContain(selector);
    expect(controls).toContain(".am-select:focus-within, .am-input:focus, .am-textarea:focus { border-color:");
  });

  it("overrides generated accent text utilities without changing decorative backgrounds", async () => {
    const { compile } = await import("tailwindcss");
    const css = await readFile(resolve(process.cwd(), "src/app.css"), "utf8");
    const compiler = await compile(css.replace('@import "tailwindcss";',
      "@layer theme, base, components, utilities; @tailwind utilities;"), {
        base: resolve(process.cwd(), "src"),
        loadStylesheet: async (id, base) => {
          const path = resolve(base, id);
          return { path, base: dirname(path), content: await readFile(path, "utf8") };
        }
      });
    const compiled = compiler.build(["text-accent", "!text-accent", "hover:text-accent", "bg-accent"]);
    // An @utility with the built-in text-accent name silently loses to the
    // generated color utility. Check the final cascade, not only source CSS.
    expect([...compiled.matchAll(/\.text-accent\s*\{([^}]+)\}/g)].at(-1)?.[1])
      .toContain("color: var(--color-accent-text)");
    expect([...compiled.matchAll(/\.\\!text-accent[^{}]*\{([^}]+)\}/g)].at(-1)?.[1])
      .toContain("color: var(--color-accent-text) !important");
    expect([...compiled.matchAll(/\.hover\\:text-accent:hover\s*\{([^}]+)\}/g)].at(-1)?.[1])
      .toContain("color: var(--color-accent-text)");
    expect(compiled).toMatch(/\.bg-accent\s*\{\s*background-color: var\(--color-accent\)/);
  });
});
