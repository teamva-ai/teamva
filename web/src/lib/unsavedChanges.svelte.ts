import { onMount } from "svelte";
import { t } from "./i18n.svelte";
import { confirmNavigation, location, registerNavigationGuard } from "./router.svelte";

/** Project pickers do not change the URL, but must obey the same editor guards. */
export function confirmProjectChange(projectId: string): boolean {
  return confirmNavigation(location.path + location.search, { kind: "project", projectId });
}

/** One owner-scoped guard for routes, history, project switching and reloads.
 * Read live state rather than capturing a draft's value at registration time.
 */
export function useUnsavedChanges(isDirty: () => boolean, options: {
  message?: () => string;
  onDiscard?: () => void;
} = {}) {
  let allowedChanges = 0;
  function confirm(): boolean {
    if (allowedChanges || !isDirty()) return true;
    if (!window.confirm(options.message?.() ?? t("common.unsavedChanges"))) return false;
    options.onDiscard?.();
    return true;
  }

  // Only bypass this editor after its own in-page confirmation, not unrelated
  // editors' guards. The synchronous scope cannot leak into a later action.
  function allowChanges<T>(action: () => T): T {
    allowedChanges++;
    try {
      return action();
    } finally {
      allowedChanges--;
    }
  }

  onMount(() => {
    let discardPending = false;
    const removeGuard = registerNavigationGuard(() => {
      discardPending = !allowedChanges && isDirty();
      return !discardPending || window.confirm(options.message?.() ?? t("common.unsavedChanges"));
    }, () => {
      if (discardPending) options.onDiscard?.();
      discardPending = false;
    });
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!isDirty()) return;
      event.preventDefault();
      // Browsers deliberately show their own localized text, not our message.
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => {
      removeGuard();
      window.removeEventListener("beforeunload", beforeUnload);
    };
  });
  return { confirm, allowChanges };
}
