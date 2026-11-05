import { readFileSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import { parse } from "svelte/compiler";
import { controlTranslations } from "../locales/controls";

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => entry.isDirectory() ? sourceFiles(join(dir, entry.name)) : entry.name.endsWith(".svelte") ? [join(dir, entry.name)] : []);
}
describe("UI library boundaries", () => {
  it("requires shared tracks for ordinary side-by-side Field components", () => {
    const root = join(process.cwd(), "src");
    const violations: string[] = [];
    type LayoutNode = { type?: string; name?: string; fragment?: { nodes?: LayoutNode[] }; attributes?: Array<{ type?: string; name?: string; value?: unknown }> };
    for (const path of sourceFiles(root)) {
      const name = relative(root, path);
      if (name.startsWith("ui/") || name.startsWith("test/") || /TestHost\.svelte$/.test(name)) continue;
      const ast = parse(readFileSync(path, "utf8"), { modern: true });
      function visit(node: unknown) {
        if (!node || typeof node !== "object") return;
        if (Array.isArray(node)) { node.forEach(visit); return; }
        const value = node as LayoutNode;
        const classValue = value.attributes?.find((attribute) => attribute.type === "Attribute" && attribute.name === "class")?.value;
        const classes = Array.isArray(classValue) ? classValue.filter((part) => part?.type === "Text").map((part) => part.data).join(" ") : "";
        if (value.type === "RegularElement" && /\bgrid-cols-(?:2|3)\b/.test(classes) && (value.fragment?.nodes?.filter((child) => child.type === "Component" && child.name === "Field").length ?? 0) >= 2) {
          violations.push(`${name}: use FieldGroup instead of independent grid rows`);
        }
        for (const [key, item] of Object.entries(node)) if (!["instance", "module", "css", "attributes"].includes(key)) visit(item);
      }
      visit(ast.fragment);
    }
    expect(violations).toEqual([]);
  });

  it("requires business pages to reuse native form primitives from ui", () => {
    const root = join(process.cwd(), "src");
    const violations: string[] = [];
    for (const path of sourceFiles(root)) {
      const name = relative(root, path);
      if (name.startsWith("ui/") || name.startsWith("test/") || /TestHost\.svelte$/.test(name)) continue;
      const ast = parse(readFileSync(path, "utf8"), { modern: true });
      function visit(node: unknown) {
        if (!node || typeof node !== "object") return;
        if (Array.isArray(node)) { node.forEach(visit); return; }
        const value = node as Record<string, unknown>;
        if (value.type === "RegularElement" && ["input", "select", "textarea"].includes(String(value.name))) violations.push(`${name}: ${value.name}`);
        for (const [key, item] of Object.entries(value)) if (!["instance", "module", "css", "attributes"].includes(key)) visit(item);
      }
      visit(ast.fragment);
    }
    expect(violations).toEqual([]);
  });

  it("centralizes geometry and keeps icon/text clearance in the shared stylesheet", () => {
    const css = readFileSync(join(process.cwd(), "src/ui/controls.css"), "utf8");
    expect(css).toContain("--am-control-height: 36px");
    expect(css).toContain("--am-control-height: 32px");
    expect(css).toContain("--am-control-height: 40px");
    expect(css).toContain("right: 12px");
    expect(css).toContain("padding: 6px 38px 6px 12px");
    expect(css).toContain("pointer-events: none");
    expect(css).toContain("@media (pointer: coarse)");
    expect(css).toContain("var(--color-control-focus)");
    expect(css).toContain("background: var(--color-control)");
    expect(css).toContain("color: var(--color-control-text)");
    expect(css).not.toContain("var(--color-ink)");
  });

  it("has all shared control copy and interpolation parameters in every locale", () => {
    const source = controlTranslations["en-US"];
    for (const pack of Object.values(controlTranslations)) {
      expect(Object.keys(pack).sort()).toEqual(Object.keys(source).sort());
      for (const [key, value] of Object.entries(source)) {
        expect(pack[key]).toBeTruthy();
        expect(pack[key].match(/\{\{\w+\}\}/g)?.sort() ?? []).toEqual(value.match(/\{\{\w+\}\}/g)?.sort() ?? []);
      }
    }
  });
});
