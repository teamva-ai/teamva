<script lang="ts">
  import { onMount } from "svelte";
  import {
    Check,
    CirclePause,
    GitPullRequestArrow,
    MessageSquareText,
    Pause,
    Play,
    RotateCcw,
    Sparkles
  } from "lucide-svelte";

  const steps = [
    { label: "Delegate", note: "Two mentions become two sessions" },
    { label: "Run in parallel", note: "Scout and Builder work independently" },
    { label: "Handoff", note: "Scout publishes context for Builder" },
    { label: "Return pass", note: "Builder asks Scout to verify" },
    { label: "Review", note: "Evidence is ready for you" }
  ] as const;

  let activeStep = $state(0);
  let playing = $state(true);

  onMount(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    if (media.matches) playing = false;
    const timer = window.setInterval(() => {
      if (playing) activeStep = (activeStep + 1) % steps.length;
    }, 2300);
    return () => window.clearInterval(timer);
  });

  function pickStep(index: number) {
    activeStep = index;
    playing = false;
  }
</script>

<div class="orchestration-demo" aria-label="Animated example of an Issue coordinating multiple coding agents">
  <div class="demo-window">
    <div class="demo-titlebar">
      <div class="window-controls" aria-hidden="true"><i></i><i></i><i></i></div>
      <span>Atlas / Reliability</span>
      <span class="demo-titlebar__status"><i></i> Local</span>
    </div>

    <div class="demo-shell">
      <aside class="demo-sidebar" aria-label="Example project navigation">
        <div class="demo-project">
          <span class="demo-project__mark">A</span>
          <div><strong>Atlas</strong><small>3 agents ready</small></div>
        </div>
        <div class="demo-nav">
          <span>Overview</span>
          <span class="is-active"><MessageSquareText size={13} /> Issues <b>6</b></span>
          <span>Resources</span>
          <span>Project memory</span>
          <span>Schedules</span>
        </div>
        <div class="demo-sidebar__bottom">
          <span>Agents</span><span>Programming tools</span>
        </div>
      </aside>

      <section class="demo-issue">
        <div class="demo-issue__header">
          <div><small>ISSUE AM-48</small><strong>Make retry recovery deterministic</strong></div>
          <span class:review-ready={activeStep === 4} class="issue-state">
            {activeStep === 4 ? "Ready for review" : "In progress"}
          </span>
        </div>

        <div class="demo-thread">
          <article class="thread-entry thread-entry--user">
            <span class="avatar avatar--user">M</span>
            <div>
              <header><strong>Maya</strong><time>09:41</time></header>
              <p>Trace the recovery path and make the checkpoint safe across a forced restart.</p>
              <p class="mention-line"><span>@Scout</span> map the failure boundary. <span>@Builder</span> prepare the patch.</p>
            </div>
          </article>

          <article class:thread-entry--visible={activeStep >= 1} class="thread-entry thread-entry--agent">
            <span class="avatar avatar--scout">S</span>
            <div>
              <header><strong>Scout</strong><em>Codex</em><time>{activeStep < 2 ? "Working" : "09:43"}</time></header>
              {#if activeStep < 2}
                <div class="stream-lines" aria-label="Scout is working"><i></i><i></i><i></i></div>
              {:else}
                <p>The retry cursor can advance before the event is durable. The invariant belongs at the transaction boundary.</p>
                <p class="mention-line"><span>@Builder</span> apply the cursor write after the outbox insert.</p>
              {/if}
            </div>
          </article>

          <article class:thread-entry--visible={activeStep >= 1} class="thread-entry thread-entry--agent">
            <span class="avatar avatar--builder">B</span>
            <div>
              <header><strong>Builder</strong><em>OpenCode</em><time>{activeStep < 3 ? "Working" : "09:47"}</time></header>
              {#if activeStep < 3}
                <div class="stream-lines stream-lines--orange" aria-label="Builder is working"><i></i><i></i><i></i></div>
              {:else}
                <p>Patch and crash-recovery tests are ready. The forced restart now replays exactly once.</p>
                <p class="mention-line"><span>@Scout</span> verify the new recovery trace.</p>
              {/if}
            </div>
          </article>

          <article class:thread-entry--visible={activeStep >= 4} class="thread-entry thread-entry--result">
            <span class="avatar avatar--scout"><Check size={13} /></span>
            <div>
              <header><strong>Scout · Session 2</strong><em>Codex</em><time>09:50</time></header>
              <p>Verified the crash window and the two recovery cases. No duplicate event was observed.</p>
              <div class="evidence-row"><GitPullRequestArrow size={13} /> 3 files changed <span>·</span> 8 tests passed</div>
            </div>
          </article>
        </div>

        <div class="demo-composer"><span>Comment or @mention an agent…</span><button type="button">Send</button></div>
      </section>

      <aside class="demo-runs" aria-label="Example agent runs">
        <div class="demo-runs__header"><strong>Agent runs</strong><button type="button" aria-label="Pause issue"><CirclePause size={15} /></button></div>
        <p>Each mention starts an independent session.</p>
        <div class="run-card">
          <div><span class="avatar avatar--scout">S</span><div><strong>Scout</strong><small>Session 1 · Codex</small></div></div>
          <span class:status-running={activeStep === 1} class:status-done={activeStep >= 2} class="run-status">
            {activeStep === 0 ? "Queued" : activeStep === 1 ? "Running" : "Done"}
          </span>
        </div>
        <div class="run-card">
          <div><span class="avatar avatar--builder">B</span><div><strong>Builder</strong><small>Session 1 · OpenCode</small></div></div>
          <span class:status-running={activeStep > 0 && activeStep < 3} class:status-done={activeStep >= 3} class="run-status">
            {activeStep === 0 ? "Queued" : activeStep < 3 ? "Running" : "Done"}
          </span>
        </div>
        <div class:run-card--visible={activeStep >= 3} class="run-card">
          <div><span class="avatar avatar--scout">S</span><div><strong>Scout</strong><small>Session 2 · Codex</small></div></div>
          <span class:status-running={activeStep === 3} class:status-done={activeStep >= 4} class="run-status">
            {activeStep < 3 ? "Waiting" : activeStep === 3 ? "Running" : "Done"}
          </span>
        </div>
        <div class="demo-runs__event">
          {#if activeStep === 0}<Sparkles size={14} /> 2 activations persisted
          {:else if activeStep === 1}<RotateCcw size={14} /> Runs streaming live
          {:else if activeStep === 2}<MessageSquareText size={14} /> Public handoff created
          {:else if activeStep === 3}<RotateCcw size={14} /> Return pass started
          {:else}<Check size={14} /> Review evidence collected{/if}
        </div>
      </aside>
    </div>
  </div>

  <div class="demo-timeline">
    <div class="demo-timeline__steps">
      {#each steps as step, index}
        <button class:is-active={activeStep === index} type="button" onclick={() => pickStep(index)} aria-label={`Show step ${index + 1}: ${step.label}`}>
          <span>{String(index + 1).padStart(2, "0")}</span>
          <div><strong>{step.label}</strong><small>{step.note}</small></div>
        </button>
      {/each}
    </div>
    <button class="demo-play" type="button" onclick={() => (playing = !playing)} aria-label={playing ? "Pause demo" : "Play demo"}>
      {#if playing}<Pause size={15} fill="currentColor" />{:else}<Play size={15} fill="currentColor" />{/if}
    </button>
  </div>
</div>
