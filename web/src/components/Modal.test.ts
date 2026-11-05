import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import ModalTestHost from "./ModalTestHost.svelte";

afterEach(cleanup);

describe("Modal accessibility and Escape lifecycle", () => {
  it("labels the dialog, moves/restores focus, and only consumes Escape while open", async () => {
    render(ModalTestHost);
    const opener = screen.getByTestId("modal-opener");
    opener.focus();
    await fireEvent.click(opener);

    const dialog = screen.getByRole("dialog", { name: "Choose repository" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog.parentElement?.parentElement).toBe(document.body);
    // The panel has natural height: its body must start with an auto basis,
    // while remaining shrinkable for short-screen body-only scrolling.
    expect(dialog.querySelector(".am-modal__body")).toHaveClass("flex-initial");
    expect(dialog.querySelector(".am-modal__body")).not.toHaveClass("flex-1");
    await waitFor(() => expect(screen.getByLabelText("Repository")).toHaveFocus());
    const restoreFocus = vi.spyOn(opener, "focus");

    const openEscape = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    document.dispatchEvent(openEscape);
    expect(openEscape.defaultPrevented).toBe(true);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByTestId("modal-close-count")).toHaveTextContent("1");
    expect(opener).toHaveFocus();
    expect(restoreFocus).toHaveBeenCalledWith({ preventScroll: true });

    const closedEscape = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    document.dispatchEvent(closedEscape);
    expect(closedEscape.defaultPrevented).toBe(false);
    expect(screen.getByTestId("modal-close-count")).toHaveTextContent("1");
  });

  it("cycles only through enabled visible controls and keeps keyboard ownership on the top modal", async () => {
    render(ModalTestHost);
    const opener = screen.getByTestId("modal-opener");
    opener.focus();
    await fireEvent.click(opener);

    const outer = screen.getByRole("dialog", { name: "Choose repository" });
    const repository = screen.getByLabelText("Repository");
    await waitFor(() => expect(repository).toHaveFocus());
    expect(screen.getByLabelText("Disabled input")).toBeDisabled();

    const outerClose = outer.querySelector<HTMLButtonElement>(':scope > header button[aria-label="Close"]')!;
    const nestedOpener = screen.getByTestId("nested-modal-opener");
    nestedOpener.focus();
    const forward = new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
    document.dispatchEvent(forward);
    expect(forward.defaultPrevented).toBe(true);
    expect(outerClose).toHaveFocus();

    const backward = new KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true, cancelable: true });
    document.dispatchEvent(backward);
    expect(backward.defaultPrevented).toBe(true);
    expect(nestedOpener).toHaveFocus();

    await fireEvent.click(nestedOpener);
    const nested = screen.getByRole("dialog", { name: "Nested confirmation" });
    expect(nested.parentElement?.parentElement).toBe(document.body);
    expect(Number(nested.parentElement!.style.zIndex)).toBeGreaterThan(Number(outer.parentElement!.style.zIndex));
    await waitFor(() => expect(screen.getByLabelText("Nested choice")).toHaveFocus());
    expect(outer).toBeInTheDocument();

    const nestedLast = screen.getByTestId("nested-modal-last");
    nestedLast.focus();
    const nestedForward = new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
    document.dispatchEvent(nestedForward);
    expect(nestedForward.defaultPrevented).toBe(true);
    const nestedClose = nested.querySelector<HTMLButtonElement>(':scope > header button[aria-label="Close"]')!;
    expect(nestedClose).toHaveFocus();

    const nestedEscape = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    document.dispatchEvent(nestedEscape);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Nested confirmation" })).not.toBeInTheDocument());
    expect(screen.getByTestId("nested-modal-close-count")).toHaveTextContent("1");
    expect(screen.getByTestId("modal-close-count")).toHaveTextContent("0");
    expect(nestedOpener).toHaveFocus();

    const outerEscape = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    document.dispatchEvent(outerEscape);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Choose repository" })).not.toBeInTheDocument());
    expect(screen.getByTestId("modal-close-count")).toHaveTextContent("1");
    expect(opener).toHaveFocus();
  });

  it("removes portalled backdrops and restores inert when the entire owner is unmounted", async () => {
    const background = document.createElement("button");
    document.body.append(background);
    const view = render(ModalTestHost);
    try {
      await fireEvent.click(screen.getByTestId("modal-opener"));
      expect(background).toHaveAttribute("inert");
      await fireEvent.click(screen.getByTestId("nested-modal-opener"));
      expect(screen.getAllByRole("dialog", { hidden: true })).toHaveLength(2);
      view.unmount();
      expect(document.querySelector(".am-modal-backdrop")).toBeNull();
      expect(background).not.toHaveAttribute("inert");
    } finally {
      background.remove();
    }
  });
});
