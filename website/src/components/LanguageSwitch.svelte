<script lang="ts">
  import { setLanguage, siteLanguage } from "../language.svelte";
</script>

<div class="language-switch" role="group" aria-label="Website language">
  <button type="button" lang="en" aria-label="English" aria-pressed={siteLanguage.value === "en"} onclick={() => setLanguage("en")}>EN</button>
  <button type="button" lang="zh-CN" aria-label="简体中文" aria-pressed={siteLanguage.value === "zh"} onclick={() => setLanguage("zh")}>中文</button>
</div>

<style>
  .language-switch { display: inline-flex; align-items: center; flex: none; gap: 2px; padding: 3px; border: 1px solid currentColor; border-radius: 7px; color: inherit; }
  button { min-width: 40px; min-height: 34px; padding: 5px 9px; border: 0; border-radius: 4px; background: transparent; color: inherit; font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  button[aria-pressed="true"] { background: var(--language-active-bg, #c2ff75); color: var(--language-active-ink, #15200c); }
  button:focus-visible { outline: 2px solid currentColor; outline-offset: 3px; }
</style>
