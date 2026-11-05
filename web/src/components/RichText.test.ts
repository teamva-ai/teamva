import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import RichText from "./RichText.svelte";

afterEach(cleanup);

describe("shared RichText Markdown rendering", () => {
  it("preserves mixed GFM headers, emphasis, table alignment and task markers", () => {
    const { container } = render(RichText, { text: `## 报告 **标题**

段落含 *强调*、~~删除~~ 和 \`inline\`。

| 项目 | 结果 |
| --- | ---: |
| A | 通过 |

- [x] 完成
- [ ] 待办` });
    expect(container.querySelector("strong")).toHaveTextContent("标题");
    expect(container.querySelector("em")).toHaveTextContent("强调");
    expect(container.querySelector("s")).toHaveTextContent("删除");
    expect(container.querySelector("code")).toHaveTextContent("inline");
    expect(container.querySelectorAll("th")).toHaveLength(2);
    expect(container.querySelectorAll("th")[1]).toHaveClass("text-right");
    expect(container.querySelectorAll("tbody td")[1]).toHaveTextContent("通过");
    expect(container).toHaveTextContent("✓");
  });

  it("retains nested blockquotes and ordered/unordered list structure", () => {
    const { container } = render(RichText, { text: `> 引用
>
> 3. 有序内容
>    - 嵌套列表
>      \`long_nested_token\`` });
    const quote = container.querySelector("blockquote");
    expect(quote).toHaveTextContent("引用");
    expect(quote?.querySelector("ol")).toHaveAttribute("start", "3");
    expect(quote?.querySelector("ol ul li")).toHaveTextContent("嵌套列表");
    expect(quote?.querySelector("code")).toHaveTextContent("long_nested_token");
  });

  it("renders multi-block footnotes including a nested quote and list", () => {
    const { container } = render(RichText, { text: `引用[^note]。

[^note]: 脚注段落。

    > 脚注引用。

    - 脚注列表。` });
    expect(container.querySelector("sup")).toHaveTextContent("note");
    expect(container.querySelector("blockquote")).toHaveTextContent("脚注引用");
    expect(container.querySelector("ul li")).toHaveTextContent("脚注列表");
    expect(container).toHaveTextContent("脚注段落");
  });

  it("keeps every character and newline in long fenced code", () => {
    const token = "UNBROKEN_" + "0123456789abcdef".repeat(20);
    const content = `${token}\nSECOND_LINE\n`;
    const { container } = render(RichText, { text: `\`\`\`text\n${content}\`\`\`` });
    expect(container.querySelector("pre code")?.textContent).toBe(content.trimEnd());
  });

  it("does not activate Markdown links by default", () => {
    const { container } = render(RichText, { text: "[文档](https://example.test/docs)" });
    expect(container.querySelector("a")).toBeNull();
    expect(container).toHaveTextContent("文档");
  });

  it("enables only safe external or same-app links without changing their labels", () => {
    const { container } = render(RichText, { text: "[外部](https://example.test/docs) [内部](/issues) [禁止](javascript:alert(1))", enableLinks: true });
    const links = container.querySelectorAll("a");
    expect(links).toHaveLength(2);
    expect(links[0]).toHaveAttribute("target", "_blank");
    expect(links[0]).toHaveAttribute("rel", "noopener noreferrer");
    expect(links[1]).toHaveAttribute("href", "/issues");
    expect(links[1]).not.toHaveAttribute("target");
    expect(container).toHaveTextContent("禁止");
  });

  it("renders raw HTML as text rather than executable elements", () => {
    const { container } = render(RichText, { text: '<img src="x" onerror="alert(1)">\n\n<script>alert(1)</script>' });
    expect(container.querySelector("img,script")).toBeNull();
    expect(container).toHaveTextContent("<img");
    expect(container).toHaveTextContent("<script>");
  });

  it("preserves normal and compact/inverse typography classes while rerendering", async () => {
    const { container, rerender } = render(RichText, { text: "普通正文" });
    expect(container.firstElementChild).toHaveClass("gap-2", "text-[13px]", "leading-[1.7]", "text-text");
    await rerender({ text: "> 引用更新", compact: true, tone: "inverse" });
    expect(container.firstElementChild).toHaveClass("gap-1.5", "text-xs", "leading-[1.6]", "text-canvas");
    expect(container.querySelector("blockquote")).toHaveTextContent("引用更新");
    expect(container.querySelector("blockquote")).toHaveClass("border-current/40", "text-inherit");
  });

  it("uses adaptive ink-surface text throughout inverse Markdown, but keeps code blocks on their own dark surface", () => {
    const { container } = render(RichText, { tone: "inverse", enableLinks: true, text: `**粗体** 与 \`inline\` [链接](https://example.test) ![替代文字](https://example.test/image.png)

> 引用

- [x] 完成

| 表头 |
| --- |
| 内容 |

\`\`\`text
code block
\`\`\`` });
    for (const selector of ["strong", "p code", "a", "p span", "blockquote", "li > span", "th"]) {
      expect(container.querySelector(selector)).toHaveClass("text-inherit");
    }
    expect(container.querySelector("pre")).toHaveClass("bg-inverse-canvas", "text-white");
    expect(container.querySelector("th")).toHaveClass("bg-transparent");
    expect(container.querySelectorAll(".text-white")).toHaveLength(1);
  });
});
