<script lang="ts">
  import { ArrowUpRight, Menu, X } from "lucide-svelte";
  import BrandMark from "./BrandMark.svelte";
  import LanguageSwitch from "./LanguageSwitch.svelte";
  import { languageHref, siteLanguage } from "../language.svelte";
  import { siteCopy } from "../copy";

  let {
    page = "home",
    homeHref = "./",
    docsHref = "./docs/"
  }: {
    page?: "home" | "docs";
    homeHref?: string;
    docsHref?: string;
  } = $props();

  let menuOpen = $state(false);
  const s = $derived(siteCopy[siteLanguage.value]);
  const zh = $derived(siteLanguage.value === "zh");

  const productHref = $derived(languageHref(page === "home" ? "#product" : `${homeHref}#product`));
  const workflowHref = $derived(languageHref(page === "home" ? "#workspace" : `${homeHref}#workspace`));
  const localHref = $derived(languageHref(page === "home" ? "#local-first" : `${homeHref}#local-first`));
</script>

<header class="site-header" data-page={page}>
  <div class="site-header__inner">
    <a class="site-header__brand" href={languageHref(homeHref)} aria-label={s.home} onclick={() => (menuOpen = false)}>
      <BrandMark />
      {#if page === "docs"}<span class="site-header__section">{zh ? "文档" : "Docs"}</span>{/if}
    </a>

    <nav class:site-nav--open={menuOpen} class="site-nav" aria-label={s.nav.label}>
      <a href={productHref} onclick={() => (menuOpen = false)}>{s.nav.workspace}</a>
      <a href={workflowHref} onclick={() => (menuOpen = false)}>{s.footer.tour}</a>
      <a href={localHref} onclick={() => (menuOpen = false)}>{zh ? "本地优先" : "Local-first"}</a>
      <a class:site-nav__active={page === "docs"} href={languageHref(docsHref)} onclick={() => (menuOpen = false)}>{s.nav.docs}</a>
    </nav>

    <div class="site-header__actions">
    <LanguageSwitch />
    <a class="header-cta" href={languageHref(docsHref)}>
      {s.start.docs}
      <ArrowUpRight size={15} strokeWidth={1.8} aria-hidden="true" />
    </a>

    <button
      class="menu-button"
      type="button"
      aria-label={menuOpen ? s.nav.close : s.nav.open}
      aria-expanded={menuOpen}
      onclick={() => (menuOpen = !menuOpen)}
    >
      {#if menuOpen}<X size={21} />{:else}<Menu size={21} />{/if}
    </button>
    </div>
  </div>
</header>
