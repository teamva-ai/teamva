import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, expect, it } from "vitest";
import RichText from "./RichText.svelte";

afterEach(cleanup);

it("renders GFM tables instead of raw pipe text", () => {
  render(RichText, {
    text: [
      "| 项目 | Alpha | Beta |",
      "| --- | ---: | :---: |",
      "| Stars | 51,453 | 10,777 |",
      "| 语言 | Go | TypeScript |"
    ].join("\n")
  });
  expect(screen.getByRole("columnheader", { name: "项目" })).toBeInTheDocument();
  expect(screen.getAllByRole("columnheader")).toHaveLength(3);
  const stars = screen.getByRole("cell", { name: "51,453" });
  expect(stars.className).toContain("text-right");
  expect(screen.getByRole("cell", { name: "语言" }).className).toContain("text-left");
  expect(screen.getByRole("cell", { name: "Go" }).className).toContain("text-right");
  expect(screen.getByRole("cell", { name: "10,777" }).className).toContain("text-center");
  expect(screen.queryByText(/\| --- \|/)).toBeNull();
});

it("keeps inline code and following paragraphs around a table", () => {
  render(RichText, {
    text: [
      "| 仓库 | 全名 |",
      "| --- | --- |",
      "| `alpha` | example-org/alpha |",
      "",
      "末尾段落"
    ].join("\n")
  });
  const cell = screen.getByRole("cell", { name: "alpha" });
  expect(cell.querySelector("code")).not.toBeNull();
  expect(screen.getByText("末尾段落")).toBeInTheDocument();
});

it("treats lone pipes as plain text, not a table", () => {
  render(RichText, { text: "a | b 只是普通文本" });
  expect(screen.queryByRole("table")).toBeNull();
  expect(screen.getByText("a | b 只是普通文本")).toBeInTheDocument();
});
