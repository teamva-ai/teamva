import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, expect, it } from "vitest";
import RichText from "./RichText.svelte";

afterEach(cleanup);

it("renders GFM strikethrough, task lists and autolinks", () => {
  const { container } = render(RichText, {
    text: [
      "~~旧数据~~ 已更正",
      "",
      "- [x] 已完成项",
      "- [ ] 待办项",
      "",
      "参考 https://example.test/docs"
    ].join("\n"),
    enableLinks: true
  });
  expect(container.querySelector("s")?.textContent).toBe("旧数据");
  expect(container.querySelectorAll("li")).toHaveLength(2);
  expect(screen.getByText("已完成项")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "https://example.test/docs" })).toHaveAttribute(
    "href",
    "https://example.test/docs"
  );
});

it("renders nested and ordered lists", () => {
  const { container } = render(RichText, {
    text: ["1. 第一步", "2. 第二步", "   - 子项", "", "- 无序"].join("\n")
  });
  expect(container.querySelectorAll("ol > li")).toHaveLength(2);
  expect(container.querySelectorAll("ol ul > li")).toHaveLength(1);
  expect(container.querySelectorAll("ul > li")).toHaveLength(2);
});

it("keeps raw HTML as plain text", () => {
  const { container } = render(RichText, {
    text: "<img src=x onerror=alert(1)>\n<script>alert(2)</script>"
  });
  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector("script")).toBeNull();
  expect(container.textContent).toContain("<img src=x onerror=alert(1)>");
});
