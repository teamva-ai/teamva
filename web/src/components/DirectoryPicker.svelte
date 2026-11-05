<script lang="ts">
  import { Button, Checkbox, Input } from "../ui";
  import { onMount } from "svelte";
  import { ArrowUp, Check, Folder, FolderOpen, Plus, RefreshCw } from "lucide-svelte";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";

  // Interaction model adapted from OpenChamber (MIT); see THIRD_PARTY_NOTICES.md.
  let {
    computerId,
    selectedPath = "",
    onSelect
  }: {
    computerId: string;
    selectedPath?: string;
    onSelect: (path: string) => void;
  } = $props();

  type Entry = { name: string; path: string; is_dir: boolean };
  type BrowseResult = { items?: Entry[]; path?: string; home?: string };
  type CommandResult = { status: string; result?: unknown };
  class BrowseError extends Error {
    readonly kind: "unavailable" | "expired" | "timeout";
    constructor(kind: "unavailable" | "expired" | "timeout") {
      super(kind);
      this.kind = kind;
    }
  }

  let query = $state("~/");
  let home = $state("");
  let directory = $state("");
  let loadedQueryDirectory = $state("");
  let entries = $state<Entry[]>([]);
  let status = $state<"loading" | "ready" | "error">("loading");
  let error = $state("");
  let highlighted = $state(0);
  let showHidden = $state(false);
  let input = $state<HTMLInputElement>();
  let loadSerial = 0;
  let debounceTimer: number | undefined;
  let queryRevision = 0;
  let disposed = false;
  let loadController: AbortController | undefined;
  let homeController: AbortController | undefined;
  let homeRequest: Promise<BrowseResult> | undefined;
  let lastRequestedDirectory = "";
  let lastRequestUpdatesQuery = false;
  let focusFrame: number | undefined;

  const normalize = (value: string) => value.replace(/\\/g, "/");
  const trimSlash = (value: string) => value.length > 1 ? value.replace(/\/+$/, "") : value;
  const directoryPart = (value: string) => {
    if (value.endsWith("/")) return value;
    const index = value.lastIndexOf("/");
    return index < 0 ? value : value.slice(0, index + 1);
  };
  const leafPart = (value: string) => value.endsWith("/") ? "" : value.slice(value.lastIndexOf("/") + 1);
  const toAbsolute = (value: string) => {
    const clean = normalize(value.trim());
    if (clean === "~") return home;
    if (clean.startsWith("~/")) return home ? home + clean.slice(1) : "";
    return clean;
  };
  const toDisplay = (value: string) => {
    const clean = normalize(value);
    if (home && (clean === home || clean.startsWith(home + "/"))) return "~" + clean.slice(home.length);
    return clean;
  };

  const filteredEntries = $derived.by(() => {
    if (!hasCurrentDirectory) return [];
    const leaf = leafPart(query).toLocaleLowerCase();
    return entries.filter((entry) => entry.name.toLocaleLowerCase().startsWith(leaf));
  });
  const hasCurrentDirectory = $derived(status === "ready" && Boolean(directory) &&
    trimSlash(toAbsolute(directoryPart(query))) === loadedQueryDirectory);
  const canGoUp = $derived(hasCurrentDirectory && Boolean(home && trimSlash(directory) !== trimSlash(home)));
  const exactEntry = $derived(
    leafPart(query) ? filteredEntries.find((entry) => entry.name === leafPart(query)) : undefined
  );
  const currentTarget = $derived(
    hasCurrentDirectory ? exactEntry?.path ?? (query.endsWith("/") ? directory : "") : ""
  );
  const hasNativePicker = $derived(
    typeof window !== "undefined" && Boolean((window as unknown as {
      go?: { main?: { desktopApp?: { ChooseDirectory?: unknown } } };
    }).go?.main?.desktopApp?.ChooseDirectory)
  );

  function pause(signal: AbortSignal) {
    signal.throwIfAborted();
    return new Promise<void>((resolve, reject) => {
      const abort = () => {
        window.clearTimeout(timer);
        reject(signal.reason);
      };
      const timer = window.setTimeout(() => {
        signal.removeEventListener("abort", abort);
        resolve();
      }, 250);
      signal.addEventListener("abort", abort, { once: true });
    });
  }

  async function poll(commandId: string, signal: AbortSignal): Promise<BrowseResult> {
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      signal.throwIfAborted();
      const result = await api<CommandResult>(`/computers/${computerId}/directory-list/${commandId}`, { signal });
      signal.throwIfAborted();
      if (result.status === "completed" && result.result) return result.result as BrowseResult;
      if (result.status === "expired") throw new BrowseError("expired");
      if (result.status === "failed") throw new BrowseError("unavailable");
      await pause(signal);
    }
    throw new BrowseError("timeout");
  }

  async function browse(path: string, signal: AbortSignal): Promise<BrowseResult> {
    signal.throwIfAborted();
    const command = await api<{ command_id: string }>(`/computers/${computerId}/directory-list`, {
      body: { path: trimSlash(path), show_hidden: showHidden }, signal
    });
    signal.throwIfAborted();
    return poll(command.command_id, signal);
  }

  // Home discovery may finish after the user types. Share only its home value,
  // never its entries/query, with a newer request (including unresolved ~/ paths).
  function discoverHome(): Promise<BrowseResult> {
    if (!homeRequest) {
      const controller = new AbortController();
      homeController = controller;
      homeRequest = browse("", controller.signal).then((result) => {
        if (!disposed && !controller.signal.aborted && !home) home = normalize(result.home ?? result.path ?? "");
        return result;
      }).finally(() => {
        homeRequest = undefined;
        homeController = undefined;
      });
    }
    return homeRequest;
  }

  function invalidateLoad() {
    ++loadSerial;
    if (debounceTimer !== undefined) window.clearTimeout(debounceTimer);
    debounceTimer = undefined;
    if (focusFrame !== undefined) cancelAnimationFrame(focusFrame);
    focusFrame = undefined;
    loadController?.abort();
    loadController = undefined;
  }

  async function load(path: string, updateQuery: boolean) {
    invalidateLoad();
    const serial = loadSerial;
    const revision = queryRevision;
    const controller = new AbortController();
    loadController = controller;
    lastRequestedDirectory = path;
    lastRequestUpdatesQuery = updateQuery;
    status = "loading";
    error = "";
    entries = [];
    try {
      let result: BrowseResult;
      if (!home && (!path || path === "~" || path.startsWith("~/"))) {
        const initial = await discoverHome();
        if (disposed || serial !== loadSerial) return;
        if (!home) throw new BrowseError("unavailable");
        result = !path ? initial : await browse(toAbsolute(path), controller.signal);
      } else {
        result = await browse(toAbsolute(path) || home, controller.signal);
      }
      if (disposed || serial !== loadSerial || controller.signal.aborted) return;
      home = normalize(result.home ?? home);
      directory = normalize(result.path ?? toAbsolute(path));
      entries = (result.items ?? []).filter((entry) => entry.is_dir && entry.path !== directory);
      highlighted = 0;
      if (updateQuery && revision === queryRevision) query = `${toDisplay(directory).replace(/\/+$/, "")}/`;
      // Keep the request's input spelling (e.g. ~/a/../b/) associated with the
      // canonical result, rather than rejecting valid normalized HOME paths.
      loadedQueryDirectory = trimSlash(toAbsolute(directoryPart(query)));
      status = "ready";
      if (updateQuery) focusFrame = requestAnimationFrame(() => {
        if (disposed || serial !== loadSerial || revision !== queryRevision) return;
        input?.focus();
        if (input) input.setSelectionRange(input.value.length, input.value.length);
      });
    } catch (reason) {
      if (disposed || serial !== loadSerial || controller.signal.aborted) return;
      entries = [];
      status = "error";
      // Backend and transport errors can contain private paths. Only local,
      // explicitly classified messages are safe to render here.
      error = reason instanceof BrowseError && reason.kind === "expired" ? t("directoryPicker.expired")
        : reason instanceof BrowseError && reason.kind === "timeout" ? t("resources.pickerTimeout")
        : t("directoryPicker.unavailable");
    } finally {
      if (loadController === controller) loadController = undefined;
    }
  }

  function navigate(path: string) {
    void load(path, true);
  }

  function goUp() {
    if (!canGoUp) return;
    const parts = trimSlash(directory).split("/");
    parts.pop();
    navigate(parts.join("/") || "/");
  }

  function select(path: string) {
    invalidateLoad();
    ++queryRevision;
    if (status !== "ready") {
      // A native selection can finish while a typed-path request is pending.
      // That request is cancelled, so do not leave a permanent loading state.
      status = "ready";
      error = "";
      directory = "";
      entries = [];
      loadedQueryDirectory = "";
    }
    const normalized = trimSlash(normalize(path));
    query = toDisplay(normalized);
    onSelect(normalized);
  }

  function handleInput(event: Event) {
    ++queryRevision;
    query = normalize((event.currentTarget as HTMLInputElement).value);
    // Invalidate now, not when the debounce fires: old results must not become
    // selectable or overwrite the input during this 140 ms window.
    invalidateLoad();
    const requestedDirectory = directoryPart(query);
    if (hasCurrentDirectory) return;
    entries = [];
    status = "loading";
    error = "";
    debounceTimer = window.setTimeout(() => void load(requestedDirectory, false), 140);
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "ArrowDown" && filteredEntries.length) {
      event.preventDefault();
      highlighted = Math.min(filteredEntries.length - 1, highlighted + 1);
      return;
    }
    if (event.key === "ArrowUp" && filteredEntries.length) {
      event.preventDefault();
      highlighted = Math.max(0, highlighted - 1);
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      if ((event.metaKey || event.ctrlKey) && currentTarget) {
        select(currentTarget);
        return;
      }
      const entry = filteredEntries[highlighted];
      if (entry) navigate(entry.path);
    }
  }

  async function chooseNativeDirectory() {
    const binding = (window as unknown as {
      go?: { main?: { desktopApp?: { ChooseDirectory?: () => Promise<string> } } };
    }).go?.main?.desktopApp?.ChooseDirectory;
    if (!binding) return;
    const path = await binding();
    if (path && !disposed) select(path);
  }

  function toggleHidden() {
    showHidden = !showHidden;
    void load(directoryPart(query), false);
  }

  onMount(() => {
    void load("", true);
    return () => {
      disposed = true;
      invalidateLoad();
      homeController?.abort();
    };
  });
</script>

<div class="grid gap-2.5">
  <div class="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-1.5 rounded-md border border-border bg-surface-subtle pr-1.5">
    <div class="relative min-w-0">
      <FolderOpen class="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted" />
      <Input
        bind:ref={input}
        class="border-0 bg-transparent pl-10 font-mono text-xs shadow-none focus:ring-0"
        value={query}
        aria-label={t("resources.pathLabel")}
        spellcheck="false"
        autocomplete="off"
        oninput={handleInput}
        onkeydown={handleKeydown}
      />
    </div>
    <Button
      controlSize="sm"
      type="button"
      disabled={!currentTarget}
      onclick={() => currentTarget && select(currentTarget)}
    ><Check size={12} />{t("resources.selectCurrent")}</Button>
  </div>

  <div class="overflow-hidden rounded-md border border-border bg-surface">
    <div class="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-surface-subtle px-2.5 py-2">
      <Button variant="ghost" controlSize="sm" type="button" disabled={!canGoUp} onclick={goUp}>
        <ArrowUp size={12} />{t("resources.parentDirectory")}
      </Button>
      <label class="inline-flex cursor-pointer items-center gap-1.5 text-2xs text-muted">
        <Checkbox inheritField={false} type="checkbox" checked={showHidden} onchange={toggleHidden} />
        {t("resources.showHidden")}
      </label>
    </div>
    <div class="grid max-h-[270px] min-h-[190px] content-start overflow-y-auto p-1.5">
      {#if status === "loading"}
        <div class="grid min-h-[170px] place-items-center text-xs text-muted"><span class="inline-flex items-center gap-2"><RefreshCw size={13} class="animate-spin" />{t("common.loadingBlock")}</span></div>
      {:else if status === "error"}
        <div class="grid min-h-[170px] place-items-center gap-2 p-4 text-center text-xs text-danger">
          <span>{error}</span>
          <Button type="button" onclick={() => void load(lastRequestedDirectory, lastRequestUpdatesQuery)}>{t("common.retry")}</Button>
        </div>
      {:else if !filteredEntries.length}
        <div class="grid min-h-[170px] place-items-center text-xs text-muted">{t("resources.pickerEmpty")}</div>
      {:else}
        {#each filteredEntries as entry, index (entry.path)}
          <div role="group" class="grid grid-cols-[minmax(0,1fr)_32px] items-center rounded-sm {index === highlighted ? 'bg-accent-subtle' : 'hover:bg-surface-hover'}" onmouseenter={() => (highlighted = index)}>
            <button class="flex min-h-9 min-w-0 cursor-pointer items-center gap-2 border-0 bg-transparent px-2.5 text-left text-xs text-text" type="button" onclick={() => navigate(entry.path)}>
              <Folder size={14} class="shrink-0 text-muted" /><span class="truncate">{entry.name}</span>
            </button>
            <button class="grid size-7 cursor-pointer place-items-center rounded-full border-0 bg-transparent text-muted hover:bg-surface hover:text-text" type="button" title={t("resources.quickSelect")} onclick={() => select(entry.path)}><Plus size={13} /></button>
          </div>
        {/each}
      {/if}
    </div>
  </div>

  <div class="flex flex-wrap items-center justify-between gap-2 text-2xs text-muted">
    <span>{t("directoryPicker.scopeHint")}</span>
    <span>{t("resources.keyboardHint")}</span>
    {#if hasNativePicker}
      <Button controlSize="sm" type="button" onclick={chooseNativeDirectory}>
        <FolderOpen size={13} />{t("resources.chooseInFinder")}
      </Button>
    {/if}
  </div>

  {#if selectedPath}
    <div class="flex min-w-0 items-center gap-2 rounded-sm bg-success-subtle px-3 py-2 text-xs text-success">
      <Check size={13} class="shrink-0" /><code class="min-w-0 truncate" title={selectedPath}>{selectedPath}</code>
    </div>
  {/if}
</div>
