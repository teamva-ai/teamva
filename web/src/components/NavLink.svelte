<script lang="ts">
  import type { Snippet } from "svelte";
  import { location, navigate } from "../lib/router.svelte";

  let { to, class: className = "", activeClass = "", children, ariaLabel }: {
    to: string;
    class?: string;
    activeClass?: string;
    children: Snippet<[state: { isActive: boolean }]>;
    ariaLabel?: string;
  } = $props();

  const isActive = $derived(location.path === to || (to !== "/" && location.path.startsWith(to + "/")));

  function handleClick(event: MouseEvent) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) {
      return;
    }
    event.preventDefault();
    navigate(to);
  }
</script>

<a
  href={to}
  class={isActive && activeClass ? `${className} ${activeClass}` : className}
  aria-label={ariaLabel}
  aria-current={isActive ? "page" : undefined}
  onclick={handleClick}
>
  {@render children({ isActive })}
</a>
