<script lang="ts">
  import brandIcon from "../../../assets/brand/icon.png";
  let { compact = false }: { compact?: boolean } = $props();
</script>

<span class="brand-mark" aria-hidden={compact ? true : undefined}>
  <img class="brand-mark__symbol" src={brandIcon} alt="" />
  {#if !compact}<span class="brand-mark__word">Teamva</span>{/if}
</span>

<style>
  .brand-mark { display: inline-flex; align-items: center; gap: 11px; color: inherit; }
  .brand-mark__symbol { width: var(--brand-symbol-size, 31px); height: var(--brand-symbol-size, 31px); flex: none; }
  .brand-mark__word { font-size: 15px; font-weight: 600; letter-spacing: -.025em; }
  @media (max-width: 560px) { .brand-mark__word { font-size: 14px; } }
</style>
