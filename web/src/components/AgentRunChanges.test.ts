import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import AgentRunChangesTestHost from "./AgentRunChangesTestHost.svelte";

afterEach(() => { cleanup(); queryClient.clear(); });

describe("AgentRunChanges source filter", () => {
  it("uses small shared buttons and retains mutually exclusive pressed/filter semantics", async () => {
    render(AgentRunChangesTestHost);
    const group = screen.getByRole("group", { name: t("runChanges.sourceFilter") });
    expect(group).toHaveClass("flex-wrap");
    const all = within(group).getByRole("button", { name: t("runChanges.sourceAll") });
    const provider = within(group).getByRole("button", { name: t("runChanges.agentReported") });
    const observation = within(group).getByRole("button", { name: t("runChanges.runObservation") });
    for (const button of [all, provider, observation]) {
      expect(button).toHaveClass("am-button");
      expect(button).toHaveAttribute("data-size", "sm");
      expect(button).toHaveAttribute("type", "button");
      expect(button).not.toHaveClass("min-h-8");
    }
    expect(all).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("note")).toHaveTextContent(t("runChanges.observationDisclaimer"));
    await fireEvent.click(provider);
    expect(provider).toHaveAttribute("aria-pressed", "true");
    expect(all).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByRole("note")).toBeNull();
    await fireEvent.click(observation);
    expect(observation).toHaveAttribute("aria-pressed", "true");
    expect(provider).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("note")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: t("runChanges.scope") }).closest(".am-field-group")).toBeNull();
  });
});
