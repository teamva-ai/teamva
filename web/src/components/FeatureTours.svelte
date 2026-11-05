<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { CircleHelp, Pause, Play, RotateCcw } from "lucide-svelte";
  import { onMount, untrack } from "svelte";
  import { api } from "../lib/api";
  import { localeInfo } from "../lib/i18n.svelte";
  import { queryClient } from "../lib/query";
  import { featureTourForPath, featureTourQueryKey, mergeFeatureTourState, parseFeatureTourState, type FeatureTourId, type FeatureTourState } from "../lib/featureTours";
  import { featureTourTranslations } from "../locales/featureTours";
  import { Button } from "../ui";
  import Modal from "../ui/Modal.svelte";
  import FeatureTourAnimation from "./FeatureTourAnimation.svelte";

  let { userId, path, enabled }: { userId: string; path: string; enabled: boolean } = $props();
  const text = $derived(featureTourTranslations[localeInfo.value]);
  const currentTour = $derived(featureTourForPath(path));
  let visibleTour = $state<FeatureTourId | null>(null);
  let owner = $state("");
  let locallySeen = $state<FeatureTourId[]>([]);
  let failedSaves = $state<FeatureTourId[]>([]);
  let otherModalOpen = $state(false);
  let observing = $state(false);
  let documentHidden = $state(false);
  let paused = $state(false);
  let replay = $state(0);
  const saving = new Set<string>();

  const history = createQuery<FeatureTourState>(() => ({
    queryKey: featureTourQueryKey(userId),
    queryFn: async ({ signal }) => parseFeatureTourState(await api("/me/feature-tours", { signal })),
    enabled: Boolean(userId),
    staleTime: 30_000,
    retry: false
  }));

  async function remember(id: FeatureTourId, user = userId) {
    const request = `${user}:${id}`;
    if (saving.has(request)) return;
    saving.add(request);
    try {
      const state = parseFeatureTourState(await api(`/me/feature-tours/${id}/seen`, { body: {} }));
      // Two acknowledgements can return out of order. Their monotone union must
      // never replace a newer response or another window's already-seen entry.
      queryClient.setQueryData<FeatureTourState>(featureTourQueryKey(user), (old) => mergeFeatureTourState(old, state));
      if (user === userId) failedSaves = failedSaves.filter((item) => item !== id);
    } catch {
      if (user === userId && !failedSaves.includes(id)) failedSaves = [...failedSaves, id];
    } finally {
      saving.delete(request);
    }
  }

  function open(id: FeatureTourId) {
    visibleTour = id;
    paused = false;
    replay += 1;
    const seen = locallySeen.includes(id) || history.data?.seen.includes(id);
    if (!locallySeen.includes(id)) locallySeen = [...locallySeen, id];
    if (!seen || failedSaves.includes(id)) void remember(id);
  }

  function close() { visibleTour = null; }

  function togglePlayback() { paused = !paused; }

  $effect(() => {
    if (owner === userId) return;
    owner = userId;
    visibleTour = null;
    locallySeen = [];
    failedSaves = [];
  });

  $effect(() => {
    if (visibleTour && (!enabled || currentTour !== visibleTour)) close();
  });

  $effect(() => {
    // Read the query even while another dialog blocks presentation. Its Svelte
    // observer must stay subscribed through a fast close + query completion.
    const loaded = history.isSuccess;
    const serverSeen = history.data?.seen;
    if (!enabled || !observing || !currentTour || visibleTour || otherModalOpen || !loaded || !serverSeen ||
      serverSeen.includes(currentTour) || locallySeen.includes(currentTour)) return;
    // The animation only exists after it is actually presented, never while
    // setup, a business dialog or an unresolved deep link still owns the UI.
    untrack(() => open(currentTour));
  });

  onMount(() => {
    const inspectDialogs = () => {
      otherModalOpen = [...document.querySelectorAll('[role="dialog"], [role="alertdialog"]')]
        .some((element) => !element.hasAttribute("data-feature-tour-dialog"));
    };
    const visibility = () => { documentHidden = document.hidden; };
    const observer = new MutationObserver(inspectDialogs);
    // Shared Modal portals are direct body children. Do not observe every
    // streaming token or file-tree update in a busy project just to find them.
    observer.observe(document.body, { childList: true });
    inspectDialogs();
    visibility();
    observing = true;
    document.addEventListener("visibilitychange", visibility);
    return () => {
      observer.disconnect();
      document.removeEventListener("visibilitychange", visibility);
    };
  });
</script>

{#if currentTour}
  <Button type="button" icon controlSize="sm" variant="ghost" aria-label={text.reopen}
    title={history.isError ? text.seenUnavailable : text.reopen} disabled={!enabled || otherModalOpen}
    data-feature-tour-trigger={currentTour} onclick={() => { if (currentTour) open(currentTour); }}>
    <CircleHelp size={17} aria-hidden="true" />
  </Button>
{/if}

{#if visibleTour}
  {@const guide = text.guides[visibleTour]}
  <Modal title={guide.title} description={text.intro} maxWidth={800} onClose={close} data-feature-tour-dialog={visibleTour}>
    <div class="tour-visual" data-tour-visual>
      {#key `${visibleTour}:${replay}`}
        <FeatureTourAnimation tour={visibleTour} title={guide.title} steps={guide.steps} paused={paused || documentHidden} allowMotion />
      {/key}
      <div class="tour-playback">
        <Button type="button" icon controlSize="sm" aria-label={paused ? text.play : text.pause}
          title={paused ? text.play : text.pause} aria-pressed={paused} data-tour-pause onclick={togglePlayback}>
          {#if paused}<Play size={14} aria-hidden="true" />{:else}<Pause size={14} aria-hidden="true" />{/if}
        </Button>
        <Button type="button" icon controlSize="sm" aria-label={text.replay} title={text.replay} data-tour-replay
          onclick={() => { replay += 1; paused = false; }}><RotateCcw size={14} aria-hidden="true" /></Button>
      </div>
    </div>
    <div class="tour-explanation" data-tour-explanation>
      <p class="tour-description">{guide.description}</p>
      <ol class="tour-steps">
        {#each guide.steps as step, index}<li><span aria-hidden="true">{index + 1}</span>{step}</li>{/each}
      </ol>
      <div class="tour-actions">
        <Button type="button" variant="primary" data-tour-acknowledge onclick={close}>{text.acknowledge}</Button>
      </div>
      {#if failedSaves.includes(visibleTour)}<p class="tour-save-error" role="status">{text.saveFailed}</p>{/if}
    </div>
  </Modal>
{/if}

<style>
  .tour-visual { position:relative; display:grid; align-items:center; min-height:240px; overflow:hidden; border-radius:var(--radius-md); background:var(--color-canvas); }
  .tour-playback { position:absolute; top:12px; right:12px; display:flex; gap:6px; }
  .tour-explanation { display:flex; flex-direction:column; gap:14px; min-height:180px; padding-top:18px; }
  .tour-description { margin:0; color:var(--color-text-secondary); font-size:0.875rem; line-height:1.65; }
  .tour-steps { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; list-style:none; margin:0; padding:0; }
  .tour-steps li { display:flex; align-items:flex-start; gap:7px; min-width:0; color:var(--color-text); font-size:0.75rem; line-height:1.5; }
  .tour-steps li span { display:grid; place-items:center; flex-shrink:0; width:19px; height:19px; border-radius:50%; color:var(--color-accent-text); background:var(--color-accent-subtle); font-size:0.6875rem; font-variant-numeric:tabular-nums; }
  .tour-actions { display:flex; justify-content:flex-end; margin-top:auto; }
  .tour-save-error { margin:0; color:var(--color-warning); font-size:0.75rem; line-height:1.5; }
  @media(max-width:639px) {
    .tour-visual { min-height:260px; }
    .tour-playback { top:8px; right:8px; }
    .tour-explanation { min-height:180px; gap:12px; padding-top:16px; }
    .tour-steps { gap:8px; }
    .tour-steps li { gap:5px; }
  }
</style>
