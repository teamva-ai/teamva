<script module lang="ts">
  const modalStack: Array<{ token: symbol; layer: number }> = [];
  const inertOwners = new Map<HTMLElement, { count: number; originallyInert: boolean }>();

  function acquireInert(element: HTMLElement) {
    const owner = inertOwners.get(element);
    if (owner) owner.count += 1;
    else inertOwners.set(element, { count: 1, originallyInert: element.hasAttribute("inert") });
    element.setAttribute("inert", "");
  }

  function releaseInert(element: HTMLElement) {
    const owner = inertOwners.get(element);
    if (!owner || --owner.count > 0) return;
    inertOwners.delete(element);
    if (!owner.originallyInert) element.removeAttribute("inert");
  }

  function registerModal(token: symbol, layer: number): number {
    const effectiveLayer = Math.max(layer, (modalStack.at(-1)?.layer ?? layer - 1) + 1);
    modalStack.push({ token, layer: effectiveLayer });
    return effectiveLayer;
  }

  function unregisterModal(token: symbol): boolean {
    const wasTop = modalStack.at(-1)?.token === token;
    const index = modalStack.findIndex((entry) => entry.token === token);
    if (index >= 0) modalStack.splice(index, 1);
    return wasTop;
  }

  function isTopModal(token: symbol): boolean {
    return modalStack.at(-1)?.token === token;
  }
</script>

<script lang="ts">
  import Button from "./Button.svelte";
  import { t } from "../lib/i18n.svelte";
  import type { Snippet } from "svelte";
  import type { HTMLAttributes } from "svelte/elements";
  import { onMount, tick } from "svelte";

  let { title, description, onClose, children, footer, headerExtra, dismissible = true, closeDisabled = false, maxWidth = 640, layer = 50, centered = false, initialFocus, ...rest }: Omit<HTMLAttributes<HTMLDivElement>, "title" | "children"> & {
    title: string;
    description?: string;
    onClose: () => void;
    children: Snippet;
    footer?: Snippet;
    headerExtra?: Snippet;
    dismissible?: boolean;
    closeDisabled?: boolean;
    maxWidth?: number;
    layer?: number;
    centered?: boolean;
    initialFocus?: string;
  } = $props();

  const titleId = `modal-title-${Math.random().toString(36).slice(2, 8)}`;
  const descriptionId = `${titleId}-description`;
  let backdrop: HTMLDivElement;
  let dialog: HTMLDivElement;
  let effectiveLayer = $state(50);
  const modalToken = Symbol(titleId);

  const focusableSelector = [
    "a[href]", "area[href]", "button", "input", "select", "textarea",
    "iframe", "object", "embed", "audio[controls]", "video[controls]",
    "summary", "[contenteditable]", "[tabindex]"
  ].join(",");

  function isElementHidden(element: HTMLElement): boolean {
    if (element.matches('input[type="hidden"]')) return true;
    if (element.closest('[hidden], [aria-hidden="true"], [inert]')) return true;
    if (element.matches('[contenteditable="false"]')) return true;
    for (let current: HTMLElement | null = element; current; current = current.parentElement) {
      const style = getComputedStyle(current);
      if (style.display === "none" || style.visibility === "hidden" || style.visibility === "collapse") {
        return true;
      }
      if (current === dialog) break;
    }
    return false;
  }

  function focusableElements(): HTMLElement[] {
    if (!dialog) return [];
    return [...dialog.querySelectorAll<HTMLElement>(focusableSelector)].filter((element) =>
      !element.matches(":disabled") && !element.hasAttribute("disabled") &&
      element.tabIndex >= 0 && !isElementHidden(element)
    );
  }

  onMount(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    // Escape transformed/contained/scrolling page ancestors. A modal is always
    // positioned against the viewport, including when opened inside a drawer.
    document.body.append(backdrop);
    const inertSiblings: HTMLElement[] = [];
    for (let current: HTMLElement | null = backdrop; current?.parentElement; current = current.parentElement) {
      for (const sibling of current.parentElement.children) {
        if (sibling === current || !(sibling instanceof HTMLElement)) continue;
        acquireInert(sibling);
        inertSiblings.push(sibling);
      }
    }
    effectiveLayer = registerModal(modalToken, layer);
    const handleKey = (event: KeyboardEvent) => {
      if (!isTopModal(modalToken)) return;
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopImmediatePropagation();
        if (dismissible && !closeDisabled) onClose();
        return;
      }
      if (event.key === "Tab") {
        const focusables = focusableElements();
        if (focusables.length === 0) {
          event.preventDefault();
          dialog?.focus();
          return;
        }
        const first = focusables[0];
        const last = focusables[focusables.length - 1];
        const active = document.activeElement as HTMLElement | null;
        if (!dialog?.contains(active) || active === dialog || (active?.tabIndex ?? -1) < 0) {
          event.preventDefault();
          (event.shiftKey ? last : first).focus();
        } else if (event.shiftKey && active === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && active === last) {
          event.preventDefault();
          first.focus();
        }
      }
    };
    const handleFocus = (event: FocusEvent) => {
      if (isTopModal(modalToken) && event.target instanceof Node && !dialog.contains(event.target)) (focusableElements()[0] ?? dialog).focus({ preventScroll: true });
    };
    document.addEventListener("keydown", handleKey, true);
    document.addEventListener("focusin", handleFocus, true);
    void tick().then(() => {
      if (!isTopModal(modalToken)) return;
      const focusables = focusableElements();
      const preferred = (initialFocus ? dialog.querySelector<HTMLElement>(initialFocus) : undefined) ?? focusables.find((element) => element.hasAttribute("autofocus")) ??
        focusables.find((element) => element.matches('input:not([type="hidden"]), select, textarea')) ??
        focusables[0];
      (preferred ?? dialog)?.focus({ preventScroll: true });
    });
    return () => {
      document.removeEventListener("keydown", handleKey, true);
      document.removeEventListener("focusin", handleFocus, true);
      for (const sibling of inertSiblings) releaseInert(sibling);
      const wasTop = unregisterModal(modalToken);
      // Restoring keyboard ownership must not scroll the page (or the parent
      // modal) back to the opener after the user has moved the viewport.
      if (wasTop && previouslyFocused?.isConnected) previouslyFocused.focus({ preventScroll: true });
      backdrop.remove();
    };
  });

  function closeFromBackdrop() {
    if (isTopModal(modalToken) && dismissible && !closeDisabled) onClose();
  }
</script>

<div
  class="am-modal-backdrop fixed inset-0 grid grid-cols-[minmax(0,1fr)] grid-rows-[minmax(0,1fr)] place-items-center bg-backdrop p-4 max-sm:p-3"
  style:z-index={effectiveLayer}
  role="presentation"
  onmousedown={closeFromBackdrop}
  bind:this={backdrop}
>
  <div
    {...rest}
    class="am-modal flex max-h-full w-full min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-surface shadow-[0_16px_50px_var(--color-shadow-strong)]"
    style:max-width={`${maxWidth}px`}
    role="dialog"
    tabindex="-1"
    aria-modal="true"
    aria-labelledby={titleId}
    aria-describedby={description ? descriptionId : undefined}
    bind:this={dialog}
    onmousedown={(event) => event.stopPropagation()}
  >
    <header class="flex shrink-0 items-start justify-between gap-3 px-5 pb-3 pt-5 max-sm:px-4">
      <div class="min-w-0 flex-1" class:text-center={centered}>
        <h2 id={titleId} class="break-words text-lg font-semibold tracking-[-0.01em] [overflow-wrap:anywhere]">{title}</h2>
        {#if description}<p id={descriptionId} class="mt-[5px] break-words text-sm text-secondary [overflow-wrap:anywhere]">{description}</p>{/if}
        {@render headerExtra?.()}
      </div>
      {#if dismissible}<Button icon controlSize="sm" type="button" disabled={closeDisabled} aria-label={t("common.close")} onclick={onClose}>×</Button>{/if}
    </header>
    <!-- A natural-height panel must size its body from content, not a 0% flex
         basis whose resolution depends on the panel's own initial height. Keep
         shrinking enabled so a short viewport still scrolls only the body. -->
    <div class="am-modal__body min-h-0 min-w-0 flex-initial overflow-auto px-5 pb-5 max-sm:px-4 [overflow-wrap:anywhere]">{@render children()}</div>
    {#if footer}<footer class="min-w-0 shrink-0 border-t border-border bg-surface-subtle px-5 py-3 max-sm:px-4 [overflow-wrap:anywhere]">{@render footer()}</footer>{/if}
  </div>
</div>
