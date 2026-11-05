<script lang="ts">
  import { fromMarkdown } from "mdast-util-from-markdown";
  import { gfmFromMarkdown } from "mdast-util-gfm";
  import { gfm } from "micromark-extension-gfm";
  import { navigate } from "../lib/router.svelte";

  // 解析交给 micromark（CommonMark + GFM），渲染仍是逐节点生成 Svelte 元素，
  // 不使用 {@html}，因此任何正文都无法注入脚本或原始 HTML。
  interface MdNode {
    type: string;
    value?: string;
    children?: MdNode[];
    depth?: number;
    ordered?: boolean;
    start?: number | null;
    checked?: boolean | null;
    spread?: boolean;
    url?: string;
    alt?: string | null;
    align?: Array<"left" | "right" | "center" | null>;
    lang?: string | null;
    label?: string;
    identifier?: string;
  }

  let {
    text,
    compact = false,
    tone = "default",
    enableLinks = false
  }: {
    text: string;
    compact?: boolean;
    /** User bubbles stay dark in both modes; their content follows one foreground. */
    tone?: "default" | "inverse";
    enableLinks?: boolean;
  } = $props();

  const tree = $derived(
    fromMarkdown(text ?? "", {
      extensions: [gfm()],
      mdastExtensions: [gfmFromMarkdown()]
    }) as unknown as MdNode
  );

  function safeHref(href: string): boolean {
    return (
      !/[\u0000-\u0020\u007f\\]/.test(href) &&
      (/^https?:\/\//i.test(href) || (href.startsWith("/") && !href.startsWith("//")))
    );
  }

  function alignClass(align?: "left" | "right" | "center" | null): string {
    if (align === "center") return "text-center";
    if (align === "right") return "text-right";
    return "text-left";
  }

  function followLink(event: MouseEvent, href?: string) {
    if (
      href?.startsWith("/") &&
      !event.metaKey &&
      !event.ctrlKey &&
      !event.shiftKey &&
      !event.altKey
    ) {
      event.preventDefault();
      navigate(href);
    }
  }
</script>

{#snippet inlineNodes(nodes: MdNode[])}
  {#each nodes as node, index (index)}
    {#if node.type === "text"}{node.value ?? ""}
    {:else if node.type === "strong"}
      <strong class="font-semibold {tone === 'inverse' ? 'text-inherit' : 'text-text'}"
        >{@render inlineNodes(node.children ?? [])}</strong
      >
    {:else if node.type === "emphasis"}
      <em class="italic">{@render inlineNodes(node.children ?? [])}</em>
    {:else if node.type === "delete"}
      <s class="line-through opacity-80">{@render inlineNodes(node.children ?? [])}</s>
    {:else if node.type === "inlineCode"}
      <code
        class="rounded px-1 py-0.5 font-mono text-[0.92em] [overflow-wrap:anywhere] {tone ===
        'inverse'
          ? 'bg-current/15 text-inherit'
          : 'bg-surface-subtle text-code'}">{node.value ?? ""}</code
      >
    {:else if node.type === "link" || node.type === "linkReference"}
      {#if node.url && enableLinks && safeHref(node.url)}
        <a
          href={node.url}
          class="{tone === 'inverse' ? 'text-inherit' : 'text-accent'} underline underline-offset-2"
          target={node.url.startsWith("/") ? undefined : "_blank"}
          rel="noopener noreferrer"
          onclick={(event) => followLink(event, node.url)}
          >{@render inlineNodes(node.children ?? [])}</a
        >
      {:else}{@render inlineNodes(node.children ?? [])}{/if}
    {:else if node.type === "break"}
      <br />
    {:else if node.type === "image" || node.type === "imageReference"}
      <span class={tone === "inverse" ? "text-inherit" : "text-muted"}>{node.alt ?? node.url ?? ""}</span>
    {:else if node.type === "footnoteReference"}
      <sup class="text-2xs {tone === 'inverse' ? 'text-inherit' : 'text-accent'}">[{node.label ?? node.identifier ?? "?"}]</sup>
    {:else if node.type === "html"}
      {node.value ?? ""}
    {:else if node.children}
      {@render inlineNodes(node.children)}
    {:else if node.value}
      {node.value}
    {/if}
  {/each}
{/snippet}

{#snippet listItems(item: MdNode)}
  <li class="min-w-0">
    {#if item.checked === true || item.checked === false}
      <span
        class="mr-1.5 inline-grid size-3.5 translate-y-[1px] place-items-center rounded-sm border text-[9px] leading-none {tone ===
        'inverse'
          ? 'border-current/50 text-inherit'
          : 'border-border-strong text-text'}"
        aria-hidden="true">{item.checked ? "✓" : ""}</span
      >
    {/if}
    {#each item.children ?? [] as child, index (index)}
      {#if child.type === "paragraph" && index === 0 && !item.spread}
        <span>{@render inlineNodes(child.children ?? [])}</span>
      {:else if child.type === "paragraph"}
        <p class="m-0 whitespace-pre-wrap">{@render inlineNodes(child.children ?? [])}</p>
      {:else}
        {@render blockNodes([child])}
      {/if}
    {/each}
  </li>
{/snippet}

{#snippet blockNodes(nodes: MdNode[])}
  {#each nodes as node, index (index)}
    {#if node.type === "heading"}
      <div
        class="font-semibold tracking-[-0.01em] {(node.depth ?? 6) <= 2
          ? 'mt-1 text-sm'
          : 'text-[13px]'}"
      >
        {@render inlineNodes(node.children ?? [])}
      </div>
    {:else if node.type === "paragraph"}
      <p class="m-0 whitespace-pre-wrap">{@render inlineNodes(node.children ?? [])}</p>
    {:else if node.type === "code"}
      <pre
        class="m-0 min-w-0 max-w-full overflow-x-auto whitespace-pre-wrap [overflow-wrap:anywhere] rounded-lg border border-border/70 bg-inverse-canvas px-3 py-2.5 font-mono text-[11px] leading-[1.6] text-white"><code
          >{node.value ?? ""}</code
        ></pre>
    {:else if node.type === "list"}
      {#if node.ordered}
        <ol class="m-0 grid min-w-0 max-w-full grid-cols-[minmax(0,1fr)] list-decimal gap-1 pl-5" start={node.start ?? 1}>
          {#each node.children ?? [] as item, itemIndex (itemIndex)}
            {@render listItems(item)}
          {/each}
        </ol>
      {:else}
        <ul class="m-0 grid min-w-0 max-w-full grid-cols-[minmax(0,1fr)] list-disc gap-1 pl-5">
          {#each node.children ?? [] as item, itemIndex (itemIndex)}
            {@render listItems(item)}
          {/each}
        </ul>
      {/if}
    {:else if node.type === "blockquote"}
      <blockquote
        class="m-0 grid min-w-0 max-w-full grid-cols-[minmax(0,1fr)] gap-2 border-l-2 pl-3 {tone === 'inverse'
          ? 'border-current/40 text-inherit'
          : 'border-border-strong text-secondary'}"
      >
        {@render blockNodes(node.children ?? [])}
      </blockquote>
    {:else if node.type === "thematicBreak"}
      <hr
        class="m-0 border-0 border-t {tone === 'inverse' ? 'border-current/30' : 'border-border'}"
      />
    {:else if node.type === "table"}
      <div
        class="min-w-0 max-w-full overflow-x-auto rounded-md border {tone === 'inverse'
          ? 'border-current/25'
          : 'border-border'}"
      >
        <table class="w-full border-collapse text-left text-xs">
          <thead class={tone === "inverse" ? "bg-current/10" : "bg-surface-subtle"}>
            <tr>
              {#each node.children?.[0]?.children ?? [] as cell, column (column)}
                <th
                  scope="col"
                  class="whitespace-nowrap border-b bg-transparent px-2.5 py-1.5 font-semibold {tone === 'inverse'
                    ? 'border-current/25 text-inherit'
                    : 'border-border text-secondary'} {alignClass(node.align?.[column])}"
                  >{@render inlineNodes(cell.children ?? [])}</th
                >
              {/each}
            </tr>
          </thead>
          <tbody>
            {#each node.children?.slice(1) ?? [] as row, rowIndex (rowIndex)}
              <tr>
                {#each row.children ?? [] as cell, column (column)}
                  <td
                    class="border-b px-2.5 py-1.5 align-top {tone === 'inverse'
                      ? 'border-current/20'
                      : 'border-border'} {alignClass(node.align?.[column])}"
                    >{@render inlineNodes(cell.children ?? [])}</td
                  >
                {/each}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else if node.type === "footnoteDefinition"}
      <div class="grid min-w-0 max-w-full grid-cols-[auto_minmax(0,1fr)] gap-1.5 text-2xs {tone === 'inverse' ? 'text-inherit' : 'text-muted'}">
        <span class={tone === "inverse" ? "text-inherit" : "text-accent"}>[{node.label ?? node.identifier ?? "?"}]</span>
        <span class="grid min-w-0 max-w-full grid-cols-[minmax(0,1fr)] gap-1">{@render blockNodes(node.children ?? [])}</span>
      </div>
    {:else if node.type === "html"}
      <p class="m-0 whitespace-pre-wrap">{node.value ?? ""}</p>
    {:else if node.children}
      {@render blockNodes(node.children)}
    {:else if node.value}
      <p class="m-0 whitespace-pre-wrap">{node.value}</p>
    {/if}
  {/each}
{/snippet}

<!-- Auto-minimum Grid tracks can exceed a narrow comment even with min-w-0 on
     the grid itself. Constrain each Markdown block's track instead of clipping
     it; only a wide table owns horizontal scrolling. -->
<div
  class:inverse={tone === "inverse"}
  class="grid min-w-0 max-w-full grid-cols-[minmax(0,1fr)] {compact
    ? 'gap-1.5 text-xs leading-[1.6]'
    : 'gap-2 text-[13px] leading-[1.7]'} [overflow-wrap:anywhere] {tone === 'inverse'
    ? 'text-canvas'
    : 'text-text'}"
>
  {@render blockNodes(tree.children ?? [])}
</div>

<style>
  :global([data-theme="dark"]) .inverse {
    color: var(--color-text);
  }
</style>
