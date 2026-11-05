<script lang="ts">
  import { Check } from "lucide-svelte";
  import type { ThemeTokens } from "../lib/themeTokens";
  let { label, description, tokens, selected = false, onchange, onkeydown, tabindex = -1, id }: {
    label: string; description: string; tokens: ThemeTokens; selected?: boolean;
    onchange: () => void; onkeydown?: (event: KeyboardEvent) => void; tabindex?: number; id: string;
  } = $props();
</script>

<button type="button" role="radio" aria-checked={selected} aria-label={label} {tabindex} {onkeydown}
  data-palette-option={id} onclick={onchange} class="theme-card" class:selected
   style={`--preview-canvas:${tokens.canvas};--preview-surface:${tokens.surface};--preview-subtle:${tokens.surfaceSubtle};--preview-border:${tokens.border};--preview-text:${tokens.text};--preview-muted:${tokens.textMuted};--preview-accent:${tokens.accent};--preview-accent-subtle:${tokens.accentSubtle};--preview-control:${tokens.control}`}>
  <span class="theme-card__scene" aria-hidden="true">
    <span class="theme-card__toolbar"><i></i><i></i><i></i><b></b></span>
    <span class="theme-card__workspace">
      <span class="theme-card__sidebar"><i></i><i></i><i></i></span>
      <span class="theme-card__content"><b></b><span><i></i><i></i></span><em></em></span>
    </span>
  </span>
  <span class="theme-card__copy"><strong>{label}</strong><span>{description}</span></span>
  <span class="theme-card__check" aria-hidden="true">{#if selected}<Check size={13} />{/if}</span>
</button>

<style>
  .theme-card { position:relative; display:grid; min-width:0; gap:0; padding:5px; text-align:left; cursor:pointer; border:1px solid var(--color-border); border-radius:12px; color:var(--color-text); background:var(--color-surface); transition:border-color 120ms,background 120ms; scroll-margin-block:12px 88px; }
  .theme-card:hover { border-color:var(--color-border-strong); background:var(--color-surface-hover); }
  .theme-card.selected { border-color:var(--color-accent-text); box-shadow:0 0 0 1px var(--color-accent-text); }
  .theme-card__scene { display:block; overflow:hidden; height:100px; border:1px solid var(--preview-border); border-radius:8px; background:var(--preview-canvas); }
  .theme-card__toolbar { display:flex; align-items:center; gap:3px; height:17px; padding:0 7px; border-bottom:1px solid var(--preview-border); background:var(--preview-surface); }
  .theme-card__toolbar i { width:3px; height:3px; border-radius:50%; background:var(--preview-muted); opacity:.55; }
  .theme-card__toolbar b { margin-left:auto; width:30%; height:4px; border-radius:2px; background:var(--preview-border); }
  .theme-card__workspace { display:grid; height:83px; grid-template-columns:25% 1fr; }
  .theme-card__sidebar { display:grid; align-content:start; gap:7px; padding:12px 7px; background:var(--preview-subtle); border-right:1px solid var(--preview-border); }
  .theme-card__sidebar i { width:85%; height:4px; border-radius:2px; background:var(--preview-muted); opacity:.5; }
  .theme-card__sidebar i:first-child { width:100%; background:var(--preview-accent); opacity:1; }
  .theme-card__content { display:grid; align-content:start; gap:8px; padding:12px; }
  .theme-card__content>b { width:52%; height:5px; border-radius:2px; background:var(--preview-text); }
  .theme-card__content>span { display:flex; gap:5px; }
  .theme-card__content i { display:block; flex:1; height:22px; border:1px solid var(--preview-border); border-radius:4px; background:var(--preview-surface); }
  .theme-card__content em { width:35%; height:7px; border-radius:3px; background:var(--preview-control); }
  .theme-card__copy { display:grid; gap:3px; padding:10px 27px 8px 7px; }
  .theme-card__copy strong { font-size:var(--text-sm); font-weight:600; }
  .theme-card__copy>span { color:var(--color-text-muted); font-size:var(--text-xs); line-height:1.5; }
  .theme-card__check { position:absolute; right:12px; bottom:32px; display:grid; place-items:center; width:18px; height:18px; border-radius:50%; border:1px solid var(--color-control-border); color:var(--color-control-text); }
  .selected .theme-card__check { background:var(--color-control); border-color:var(--color-control); }
  @media(prefers-reduced-motion:reduce) { .theme-card { transition:none; } }
</style>
