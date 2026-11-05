<script lang="ts">
  import { onMount } from "svelte";
  import { fade } from "svelte/transition";
  import {
    ArrowUpRight, ArrowRight, GitBranch, Layers3, MessageSquareText, ShieldCheck, Cpu,
    FolderGit2, BookOpen, Puzzle, Plug, History, Check, Menu, X, Terminal
  } from "lucide-svelte";
  import LanguageSwitch from "./components/LanguageSwitch.svelte";
  import BrandMark from "./components/BrandMark.svelte";
  import { languageHref, siteLanguage } from "./language.svelte";
  import { siteCopy } from "./copy";
  import "./landing.css";
  import tasks from "../../docs/screenshots/multitask.png";
  import graph from "../../docs/screenshots/graph.png";
  import conversation from "../../docs/screenshots/agent-conversation.png";
  import resources from "../../docs/screenshots/resources.png";
  import agents from "../../docs/screenshots/agents.png";
  import skills from "../../docs/screenshots/skills.png";
  import mcp from "../../docs/screenshots/mcp.png";
  import memories from "../../docs/screenshots/memories.png";
  import tasksZh from "../../docs/screenshots/zh/multitask.png";
  import graphZh from "../../docs/screenshots/zh/graph.png";
  import conversationZh from "../../docs/screenshots/zh/agent-conversation.png";
  import resourcesZh from "../../docs/screenshots/zh/resources.png";
  import agentsZh from "../../docs/screenshots/zh/agents.png";
  import skillsZh from "../../docs/screenshots/zh/skills.png";
  import mcpZh from "../../docs/screenshots/zh/mcp.png";
  import memoriesZh from "../../docs/screenshots/zh/memories.png";

  const screenAssets = [
    { id: "tasks", images: { en: tasks, zh: tasksZh }, icon: Layers3 }, { id: "graph", images: { en: graph, zh: graphZh }, icon: GitBranch },
    { id: "conversation", images: { en: conversation, zh: conversationZh }, icon: MessageSquareText }, { id: "resources", images: { en: resources, zh: resourcesZh }, icon: FolderGit2 },
    { id: "agents", images: { en: agents, zh: agentsZh }, icon: Cpu }, { id: "skills", images: { en: skills, zh: skillsZh }, icon: Puzzle },
    { id: "mcp", images: { en: mcp, zh: mcpZh }, icon: Plug }, { id: "memory", images: { en: memories, zh: memoriesZh }, icon: BookOpen }
  ];
  const storyDiagrams = [
    { path: "M45 115H100C180 115 180 55 280 55S380 115 470 115H515M100 115C180 115 180 175 280 175S380 115 470 115", points: [[8, 50], [50, 24], [50, 76], [92, 50]] },
    { path: "M56 115H155C250 115 290 50 476 50M155 115C250 115 290 180 476 180", points: [[10, 50], [85, 22], [85, 78]] },
    { path: "M56 115H482M90 107V123M126 107V123M162 107V123M198 107V123M270 107V123M306 107V123M342 107V123M378 107V123", points: [[10, 50], [48, 50], [86, 50]] }
  ];
  const contextIcons = [FolderGit2, BookOpen, Puzzle, History];
  const s = $derived(siteCopy[siteLanguage.value]);
  const screens = $derived(screenAssets.map((asset, index) => ({ ...asset, image: asset.images[siteLanguage.value], ...s.tour.screens[index] })));
  const heroScreens = $derived([
    { ...screens[0], label: s.hero.tasks, alt: s.hero.taskAlt },
    { ...screens[2], label: s.hero.intervention, alt: s.hero.interventionAlt },
    { ...screens[1], label: s.hero.graph, alt: s.hero.graphAlt }
  ]);
  const docsHref = $derived(languageHref("./docs/"));
  let activeScreen = $state(0);
  let heroView = $state(0);
  let heroStopped = $state(false);
  let heroInView = $state(false);
  let pageVisible = $state(false);
  let menuOpen = $state(false);
  let motion = $state(false);
  let copied = $state(false);
  let copyError = $state(false);
  let landing: HTMLDivElement;
  let heroSelector: HTMLDivElement;
  const heroRunning = $derived(!heroStopped && motion && heroInView && pageVisible);
  const selected = $derived(screens[activeScreen]);
  const command = "pnpm install && pnpm build && pnpm start:browser";

  $effect(() => { document.documentElement.lang = siteLanguage.value === "zh" ? "zh-CN" : "en"; });

  onMount(() => {
    const preference = matchMedia("(prefers-reduced-motion: reduce)");
    const reduceMotion = () => { motion = !preference.matches; };
    reduceMotion();
    preference.addEventListener("change", reduceMotion);
    const visibility = () => { pageVisible = !document.hidden; };
    visibility();
    document.addEventListener("visibilitychange", visibility);
    const heroObserver = new IntersectionObserver(([entry]) => { heroInView = entry.isIntersecting; });
    heroObserver.observe(heroSelector);
    landing.classList.add("effects-ready");
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) if (entry.isIntersecting) {
        entry.target.classList.add("in-view"); observer.unobserve(entry.target);
      }
    }, { threshold: 0.08 });
    landing.querySelectorAll(".reveal").forEach((el) => observer.observe(el));
    let pointerFrame = 0, scrollFrame = 0;
    const move = (event: PointerEvent) => {
      if (!motion || event.pointerType === "touch") return;
      cancelAnimationFrame(pointerFrame);
      pointerFrame = requestAnimationFrame(() => {
        landing.style.setProperty("--pointer-x", `${event.clientX}px`);
        landing.style.setProperty("--pointer-y", `${event.clientY}px`);
        landing.style.setProperty("--parallax-x", `${(event.clientX / innerWidth - .5) * 12}px`);
        landing.style.setProperty("--parallax-y", `${(event.clientY / innerHeight - .5) * 8}px`);
      });
    };
    const progress = () => {
      cancelAnimationFrame(scrollFrame);
      scrollFrame = requestAnimationFrame(() => {
        const distance = document.documentElement.scrollHeight - innerHeight;
        landing.style.setProperty("--scroll-progress", String(distance > 0 ? Math.min(1, Math.max(0, scrollY / distance)) : 0));
      });
    };
    progress();
    window.addEventListener("pointermove", move, { passive: true });
    window.addEventListener("scroll", progress, { passive: true });
    window.addEventListener("resize", progress);
    return () => {
      observer.disconnect(); heroObserver.disconnect(); cancelAnimationFrame(pointerFrame); cancelAnimationFrame(scrollFrame);
      document.removeEventListener("visibilitychange", visibility);
      window.removeEventListener("pointermove", move); window.removeEventListener("scroll", progress);
      window.removeEventListener("resize", progress); preference.removeEventListener("change", reduceMotion);
    };
  });

  function selectHero(view: number) {
    heroStopped = true;
    heroView = view;
  }

  function advanceHero(event: AnimationEvent) {
    if (event.animationName === "hero-progress" && heroRunning) heroView = (heroView + 1) % heroScreens.length;
  }

  async function copyCommand() {
    try { await navigator.clipboard.writeText(command); copied = true; copyError = false; }
    catch { copied = false; copyError = true; }
  }
</script>

<svelte:head>
  <title>{s.title}</title>
  <meta name="description" content={s.description} />
  <meta property="og:title" content={s.title} />
  <meta property="og:description" content={s.description} />
  <meta property="og:locale" content={siteLanguage.value === "zh" ? "zh_CN" : "en_US"} />
</svelte:head>

<div class="landing" id="top" lang={siteLanguage.value === "zh" ? "zh-CN" : "en"} bind:this={landing} data-motion={motion ? "on" : "off"}>
  <div class="pointer-light" aria-hidden="true"></div>
  <a class="skip-link" href="#main">{s.skip}</a>
  <header class="landing-nav">
    <a href="#top" class="wordmark" aria-label={s.home}><BrandMark compact/>Teamva<span class="preview-label">{s.preview}</span></a>
    <nav class:open={menuOpen} aria-label={s.nav.label}>
      <a href="#product" onclick={() => menuOpen = false}>{s.nav.workspace}</a>
      <a href="#super-agent" onclick={() => menuOpen = false}>{s.nav.research} <span>↗</span></a>
      <a href="#possibilities" onclick={() => menuOpen = false}>{s.nav.why}</a>
      <a href={docsHref}>{s.nav.docs}</a>
    </nav>
    <div class="nav-actions">
      <LanguageSwitch />
      <a class="nav-start" href="#start">{s.nav.start} <ArrowUpRight size={15}/></a>
      <button class="mobile-toggle" aria-label={menuOpen ? s.nav.close : s.nav.open} aria-expanded={menuOpen} onclick={() => menuOpen = !menuOpen}>
        {#if menuOpen}<X size={20}/>{:else}<Menu size={20}/>{/if}
      </button>
    </div>
    <div class="scroll-progress" aria-hidden="true"></div>
  </header>

  <main id="main">
    <section class="launch-hero">
      <div class="hero-grid" aria-hidden="true"></div>
      <div class="grid-beam" aria-hidden="true"></div>
      <div class="aurora aurora-a" aria-hidden="true"></div><div class="aurora aurora-b" aria-hidden="true"></div>
      <div class="orbital" aria-hidden="true"><i></i><i></i><i></i><span>+</span></div>
      <div class="hero-content">
        <a class="era-badge" href="#super-agent"><span class="signal"></span>{s.hero.badge}<ArrowUpRight size={15}/></a>
        <h1>{s.hero.line1}<br/><span class="gradient-word">{s.hero.line2}</span></h1>
        <p class="hero-description">{s.hero.description}<br class="desktop-break"/>{" "}{s.hero.detail}</p>
        <div class="launch-actions">
          <a class="launch-button primary" href="#start">{s.hero.primary}<ArrowUpRight size={17}/></a>
          <a class="launch-button secondary" href="#product">{s.hero.secondary}<ArrowRight size={17}/></a>
        </div>
        <div class="hero-proof">{#each s.hero.proof as proof}<span><Check size={14}/>{proof}</span>{/each}</div>
      </div>

      <div class="product-preview" id="product" data-autoplay={heroStopped ? "stopped" : heroRunning ? "running" : "paused"}>
        <div class="preview-heading"><h2>{s.hero.previewTitle}</h2></div>
        <div class="hero-view-switch" role="group" aria-label={s.hero.selector} bind:this={heroSelector}>
          {#each heroScreens as screen, index (screen.id)}
            <button aria-pressed={heroView === index} onclick={() => selectHero(index)}>
              <screen.icon size={16}/>{screen.label}
              {#if heroView === index}<span class="hero-view-progress" aria-hidden="true">{#if !heroStopped}<span class="hero-view-progress-fill" onanimationend={advanceHero}></span>{/if}</span>{/if}
            </button>
          {/each}
        </div>
        <figure class="hero-console">
          <div class="hero-image">
            {#key heroView}
              <img in:fade={{ duration: motion ? 220 : 0 }} src={heroScreens[heroView].image} width="3200" height="2200" alt={heroScreens[heroView].alt} fetchpriority="high"/>
            {/key}
          </div>
        </figure>
      </div>
    </section>

    <section class="era-section landing-wrap reveal" id="super-agent">
      <div class="section-kicker"><span>{s.era.kicker}</span><span>{s.era.side}</span></div>
      <div class="era-layout">
        <h2 class="preserve-lines">{s.era.heading}<br/><em>{s.era.emphasis}</em></h2>
        <div class="era-copy">
          <span class="research-chip">{s.era.chip}</span><h3>{s.era.title}</h3>
          {#each s.era.paragraphs as paragraph}<p>{paragraph}</p>{/each}
          <a class="text-link" href="https://github.com/deepseek-ai/dsh-superagent-prototype" target="_blank" rel="noreferrer">{s.era.source}<ArrowUpRight size={16}/></a>
          <small>{s.era.disclaimer}</small>
        </div>
      </div>
      <div class="principle-row">
        {#each s.era.principles as principle, index}
          {#if index > 0}<div class="flow-connector" aria-hidden="true"><span></span><ArrowRight size={18}/></div>{/if}
          <div class="principle-step"><span>0{index + 1}</span><strong>{principle}</strong></div>
        {/each}
      </div>
    </section>

    <section class="engine-section landing-wrap reveal" id="coding-tools">
      <div class="section-kicker">{s.engines.kicker}</div>
      <h2>{s.engines.title}</h2><p class="engine-lead">{s.engines.body}</p>
      <div class="engine-cards">{#each s.engines.cards as engine}<article><Cpu size={22}/><h3>{engine.name}</h3><p>{engine.note}</p></article>{/each}</div>
      <div class="engine-next"><div><h3>{s.engines.nextTitle}</h3><p>{s.engines.nextBody}</p></div><a class="text-link" href={`${docsHref}#providers`}>{s.engines.link}<ArrowUpRight size={16}/></a></div>
    </section>

    <section class="showcase-section" id="workspace">
      <div class="landing-wrap">
        <div class="section-kicker"><span>{s.tour.kicker}</span><span>{s.tour.side}</span></div>
        <div class="showcase-heading reveal"><h2>{s.tour.heading}<br/><em>{s.tour.emphasis}</em></h2><p>{s.tour.lead}<br/>{s.tour.detail}</p></div>
        <div class="product-tabs" role="group" aria-label={s.tour.label}>
          {#each screens as screen, index}<button data-screen={screen.id} aria-pressed={activeScreen === index} onclick={() => activeScreen = index}><screen.icon size={16}/>{screen.label}</button>{/each}
        </div>
        <div class="showcase-panel">
          <div class="showcase-caption"><span>{String(activeScreen + 1).padStart(2, "0")} / {String(screens.length).padStart(2, "0")}</span><div><h3>{selected.title}</h3><p>{selected.text}</p></div><a href={selected.image} target="_blank" rel="noreferrer" aria-label={`${s.tour.full}${siteLanguage.value === "zh" ? "：" : ": "}${selected.label}`}><ArrowUpRight size={22}/></a></div>
          {#key activeScreen}<img in:fade={{ duration: motion ? 220 : 0 }} src={selected.image} width="3200" height="2200" alt={`${selected.title} ${s.tour.alt}`} loading="lazy"/>{/key}
        </div>
        <p class="screenshot-note">{s.tour.note}</p>
      </div>
    </section>

    <section class="possibilities landing-wrap" id="possibilities">
      <div class="section-kicker"><span>{s.features.kicker}</span><span>{s.features.side}</span></div>
      <div class="features-heading reveal"><h2>{s.features.heading}<br/><em>{s.features.emphasis}</em></h2><p>{s.features.lead}<br/>{s.features.detail}</p></div>
      <div class="work-stories">
        {#each s.features.stories as story, index}
          <article class="work-chapter reveal">
            <div class="work-chapter-copy">
              <span class="work-chapter-theme">{story.theme}</span>
              <h3>{story.heading}<br/><em>{story.emphasis}</em></h3>
              <p>{story.text}</p>
            </div>
            <div class={`work-chapter-art work-chapter-art-${index}`} aria-hidden="true">
              <svg viewBox="0 0 560 230" preserveAspectRatio="none" fill="none">
                <path class="work-chapter-track" d={storyDiagrams[index].path}/>
                <path class="work-chapter-flow" d={storyDiagrams[index].path}/>
              </svg>
              {#each story.labels as label, point}
                <div class="work-chapter-point" style={`left:${storyDiagrams[index].points[point][0]}%;top:${storyDiagrams[index].points[point][1]}%`}><i></i><span>{label}</span></div>
              {/each}
            </div>
          </article>
        {/each}
      </div>
    </section>

    <section class="context-section landing-wrap reveal">
      <div class="context-copy"><span class="section-kicker">{s.context.kicker}</span><h2 class="preserve-lines">{s.context.heading}<br/><em>{s.context.emphasis}</em></h2><p>{s.context.body}</p><a class="text-link" href="#workspace" onclick={() => activeScreen = 3}>{s.context.link}<ArrowUpRight size={16}/></a></div>
      <div class="context-orbit" aria-label={s.context.label}>
        <div class="orbit-ring ring-one"></div><div class="orbit-ring ring-two"></div>
        <div class="orbit-core"><BrandMark compact/><strong>{s.context.core}</strong><small>{s.context.sub}</small></div>
        {#each s.context.nodes as node, index}
          {@const Icon = contextIcons[index]}
          <div class={`context-node node-${["a", "b", "c", "d"][index]}`}><Icon size={18}/><div><strong>{node.title}</strong><small>{node.detail}</small></div></div>
        {/each}
      </div>
    </section>

    <section class="local-band" id="local-first">
      <div class="landing-wrap local-band-inner reveal">
        <div class="local-symbol" aria-hidden="true"><ShieldCheck size={54} strokeWidth={1}/><span></span></div>
        <div><div class="section-kicker">{s.local.kicker}</div><h2>{s.local.heading}<br/><em>{s.local.emphasis}</em></h2><p>{s.local.body}<br/>{s.local.detail}</p><div class="boundary-note"><ShieldCheck size={17}/><span>{s.local.boundary}</span></div></div>
        <div class="local-stack">{#each s.local.layers as layer}<div><span class="signal"></span>{layer}<Check size={14}/></div>{/each}<small>{s.local.note}</small></div>
      </div>
    </section>

    <section class="faq-section landing-wrap">
      <div class="section-kicker"><span>{s.faq.kicker}</span><span>{s.faq.side}</span></div>
      <div class="faq-layout"><h2>{s.faq.heading}<br/><em>{s.faq.emphasis}</em></h2><div>{#each s.faq.items as [question, answer]}<details><summary>{question}<span>+</span></summary><p>{answer}</p></details>{/each}</div></div>
    </section>

    <section class="start-section landing-wrap reveal" id="start">
      <div class="start-glow" aria-hidden="true"></div><div class="section-kicker">{s.start.kicker}</div><h2>{s.start.heading}<br/><em>{s.start.emphasis}</em></h2><p>{s.start.body}<br/>{s.start.requirements}</p>
      <div class="install-command"><Terminal size={19}/><code>{command}</code><button onclick={copyCommand} aria-label={s.start.copyLabel}>{#if copied}<Check size={16}/>{:else}{s.start.copy}{/if}</button></div>
      <p class="copy-feedback" role="status">{copied ? s.start.copied : copyError ? s.start.copyError : s.start.note}</p><a class="launch-button primary" href={docsHref}>{s.start.docs}<ArrowUpRight size={17}/></a>
    </section>
  </main>

  <footer class="landing-footer landing-wrap">
    <div><a href="#top" class="wordmark"><BrandMark compact/>Teamva</a><p>{s.footer.line1}<br/>{s.footer.line2}</p></div>
    <div><a href="#workspace">{s.footer.tour}</a><a href={docsHref}>{s.footer.docs}</a><a href="#super-agent">{s.footer.research}</a><a href="https://github.com/teamva-ai" target="_blank" rel="noreferrer">{s.footer.github}<ArrowUpRight size={14}/></a></div>
    <p class="footer-support">{s.footer.support}</p>
    <div class="footer-bottom"><span>© {new Date().getFullYear()} Teamva · {s.footer.independent}</span><span>{s.footer.motto}</span></div>
  </footer>
</div>
