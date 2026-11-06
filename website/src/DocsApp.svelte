<script lang="ts">
  import {
    ArrowLeft,
    ArrowRight,
    Braces,
    Check,
    ChevronDown,
    ChevronRight,
    CirclePause,
    Clock3,
    Database,
    FileCode2,
    GitBranch,
    Info,
    Layers3,
    ListTree,
    Menu,
    MessageSquareText,
    MonitorCog,
    Search,
    ShieldCheck,
    Sparkles,
    TerminalSquare,
    UsersRound,
    X
  } from "lucide-svelte";
  import SiteFooter from "./components/SiteFooter.svelte";
  import SiteHeader from "./components/SiteHeader.svelte";
  import ChineseDocs, { chineseDocTitles } from "./ChineseDocs.svelte";
  import { languageHref, siteLanguage } from "./language.svelte";

  type NavItem = { id: string; title: string };
  type NavGroup = { title: string; items: NavItem[] };

  const baseNavGroups: NavGroup[] = [
    { title: "Start here", items: [
      { id: "overview", title: "Overview" },
      { id: "mental-model", title: "Mental model" },
      { id: "before-you-begin", title: "Before you begin" },
      { id: "providers", title: "Coding tool integrations" },
      { id: "first-workflow", title: "First workflow" }
    ] },
    { title: "Core concepts", items: [
      { id: "projects-resources", title: "Projects and resources" },
      { id: "issues-comments", title: "Issues and comments" },
      { id: "work-assignments", title: "Work assignments" },
      { id: "graph-conversations", title: "Graph conversations" },
      { id: "agents-sessions", title: "Agents and sessions" },
      { id: "multi-agent-semantics", title: "Multi-agent semantics" },
      { id: "memory-skills", title: "Memory and Skills" },
      { id: "mcp", title: "MCP configuration" }
    ] },
    { title: "Operate", items: [
      { id: "runs-approvals", title: "Runs and approvals" },
      { id: "stop-pause-recover", title: "Stop, pause, and recover" },
      { id: "schedules", title: "Schedules" },
      { id: "changes-review", title: "Changes and review" }
    ] },
    { title: "Trust and limits", items: [
      { id: "data-boundaries", title: "Data boundaries" },
      { id: "permissions", title: "Permissions" },
      { id: "product-boundaries", title: "Product boundaries" }
    ] }
  ];

  const chineseGroups: Record<string, string> = { "Start here": "从这里开始", "Core concepts": "核心概念", "Operate": "执行与操作", "Trust and limits": "信任与边界" };
  const zh = $derived(siteLanguage.value === "zh");
  const navGroups = $derived(baseNavGroups.map((group) => ({
    title: zh ? chineseGroups[group.title] : group.title,
    items: group.items.map((item) => ({ ...item, title: zh ? chineseDocTitles[item.id] : item.title }))
  })));
  const allItems = $derived(navGroups.flatMap((group) => group.items));
  let activeId = $state("overview");
  let mobileNavOpen = $state(false);
  let searchQuery = $state("");

  const filteredGroups = $derived(searchQuery.trim()
    ? navGroups.map((group) => ({
        ...group,
        items: group.items.filter((item) => item.title.toLowerCase().includes(searchQuery.trim().toLowerCase()))
      })).filter((group) => group.items.length > 0)
    : navGroups);

  $effect(() => {
    document.documentElement.lang = siteLanguage.value === "zh" ? "zh-CN" : "en";
    const headings = Array.from(document.querySelectorAll<HTMLElement>(".doc-content [id]"));
    const observer = new IntersectionObserver((entries) => {
      const visible = entries.filter((entry) => entry.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
      if (visible[0]?.target.id) activeId = visible[0].target.id;
    }, { rootMargin: "-112px 0px -68%", threshold: [0, 1] });
    headings.forEach((heading) => observer.observe(heading));
    if (window.location.hash) activeId = window.location.hash.slice(1);
    return () => observer.disconnect();
  });

  function goTo(id: string) {
    activeId = id;
    mobileNavOpen = false;
    document.getElementById(id)?.scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "start" });
    const url = new URL(location.href);
    url.hash = id;
    history.replaceState(null, "", url);
  }
</script>

<svelte:head>
  <title>{zh ? "Teamva 使用指南" : "Teamva — Product guide"}</title>
  <meta name="description" content={zh ? "了解 Teamva 的本机编程工具接入、多 Agent 分工、图对话、资源、Skills、MCP 与执行边界。" : "Learn how Teamva connects installed coding tools and organizes tasks, graph conversations, project context, Skills, and MCP."}/>
</svelte:head>
<SiteHeader page="docs" homeHref="../" docsHref="./" />

<div class="docs-mobile-bar">
  <button type="button" onclick={() => (mobileNavOpen = true)}><Menu size={17} /> {zh ? "使用文档" : "Documentation"}</button>
  <span>{allItems.find((item) => item.id === activeId)?.title ?? (zh ? "概览" : "Overview")}</span>
</div>

<main class="docs-shell">
  <aside class:docs-sidebar--open={mobileNavOpen} class="docs-sidebar" aria-label={zh ? "文档导航" : "Documentation navigation"}>
    <div class="docs-sidebar__mobile-head"><strong>{zh ? "使用文档" : "Documentation"}</strong><button type="button" aria-label={zh ? "关闭文档导航" : "Close documentation navigation"} onclick={() => (mobileNavOpen = false)}><X size={19} /></button></div>
    <label class="docs-search"><Search size={15} /><input bind:value={searchQuery} type="search" placeholder={zh ? "搜索指南章节" : "Search this guide"} aria-label={zh ? "搜索文档导航" : "Search documentation navigation"} /></label>
    <nav>
      {#each filteredGroups as group}
        <div class="docs-nav-group">
          <span>{group.title}</span>
          {#each group.items as item}
            <a class:is-active={activeId === item.id} href={`#${item.id}`} onclick={(event) => { event.preventDefault(); goTo(item.id); }}>{item.title}</a>
          {/each}
        </div>
      {/each}
      {#if filteredGroups.length === 0}<p class="docs-search-empty">{zh ? "没有匹配的章节" : "No matching section"}</p>{/if}
    </nav>
    <a class="docs-sidebar__back" href={languageHref("../")}><ArrowLeft size={14} /> {zh ? "返回产品首页" : "Back to product"}</a>
  </aside>

  {#if mobileNavOpen}<button class="docs-sidebar-backdrop" type="button" aria-label={zh ? "关闭文档导航" : "Close documentation navigation"} onclick={() => (mobileNavOpen = false)}></button>{/if}

  <article class="doc-content">
    {#if zh}
      <ChineseDocs />
    {:else}
    <div class="doc-breadcrumb"><span>Documentation</span><ChevronRight size={13} /><span>Start here</span></div>

    <header class="doc-hero" id="overview">
      <div class="doc-kicker"><Sparkles size={15} /> Product guide · macOS and browser preview</div>
      <h1>Bring the team together.<br />See the work clearly.</h1>
      <p>Start with your coding tools, organize work through Projects and Issues, follow independent agent sessions, and keep tasks and history on your computer.</p>
      <div class="doc-hero__meta"><span><Clock3 size={14} /> 12 minute read</span><span><MonitorCog size={14} /> Current behavior</span></div>
    </header>

    <div class="doc-callout doc-callout--info"><Info size={18} /><div><strong>Teamva is a development preview.</strong><p>This guide covers current setup and workflows. Coding tools connect according to their supported capabilities. Use a fresh data directory for new development builds and preserve your existing data.</p></div></div>

    <section class="doc-section" id="mental-model">
      <div class="doc-section__eyebrow">The mental model</div>
      <h2>One control plane, two kinds of responsibility</h2>
      <p>Teamva brings your coding tools into a shared workspace. The tools execute tasks; Teamva organizes their context, coordination, and results.</p>
      <div class="doc-two-column">
        <article><div class="doc-icon"><TerminalSquare size={19} /></div><h3>Coding agents execute</h3><p>Codex and OpenCode understand code, edit files, invoke tools, run tests, and decide when their native permission policy requires your confirmation.</p><ul class="doc-check-list"><li><Check size={14} /> Provider-native models</li><li><Check size={14} /> File and shell tools</li><li><Check size={14} /> Existing local authentication</li></ul></article>
        <article class="is-accent"><div class="doc-icon"><Layers3 size={19} /></div><h3>Teamva coordinates</h3><p>Projects, Issues, mentions, queues, memory, schedules, activity, and recovery give those agent sessions a shared operating context.</p><ul class="doc-check-list"><li><Check size={14} /> Durable activations</li><li><Check size={14} /> Public handoffs</li><li><Check size={14} /> Human control surface</li></ul></article>
      </div>
      <div class="doc-flow" aria-label="Teamva domain flow"><div><Layers3 size={16} /><span>Project</span></div><ChevronRight size={15} /><div><MessageSquareText size={16} /><span>Issue</span></div><ChevronRight size={15} /><div><UsersRound size={16} /><span>Mention</span></div><ChevronRight size={15} /><div><TerminalSquare size={16} /><span>Session</span></div><ChevronRight size={15} /><div><Check size={16} /><span>Public result</span></div></div>
    </section>

    <section class="doc-section" id="before-you-begin">
      <div class="doc-section__eyebrow">Before you begin</div>
      <h2>Prepare your local workspace</h2>
      <p>The workbench is designed for an individual developer using coding tools locally. Projects and working history stay on your computer, and authentication follows each tool's supported configuration.</p>
      <div class="doc-requirements">
        <div><span>01</span><div><strong>macOS or a browser workspace</strong><p>Use the macOS application, or open the shared workspace in a browser connected to the local service.</p></div></div>
        <div><span>02</span><div><strong>Codex or OpenCode</strong><p>Install and authenticate at least one supported coding tool using its own normal flow.</p></div></div>
        <div><span>03</span><div><strong>A real project directory</strong><p>Add one or more code or documentation directories as Project Resources.</p></div></div>
      </div>
      <p>Source builds require Go 1.25.x, Node.js 24.x, and pnpm 11.x. New development builds initialize empty databases; choose a fresh data directory and retain your daily workspace data.</p>
      <div class="doc-callout"><ShieldCheck size={18} /><div><strong>Authentication remains local.</strong><p>Codex and OpenCode can reuse supported local authentication and configuration. Credentials remain in protected files owned by the coding tool.</p></div></div>
    </section>

    <section class="doc-section" id="providers">
      <div class="doc-section__eyebrow">Coding tool integrations</div>
      <h2>Detect what is installed. Adapt it to the workspace.</h2>
      <p>Teamva automatically detects supported coding CLIs on your computer. Codex and OpenCode can reuse supported local authentication and provider configuration; OpenCode also has a bundled runtime. Choose a custom executable path in settings when needed. Detection identifies tools and capabilities; model access requires the appropriate authentication and configuration.</p>
      <p>The controlled Claude Code adapter can read and edit authorized files, inspect task context, and publish results. Its tool scope is limited to file and task operations, excluding shell, external MCP, user Skills, and plugins. Approvals, recovery, images, MCP, and Skills follow each provider's declared capabilities.</p>
      <p>The Provider API supports dedicated adapters for additional coding agents. More integrations are planned; adding an adapter involves implementation, a new build, and validation with the tool.</p>
    </section>

    <section class="doc-section" id="first-workflow">
      <div class="doc-section__eyebrow">First workflow</div>
      <h2>Move from a goal to a reviewable result</h2>
      <ol class="doc-steps">
        <li><span>1</span><div><h3>Create a Project</h3><p>Name the body of work and add notes that help distinguish it from your other local projects. Set the workspace appearance in global preferences.</p></div></li>
        <li><span>2</span><div><h3>Add Resources</h3><p>Register code directories, documentation directories, or reference URLs. Ready Git resources become part of the Project's authorized workspace map.</p></div></li>
        <li><span>3</span><div><h3>Configure an Agent</h3><p>Choose Codex or OpenCode, optionally set a model, write a focused system prompt, and bind only Skills already installed on this computer.</p></div></li>
        <li><span>4</span><div><h3>Open an Issue and mention the Agent</h3><p>Describe the outcome in the Issue, then write a comment with a structured mention. The mention is persisted with the comment before execution starts.</p><div class="doc-code"><div><i></i><i></i><i></i><span>Issue comment</span></div><code><b>@Frontend Developer</b> preserve the onboarding draft on validation errors and add a keyboard-only test. Post the evidence here when complete.</code></div></div></li>
        <li><span>5</span><div><h3>Follow the session</h3><p>Open the Agent side sheet to see streaming text, tool events, approval requests, context compaction, and the run's final state.</p></div></li>
        <li><span>6</span><div><h3>Review the public result</h3><p>The Agent publishes its summary through the issue comment tool. Review that public result together with tests and repository evidence.</p></div></li>
      </ol>
    </section>

    <div class="doc-divider"><span>Core concepts</span></div>

    <section class="doc-section" id="projects-resources">
      <div class="doc-section__eyebrow">Projects and resources</div>
      <h2>Projects define the operating context</h2>
      <p>A Project groups work that shares Issues, memory, schedules, policy, and authorized resources. Source code stays in the local directories you register.</p>
      <div class="doc-table-wrap"><table><thead><tr><th>Resource type</th><th>Purpose</th><th>Runtime behavior</th></tr></thead><tbody><tr><td><FileCode2 size={14} /> Code directory</td><td>Source repositories and workspaces</td><td>Ready Git roots enter the generated workspace map</td></tr><tr><td><FileCode2 size={14} /> Documentation directory</td><td>Local references outside the code roots</td><td>Authorized only at the configured resource root</td></tr><tr><td><Braces size={14} /> URL</td><td>External documentation or requirements</td><td>Recorded as Project context; normal Provider/network policy applies</td></tr></tbody></table></div>
      <h3>Stable project workspace</h3>
      <p>Runs start from a stable workspace bound to both the Project and its complete approved resource scope. Different scopes have separate repository maps, so preparing one Run cannot overwrite an active Run’s authorization map. An agent discovers the approved ready Git resources without the Issue or Schedule binding itself to a single repository.</p>
    </section>

    <section class="doc-section" id="issues-comments">
      <div class="doc-section__eyebrow">Issues and comments</div>
      <h2>The Issue is the durable unit of collaboration</h2>
      <p>An Issue combines a title, description, manually controlled status, public comment stream, agent sessions, runs, approvals, and change evidence. You control its status: <code>open</code>, <code>resolved</code>, or <code>closed</code>. Review results before marking the work complete.</p>
      <div class="doc-callout"><MessageSquareText size={18} /><div><strong>Mentions are structured data.</strong><p>The UI submits exact Agent IDs alongside the comment body, keeping activation explicit and unambiguous.</p></div></div>
      <h3>Public comments and session detail</h3>
      <p>The Issue stream contains user comments and summaries deliberately published by Agents. Internal control messages, raw stream chunks, tool details, and provider events stay in the relevant session side sheet.</p>
    </section>

    <section class="doc-section" id="work-assignments">
      <div class="doc-section__eyebrow">Work assignments</div>
      <h2>Explicit jobs, explicit dependencies</h2>
      <p>A normal user comment activates one distinct Agent. Selecting a second Agent opens the work-assignment form. Give each job a title, owner, optional instructions, and either an immediate start or explicit prerequisite jobs. The same Agent can own multiple independent jobs.</p>
      <p>Downstream work waits for successful prerequisites and published results. The scheduler handles the wait. Optional AI decomposition produces a draft using your chosen provider; review and submit it to begin execution. Each assignment can open its own conversation, and its prerequisite jobs must form an acyclic dependency graph.</p>
    </section>

    <section class="doc-section" id="graph-conversations">
      <div class="doc-section__eyebrow">Graph conversations</div>
      <h2>Explore alternatives from a shared starting point</h2>
      <p>Fork at an Agent reply to freeze the shared prefix. The original conversation keeps its later continuation, and the new branch starts from the selected reply. Both writable branches can continue independently, with their own Agent and ordered Runs. Move, resize, and zoom the nodes to follow the exploration.</p>
      <p>Graph conversations share project resources and memory, while Issue comments, mentions, and work assignments are managed separately. Continue exploring each branch, then review and combine useful findings yourself.</p>
    </section>

    <section class="doc-section" id="agents-sessions">
      <div class="doc-section__eyebrow">Agents and sessions</div>
      <h2>An Agent is a configuration; a session is work</h2>
      <p>This distinction prevents a long-running task from changing underneath you.</p>
      <div class="doc-definition-list">
        <div><dt>Agent</dt><dd>A reusable name, note, coding tool, model choice, system prompt, and ordered set of installed Skills.</dd></div>
        <div><dt>Agent revision</dt><dd>An immutable snapshot created when the Agent's tool, model, prompt, or Skills change.</dd></div>
        <div><dt>Issue agent instance</dt><dd>An independent session created for one structured mention. It retains the revision it started with.</dd></div>
        <div><dt>Run</dt><dd>One activation, follow-up, correction, or scheduled execution inside an instance.</dd></div>
      </div>
      <p>Activating the same configured Agent in separate comments or separate explicit work assignments creates separate sessions. Use separate comments or assignments to create multiple jobs. Follow-up messages from a side sheet continue only that selected session.</p>
    </section>

    <section class="doc-section" id="multi-agent-semantics">
      <div class="doc-section__eyebrow">Multi-agent semantics</div>
      <h2>Parallel between instances, serial within one instance</h2>
      <p>Different Issue Agent Instances may run at the same time. Runs inside the same instance stay ordered so a follow-up cannot race ahead of the context it depends on.</p>
      <div class="semantics-diagram" aria-label="Multi-agent scheduling semantics">
        <div class="semantics-diagram__issue"><MessageSquareText size={16} /> Issue comment</div>
        <div class="semantics-diagram__branches"><i></i><i></i></div>
        <div class="semantics-diagram__lanes">
          <article><header><span>P</span><div><strong>Product Manager</strong><small>Instance A</small></div><em>parallel</em></header><div class="run-sequence"><span>Run 1</span><ChevronRight size={13} /><span>Run 2</span></div></article>
          <article><header><span>F</span><div><strong>Frontend Developer</strong><small>Instance B</small></div><em>parallel</em></header><div class="run-sequence"><span>Run 1</span><ChevronRight size={13} /><span>Run 2</span></div></article>
        </div>
      </div>
      <h3>Agent-to-agent delegation</h3>
      <p>An Agent can publish a comment that mentions another Agent, creating a new observable activation. Direct self-mention is rejected. Indirect return paths such as <code>A → B → A</code> are allowed because review and revision often require them.</p>
      <div class="doc-callout doc-callout--warning"><CirclePause size={18} /><div><strong>You control review loops.</strong><p>Indirect mention cycles continue until you intervene. Stop the Run, pause a session, or close the task as appropriate. Explicit work-item dependencies must remain acyclic.</p></div></div>
    </section>

    <section class="doc-section" id="memory-skills">
      <div class="doc-section__eyebrow">Memory and Skills</div>
      <h2>Separate durable knowledge from executable capability</h2>
      <div class="doc-two-column doc-two-column--plain">
        <article><div class="doc-icon"><Database size={19} /></div><h3>Project memory</h3><p>Versioned constraints, decisions, facts, and progress that should remain available across Issues and restarts.</p></article>
        <article><div class="doc-icon"><Braces size={19} /></div><h3>Installed Skills</h3><p>Complete local packages containing instructions, references, assets, scripts, or hooks. Agents can bind only versions installed and verified on the current computer.</p></article>
      </div>
      <p>Skills can execute scripts and hooks with your user account's permissions. Install only packages you trust, and treat a Skill update as a code review event rather than a cosmetic catalog update.</p>
    </section>

    <section class="doc-section" id="mcp">
      <div class="doc-section__eyebrow">MCP configuration</div>
      <h2>Connect deliberately, within the Provider's capabilities</h2>
      <p>The Skills &amp; MCP page has a separate MCP manager for personal and project configurations. A same-name project entry overrides the personal entry, including when disabled. Saving configuration does not start a server; explicitly connect to inspect its catalog. Enabled configuration is applied to the next supported agent Run; active work keeps its existing configuration.</p>
      <p>Secrets remain in private local files and are redacted from public responses. Global read-only assistant sessions do not receive custom MCP. Codex and OpenCode use their own native configuration rules; the controlled Claude Code adapter does not expose external MCP, shell, or plugins.</p>
    </section>

    <div class="doc-divider"><span>Operate</span></div>

    <section class="doc-section" id="runs-approvals">
      <div class="doc-section__eyebrow">Runs and approvals</div>
      <h2>Observe what the agent is doing now</h2>
      <p>The session side sheet brings provider activity into a consistent timeline, with text, tools, and approvals clearly distinguished.</p>
      <ul class="doc-feature-list"><li><span><TerminalSquare size={17} /></span><div><strong>Streaming output</strong><p>Consecutive text chunks merge into readable assistant messages.</p></div></li><li><span><Braces size={17} /></span><div><strong>Tool events</strong><p>Tool names, status, and safe result summaries stay distinct from model prose.</p></div></li><li><span><ShieldCheck size={17} /></span><div><strong>Native approvals</strong><p>A confirmation appears only when Codex or OpenCode actually asks for one.</p></div></li><li><span><Database size={17} /></span><div><strong>Context compaction</strong><p>Start and completion are explicit so a condensed session does not look like missing history.</p></div></li></ul>
      <p>Operations follow the Provider's native permission policy. Its actual approval requests become confirmation actions in the UI.</p>
    </section>

    <section class="doc-section" id="stop-pause-recover">
      <div class="doc-section__eyebrow">Stop, pause, and recover</div>
      <h2>Choose the smallest interruption that solves the problem</h2>
      <div class="doc-table-wrap"><table><thead><tr><th>Control</th><th>Scope</th><th>Use it when</th></tr></thead><tbody><tr><td>Stop Run</td><td>Current execution</td><td>Its current direction is wrong or no longer needed</td></tr><tr><td>Pause session</td><td>One agent session</td><td>You need to inspect before allowing more work</td></tr><tr><td>Resume session</td><td>Paused work</td><td>The blocking condition has been resolved</td></tr><tr><td>Close task</td><td>Task activation boundary</td><td>The task should accept no further execution</td></tr></tbody></table></div>
      <p>Backend queue cancellation also applies to dependent work that is still waiting.</p>
      <h3>Recovery after restart</h3>
      <p>Commands and events use durable outboxes. Control state and runtime state live in separate SQLite stores. After a normal restart or forced termination, Teamva reconciles unfinished activity rather than leaving a run permanently labeled as running.</p>
      <p>If database baseline or integrity checks fail, Teamva preserves the database and refuses to start. It does not upgrade, clear, or rebuild that database automatically.</p>
    </section>

    <section class="doc-section" id="schedules">
      <div class="doc-section__eyebrow">Schedules</div>
      <h2>Make recurring local work inspectable</h2>
      <p>A Scheduled Task references one configured, enabled Agent and a prompt. Choose daily or weekly times, a one-time instant, or Cron with an IANA time zone. Preview triggers, run now, inspect history, and create a new Issue or append to an existing Issue in the same project.</p>
      <div class="doc-callout"><Clock3 size={18} /><div><strong>Schedules run on your computer.</strong><p>Keep the computer and Teamva service running when a trigger is due. New schedules skip missed triggers and avoid overlapping themselves by default; existing schedules use their saved policies. Scheduling respects paused or closed tasks and project boundaries.</p></div></div>
    </section>

    <section class="doc-section" id="changes-review">
      <div class="doc-section__eyebrow">Changes and review</div>
      <h2>Pair the summary with repository evidence</h2>
      <p>For every run, the runtime attempts to capture start and end Git state across the Project's ready Git resources. Changes are grouped by repository so you can review each diff in context.</p>
      <div class="doc-change-card"><header><GitBranch size={16} /><strong>atlas-runtime</strong><span>3 files changed</span></header><div><code>internal/recover.go</code><span class="plus">+24</span><span class="minus">−7</span></div><div><code>internal/recover_test.go</code><span class="plus">+61</span><span class="minus">−0</span></div><div><code>runtime/store.go</code><span class="plus">+8</span><span class="minus">−3</span></div></div>
      <p>If capture times out, is interrupted, or cannot assign a Provider report to a repository, the UI records an explicit warning or separate unscoped report. Review results using actual tests and repository evidence; demonstration screenshots use prewritten data.</p>
    </section>

    <div class="doc-divider"><span>Trust and limits</span></div>

    <section class="doc-section" id="data-boundaries">
      <div class="doc-section__eyebrow">Data boundaries</div>
      <h2>Local working history, model requests you configure</h2>
      <p>Tasks, conversations, and project memory are stored on your computer. The interface, local service, scheduler, and execution state also run there.</p>
      <div class="boundary-map">
        <div class="boundary-map__local"><header><span></span><strong>Your Mac</strong></header><div class="boundary-grid"><article><Layers3 size={16} /><span>Projects, Issues, memory</span></article><article><Database size={16} /><span>Control + runtime SQLite</span></article><article><Braces size={16} /><span>Installed Skills</span></article><article><TerminalSquare size={16} /><span>Codex / OpenCode processes</span></article><article><FileCode2 size={16} /><span>Source directories</span></article><article><ShieldCheck size={16} /><span>Provider credentials</span></article></div></div>
        <div class="boundary-map__arrow"><span>Selected prompts<br />and code context</span><ArrowRight size={24} /></div>
        <div class="boundary-map__remote"><Sparkles size={20} /><strong>Model Provider</strong><p>Receives what the configured coding agent sends according to that Provider's product and data policy.</p></div>
      </div>
      <div class="doc-callout doc-callout--info"><Info size={18} /><div><strong>Model requests follow your configuration.</strong><p>Coding agents may send prompts and code context to remote model services under those services' data policies and fees. For a supported local model, check the selected tool and model configuration to confirm where requests are processed.</p></div></div>
    </section>

    <section class="doc-section" id="permissions">
      <div class="doc-section__eyebrow">Permissions</div>
      <h2>Authorize resources and choose a trusted network</h2>
      <p>Project Resources determine which directory roots the runtime authorizes. Full Git resources authorize their canonical repository root; partial or unrelated resources retain their exact approved roots.</p>
      <h3>Worktrees are optional workflow isolation</h3>
      <p>An Issue can carry a worktree hint, asking the Agent to create one and publish its name and path in a comment. You and the Agent manage that worktree explicitly. A Git worktree separates development state; it is not an operating-system security sandbox.</p>
      <h3>Password-protected access on a trusted LAN or VPN</h3>
      <p>By default, the service listens only on loopback. The local entry remains available without an access password. Set a password and explicitly enable Remote access in local settings to add a separate listener on a specific LAN IPv4 address, using the same port number. External access requires password login; remote clients cannot change the access switch or password.</p>
      <p>Use remote access only on a trusted LAN or VPN. HTTP traffic is unencrypted, so do not expose the service directly to the public internet. The password protects access to the same local workspace; it does not provide multi-user permission isolation.</p>
      <h3>Skills run as you</h3>
      <p>Skill scripts and hooks execute with the current user's permissions. Digest validation helps ensure the installed package is the selected version, but it does not make untrusted code safe.</p>
    </section>

    <section class="doc-section" id="product-boundaries">
      <div class="doc-section__eyebrow">Product boundaries</div>
      <h2>Current workspace and release status</h2>
      <div class="boundary-list"><div><Check size={16} /><p><strong>Current workspace</strong><span>Browser and macOS workbench for a single local user. Codex and OpenCode integrations, controlled Claude Code file and task tools, and an extensible Provider API.</span></p></div><div><X size={16} /><p><strong>Outside the current scope</strong><span>Windows/Linux desktop packages, a Cursor adapter, cloud sync, shared team databases, online backup, and automatic corruption repair.</span></p></div><div><Info size={16} /><p><strong>Development preview</strong><span>Build from source for the current preview. macOS packaging generates separate arm64 and x64 ZIP and drag-to-Applications DMG artifacts that are ad-hoc signed but not notarized.</span></p></div></div>
      <p>Teamva is an independently developed local workspace. The Super-agent research referenced on the product page is by DeepSeek and provides background on coordination.</p>
    </section>

    {/if}
    <nav class="doc-pagination" aria-label={zh ? "文档翻页" : "Documentation pagination"}><a href="#overview" onclick={(event) => { event.preventDefault(); goTo("overview"); }}><ArrowLeft size={16} /><span><small>{zh ? "返回" : "Back to"}</small>{zh ? "概览" : "Overview"}</span></a><a href={languageHref("../#workspace")}><span><small>{zh ? "接下来" : "Next"}</small>{zh ? "产品导览" : "Product tour"}</span><ArrowRight size={16} /></a></nav>
  </article>

  <aside class="docs-toc" aria-label={zh ? "本页内容" : "On this page"}>
    <span>{zh ? "本页内容" : "On this page"}</span>
    {#each navGroups as group}
      <div class="docs-toc__group">
        <strong>{group.title}</strong>
        {#each group.items as item}<a class:is-active={activeId === item.id} href={`#${item.id}`} onclick={(event) => { event.preventDefault(); goTo(item.id); }}>{item.title}</a>{/each}
      </div>
    {/each}
    <a class="docs-toc__top" href="#overview" onclick={(event) => { event.preventDefault(); goTo("overview"); }}>{zh ? "回到顶部" : "Back to top"}<ChevronDown size={13} /></a>
  </aside>
</main>

<div class="docs-footer"><SiteFooter homeHref="../" docsHref="./" /></div>
