<script lang="ts">
  import { onMount } from "svelte";
  import { t } from "../lib/i18n.svelte";
  import ErrorNotice from "./ErrorNotice.svelte";
  import type { Crepe as CrepeType } from "@milkdown/crepe";
  import "@milkdown/crepe/theme/common/style.css";
  import "@milkdown/crepe/theme/frame.css";

  // Initial value only. The parent remounts on an intentional document switch,
  // never on autosave/refetch; serializing is the only outgoing representation.
  let { initial, onChange }: { initial: string; onChange: (markdown: string) => void } = $props();
  let host: HTMLDivElement;
  let crepe: CrepeType | undefined;
  let ready = $state(false);
  let error = $state<unknown>(null);
  let source = "";
  let canonical = "";
  let edited = false;
  export function getMarkdown() {
    if (!crepe || !ready) return source;
    const value = crepe.getMarkdown();
    return !edited && value === canonical ? source : value;
  }
  onMount(() => {
    source = initial;
    let cancelled = false;
    const load = async () => {
      const [{ Crepe }, { editorViewOptionsCtx }, { uploadConfig }] = await Promise.all([
        import("@milkdown/crepe"),
        import("@milkdown/kit/core"),
        import("@milkdown/kit/plugin/upload")
      ]);
      if (cancelled) return;
      const editor = new Crepe({
        root: host,
        defaultValue: source,
        features: {
          [Crepe.Feature.AI]: false,
          [Crepe.Feature.ImageBlock]: false,
          [Crepe.Feature.Latex]: false,
          [Crepe.Feature.TopBar]: true
        },
        featureConfigs: {
          [Crepe.Feature.Placeholder]: { text: t("inspirations.placeholder") },
          [Crepe.Feature.TopBar]: {
            headingOptions: [
              { label: t("inspirations.paragraph"), level: null },
              ...[1, 2, 3, 4, 5, 6].map((level) => ({
                label: `${t("inspirations.heading")} ${level}`,
                level
              }))
            ]
          }
        }
      });
      crepe = editor;
      // ProseMirror can mount before all Crepe plugins finish initialization.
      // Keep it non-editable until the initial Markdown snapshot is captured,
      // otherwise early typing could be mistaken for the initial document.
      editor.setReadonly(true);
      editor.editor.config((ctx) => {
        // No blob: upload URLs that disappear after reload. Files/attachments
        // are outside this feature; stable Markdown image URLs still roundtrip.
        ctx.update(uploadConfig.key, (config) => ({ ...config, uploader: async () => [] }));
        ctx.update(editorViewOptionsCtx, (config) => ({
          ...config,
          attributes: {
            ...config.attributes,
            "aria-label": t("inspirations.body"),
            role: "textbox",
            "aria-multiline": "true"
          }
        }));
      });
      editor.on((listener) =>
        listener.markdownUpdated((_ctx, markdown) => {
          if (ready && markdown !== canonical) {
            canonical = markdown;
            edited = true;
            onChange(markdown);
          }
        })
      );
      await editor.create();
      if (cancelled) return;
      canonical = editor.getMarkdown();
      ready = true;
      editor.setReadonly(false);
    };
    const initialized = load().catch((e) => {
      if (!cancelled) error = e;
    });
    return () => {
      cancelled = true;
      ready = false;
      void initialized.then(() => crepe?.destroy()).catch(() => {});
    };
  });
  function guardLink(event: MouseEvent) {
    const anchor = (event.target as Element)?.closest?.("a[href]");
    if (!anchor) return;
    const href = anchor.getAttribute("href")?.trim() ?? "";
    if (!/^(https?:|mailto:|#|\/)/i.test(href)) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
  }
</script>

<div
  class="markdown-editor min-h-64"
  data-markdown-editor
  onclickcapture={guardLink}
  role="presentation"
>
  {#if error}<ErrorNotice {error} />{:else if !ready}<p class="p-4 text-sm text-muted">
      {t("common.loading")}
    </p>{/if}
  <div bind:this={host}></div>
</div>

<style>
  .markdown-editor :global(.milkdown) {
    --crepe-color-background: var(--color-surface);
    --crepe-color-on-background: var(--color-text);
    --crepe-color-surface: var(--color-surface-subtle);
    --crepe-color-surface-low: var(--color-surface-hover);
    --crepe-color-on-surface: var(--color-text);
    --crepe-color-on-surface-variant: var(--color-muted);
    --crepe-color-outline: var(--color-border);
    --crepe-color-primary: var(--color-accent-text);
    --crepe-color-hover: var(--color-surface-hover);
    --crepe-font-default: inherit;
    --crepe-font-title: inherit;
    --crepe-base-font-size: 15px;
    min-height: 260px;
  }
  .markdown-editor :global(.milkdown .ProseMirror) {
    padding: 24px 28px;
    min-height: 260px;
    overflow-wrap: anywhere;
  }
  .markdown-editor :global(.milkdown-top-bar) {
    position: sticky;
    top: 0;
    z-index: 5;
    flex-wrap: wrap;
    max-width: 100%;
    background: var(--color-surface);
  }
  .markdown-editor :global(.milkdown img) {
    max-width: 100%;
  }
  .markdown-editor :global(.milkdown .milkdown-code-block) {
    max-width: 100%;
  }
</style>
