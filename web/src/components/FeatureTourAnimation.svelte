<script lang="ts">
  // Everything in this scene is an illustration, never a projection of project data.
  type Tour = "overview" | "issues" | "conversations" | "resources" | "memories" | "schedules" | "changes";
  interface Props {
    tour: Tour;
    title: string;
    steps: readonly [string, string, string];
    paused?: boolean;
    allowMotion?: boolean;
  }
  let { tour, title, steps, paused = false, allowMotion = false }: Props = $props();

  const icons = {
    overview: "M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z",
    issues: "M9 5h11v15H4V5h2 M9 3h6v4H9z M8 12h8 M8 16h5",
    conversations: "M4 4h16v12H9l-5 4z M8 8h8 M8 12h5",
    resources: "M3 6h7l2 3h9v11H3z M3 6V4h7l2 2",
    memories: "M5 3h14v18H5z M8 8h8 M8 12h8 M8 16h5",
    schedules: "M7 3v4 M17 3v4 M3 10h18 M3 5h18v16H3z M7 14h3 M14 14h3",
    changes: "M6 3v12a4 4 0 0 0 4 4h7 M14 15l4 4-4 4 M6 3l-3 3 M6 3l3 3 M17 3v7",
    plus: "M12 5v14 M5 12h14",
    check: "M5 12l4 4L19 6",
    arrow: "M5 12h14 M14 7l5 5-5 5",
    send: "M3 3l18 9-18 9 4-9z M7 12h14",
    branch: "M6 3v18 M6 12h5a6 6 0 0 0 6-6V3 M3 3h6 M3 21h6 M14 3h6",
    pause: "M8 5v14 M16 5v14",
    link: "M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-2 2 M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l2-2",
    clock: "M12 8v5l3 2 M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0",
    lock: "M7 10V7a5 5 0 0 1 10 0v3 M5 10h14v11H5z M12 14v3",
    eye: "M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12z M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0",
    file: "M5 3h9l5 5v13H5z M14 3v6h5",
  };

  // Independent horizontal/vertical easing draws a gently curved cursor path.
  // Three deliberate stops share the same 14 / 44 / 74% click choreography.
  const cursorPoints: Record<Tour, readonly [number, number, number, number, number, number]> = {
    overview: [226, 100, 228, 244, 511, 190],
    issues: [625, 83, 331, 299, 604, 151],
    conversations: [254, 275, 254, 207, 622, 360],
    resources: [216, 162, 176, 282, 630, 282],
    memories: [198, 185, 357, 304, 569, 181],
    schedules: [165, 147, 153, 237, 279, 306],
    changes: [158, 161, 487, 209, 604, 317],
  };
  const pointerStyle = $derived(cursorPoints[tour].map((value, index) =>
    `--${index % 2 ? "y" : "x"}${Math.floor(index / 2) + 1}:${value}px`).join(";"));
</script>

{#snippet glyph(kind: keyof typeof icons, x: number, y: number, size = 20, tone = "secondary")}
  <svg {x} {y} width={size} height={size} viewBox="0 0 24 24" class="glyph {tone}">
    <path d={icons[kind]} />
  </svg>
{/snippet}

{#snippet label(text: string, x: number, y: number, width: number, height = 36, tone = "secondary")}
  <foreignObject {x} {y} {width} {height}>
    <div class="scene-label {tone}"><span>{text}</span></div>
  </foreignObject>
{/snippet}

{#snippet lines(x: number, y: number, width: number)}
  <g class="skeleton">
    <rect {x} {y} {width} height="6" rx="3" />
    <rect {x} y={y + 14} width={width * 0.73} height="6" rx="3" />
  </g>
{/snippet}

{#snippet conversationPrefix(x: number, y: number)}
  <g data-tour-message="prefix-1">
    <rect class="inset" x={x + 80} y={y + 32} width="150" height="16" rx="4" />
    <rect class="skeleton" x={x + 91} y={y + 38} width="115" height="4" rx="2" />
  </g>
  <g data-tour-message="prefix-2">
    <rect class="card" x={x + 14} y={y + 54} width="152" height="16" rx="4" />
    <rect class="skeleton" x={x + 25} y={y + 60} width="117" height="4" rx="2" />
  </g>
{/snippet}

{#snippet conversationTail(x: number, y: number)}
  <g data-tour-message="original-3">
    <rect class="accent-wash" x={x + 80} y={y + 76} width="150" height="16" rx="4" />
    <rect class="skeleton" x={x + 91} y={y + 82} width="97" height="4" rx="2" />
  </g>
  <g data-tour-message="original-4">
    <rect class="card" x={x + 14} y={y + 98} width="152" height="16" rx="4" />
    <rect class="skeleton" x={x + 25} y={y + 104} width="119" height="4" rx="2" />
  </g>
{/snippet}

<div
  class="tour-visual"
  data-feature-tour-animation={tour}
  data-paused={paused ? "true" : "false"}
  data-allow-motion={allowMotion ? "true" : undefined}
  aria-hidden="true"
>
  {#key tour}
    <svg class="stage" viewBox="0 0 720 390" fill="none" style={pointerStyle}>
      <g class="scene-cycle">
        <rect class="accent-wash" x="32" y="18" width="30" height="30" rx="9" />
        {@render glyph(tour, 39, 25, 16, "accent")}
        {@render label(title, 74, 20, 490, 26, "scene-title")}
        {#each [0, 1, 2] as index}
          <rect class="skeleton" x={594 + index * 28} y="29" width="22" height="5" rx="2.5" />
          <rect class="accent-fill after-{['one', 'two', 'three'][index]}" x={594 + index * 28} y="29" width="22" height="5" rx="2.5" />
        {/each}

        {#if tour === "overview"}
          <g data-tour-scene="overview">
            <rect class="panel" x="40" y="69" width="326" height="277" rx="15" />
            <rect class="inset" x="58" y="85" width="288" height="34" rx="8" />
            {@render glyph("overview", 70, 94, 15)}
            {@render label(steps[0], 94, 86, 212, 30)}
            <path class="hairline" d="m325 98 4 4 4-4" />
            <rect class="selection after-one" x="58" y="85" width="288" height="34" rx="8" />
            <g data-tour-demo="project-progress">
              <rect class="inset" x="58" y="136" width="136" height="62" rx="10" />
              <rect class="inset" x="207" y="136" width="139" height="62" rx="10" />
              <text class="metric" x="74" y="166">08</text>
              <text class="metric" x="224" y="166">03</text>
              <rect class="skeleton" x="75" y="180" width="47" height="4" rx="2" />
              <rect class="skeleton" x="224" y="180" width="62" height="4" rx="2" />
              <rect class="card" x="58" y="212" width="288" height="116" rx="10" />
              <rect class="selection after-two" x="58" y="212" width="288" height="116" rx="10" />
              {@render label(steps[1], 75, 224, 203, 35)}
              <text class="number accent" x="303" y="245" text-anchor="middle">72%</text>
              <rect class="skeleton" x="76" y="273" width="252" height="7" rx="3.5" />
              <g transform="translate(76 273)">
                <rect class="accent-fill progress-fill" width="181" height="7" rx="3.5" />
              </g>
              <circle class="success-fill" cx="81" cy="306" r="3" />
              <rect class="skeleton" x="92" y="303" width="108" height="5" rx="2.5" />
              {@render glyph("arrow", 309, 296, 18)}
            </g>
            <g class="before-two">
              <rect class="ghost-panel" x="389" y="102" width="291" height="215" rx="14" />
              {@render lines(413, 128, 158)}
              {@render lines(413, 181, 217)}
              {@render lines(413, 234, 185)}
            </g>
            <g class="after-two slide-right" data-tour-demo="task-summary">
              <rect class="panel" x="389" y="82" width="291" height="252" rx="14" />
              {@render label(steps[2], 410, 96, 241, 38, "strong")}
              <path class="divider" d="M409 143h250" />
              <rect class="inset" x="406" y="158" width="256" height="60" rx="9" />
              <circle class="status-circle" cx="425" cy="179" r="5" />
              <text class="code" x="440" y="184">#128</text>
              {@render lines(440, 195, 164)}
              <rect class="selection after-three" x="406" y="158" width="256" height="60" rx="9" />
              <g class="after-three" data-tour-demo="summary-expanded">
                <rect class="accent-wash" x="406" y="234" width="256" height="81" rx="9" />
                <circle class="accent-fill" cx="426" cy="255" r="9" />
                <text class="avatar-letter" x="426" y="259" text-anchor="middle">L</text>
                <text class="code" x="443" y="260">@Lumi</text>
                {@render lines(424, 277, 193)}
              </g>
            </g>
          </g>
        {:else if tour === "issues"}
          <g data-tour-scene="issues">
            <rect class="panel" x="40" y="64" width="640" height="284" rx="15" />
            <text class="code muted" x="61" y="88">#127</text>
            <path class="divider" d="M57 103h606 M379 119v211" />
            <rect class="accent-wash" x="606" y="68" width="38" height="29" rx="7" />
            {@render glyph("plus", 616, 74, 18, "accent")}
            <g class="before-one">
              {@render lines(65, 134, 241)}
              {@render lines(65, 194, 196)}
              {@render lines(65, 254, 225)}
            </g>
            <g class="after-one" data-tour-demo="issue-compose">
              <rect class="card" x="58" y="119" width="303" height="211" rx="11" />
              <text class="code muted" x="75" y="145">#128</text>
              {@render label(steps[0], 75, 158, 262, 42, "strong")}
              <g class="typing-one">
                <rect class="accent-wash" x="75" y="216" width="81" height="27" rx="7" />
                <text class="code accent" x="86" y="234">@Lumi</text>
                {@render lines(169, 221, 143)}
              </g>
              <path class="divider" d="M75 273h267" />
              <rect class="accent-wash" x="313" y="284" width="32" height="30" rx="7" />
              {@render glyph("send", 321, 291, 16, "accent")}
            </g>
            <g class="after-two slide-right" data-tour-demo="agent-reply">
              <circle class="accent-fill" cx="413" cy="149" r="14" />
              <text class="avatar-letter" x="413" y="154" text-anchor="middle">L</text>
              <text class="code" x="436" y="153">@Lumi</text>
              <rect class="card" x="399" y="178" width="263" height="103" rx="11" />
              {@render label(steps[1], 415, 190, 229, 40, "strong")}
              {@render lines(415, 242, 199)}
              <rect class="inset before-three" x="582" y="135" width="62" height="30" rx="15" />
              <circle class="status-circle before-three" cx="604" cy="150" r="6" />
              <g class="after-three" data-tour-demo="issue-completed">
                <rect class="success-wash" x="582" y="135" width="62" height="30" rx="15" />
                {@render glyph("check", 595, 140, 20, "success")}
                <rect class="success-wash" x="399" y="295" width="263" height="34" rx="8" />
                {@render glyph("check", 409, 304, 16, "success")}
                {@render label(steps[2], 436, 297, 210, 30, "success")}
              </g>
            </g>
          </g>
        {:else if tour === "conversations"}
          <g data-tour-scene="conversations">
            <path class="graph-grid" d="M24 84h672 M24 148h672 M24 212h672 M24 276h672 M24 340h672 M84 65v298 M148 65v298 M212 65v298 M276 65v298 M340 65v298 M404 65v298 M468 65v298 M532 65v298 M596 65v298 M660 65v298" />
            {@render label(steps[0], 38, 91, 244, 40, "strong")}
            <g data-tour-demo="parent-conversation">
              <rect class="panel" data-tour-node="parent" x="38" y="145" width="244" height="148" rx="12" />
              <text class="small-code" x="52" y="164">@Lumi</text>
              <g class="after-two" data-tour-demo="frozen-parent">
                <rect class="inset" x="242" y="151" width="26" height="18" rx="5" />
                {@render glyph("pause", 249, 154, 12)}
              </g>
              <path class="divider" d="M52 171h216" />
              {@render conversationPrefix(38, 145)}
              <!-- Only the two shared messages survive in the frozen parent.
                   The same four-message history continues in branch-one. -->
              <g class="before-two" data-tour-demo="parent-continuation">
                <g class="after-one">{@render conversationTail(38, 145)}</g>
              </g>
              <g data-tour-composer="parent">
                <rect class="inset" x="52" y="267" width="216" height="18" rx="5" />
                <rect class="skeleton" x="62" y="274" width="125" height="4" rx="2" />
                <g class="before-two">
                  <rect class="accent-wash" data-tour-action="reply" data-tour-send="parent" x="242" y="268" width="24" height="16" rx="4" />
                  {@render glyph("send", 248, 270, 12, "accent")}
                </g>
                <g class="after-two">{@render glyph("lock", 248, 270, 12)}</g>
              </g>
              <g class="before-two"><g class="after-one">
                <rect class="accent-wash" data-tour-action="fork" x="242" y="197" width="24" height="20" rx="5" />
                {@render glyph("branch", 248, 201, 12, "accent")}
              </g></g>
            </g>
            <g class="after-two" data-tour-demo="frozen-prefix-caption">
              <rect class="accent-wash" x="38" y="307" width="244" height="46" rx="9" />
              {@render glyph("pause", 52, 322, 16, "accent")}
              {@render label(steps[1], 80, 312, 185, 36, "strong")}
            </g>
            <g class="after-two" data-tour-demo="branch-connections">
              <path class="connector branch-draw" d="M282 207h43c26 0 26-65 52-65h29 M325 207c26 0 26 101 52 101h29" />
              <circle class="accent-fill" cx="325" cy="207" r="4" />
            </g>
            <g class="after-two slide-right" data-tour-demo="branch-one">
              <rect class="panel" data-tour-node="continuation" x="406" y="68" width="244" height="148" rx="12" />
              {@render glyph("branch", 420, 77, 12, "accent")}
              <text class="small-code" x="441" y="87">@Lumi</text>
              <text class="small-code muted" x="618" y="87">01</text>
              <path class="divider" d="M420 94h216" />
              {@render conversationPrefix(406, 68)}
              {@render conversationTail(406, 68)}
              <g data-tour-composer="continuation">
                <rect class="inset" x="420" y="190" width="216" height="18" rx="5" />
                <rect class="skeleton" x="430" y="197" width="125" height="4" rx="2" />
                <rect class="accent-wash" data-tour-send="continuation" x="610" y="191" width="24" height="16" rx="4" />
                {@render glyph("send", 616, 193, 12, "accent")}
              </g>
            </g>
            <g class="after-two slide-right" data-tour-demo="branch-two">
              <rect class="panel" data-tour-node="new-route" x="406" y="234" width="244" height="148" rx="12" />
              <rect class="selection after-three" x="406" y="234" width="244" height="148" rx="12" />
              {@render glyph("branch", 420, 243, 12, "accent")}
              <text class="small-code" x="441" y="253">@Lumi</text>
              <text class="small-code muted" x="618" y="253">02</text>
              <path class="divider" d="M420 260h216" />
              {@render conversationPrefix(406, 234)}
              <g class="after-three" data-tour-demo="branch-reply">
                <g data-tour-message="new-route-3">
                  <rect class="accent-wash" x="458" y="310" width="178" height="39" rx="6" />
                  {@render label(steps[2], 469, 312, 156, 35, "strong")}
                </g>
              </g>
              <g data-tour-composer="new-route">
                <rect class="inset" x="420" y="356" width="216" height="18" rx="5" />
                <rect class="skeleton" x="430" y="363" width="125" height="4" rx="2" />
                <rect class="accent-wash" data-tour-action="new-route-send" data-tour-send="new-route" x="610" y="357" width="24" height="16" rx="4" />
                {@render glyph("send", 616, 359, 12, "accent")}
              </g>
            </g>
          </g>
        {:else if tour === "resources"}
          <g data-tour-scene="resources">
            <rect class="panel" x="40" y="76" width="282" height="273" rx="14" />
            {@render label(steps[0], 60, 87, 242, 40, "strong")}
            <path class="divider" d="M58 136h246" />
            <rect class="inset" x="57" y="146" width="248" height="45" rx="8" />
            <rect class="selection after-one" x="57" y="146" width="248" height="45" rx="8" />
            {@render glyph("resources", 71, 159, 20, "accent")}
            <text class="code" x="104" y="174">workspace/</text>
            <path class="tree-line" d="M82 201v41h12 M82 216h12" />
            <text class="small-code muted" x="104" y="220">src/</text>
            <text class="small-code muted" x="104" y="245">docs/</text>
            <path class="divider" d="M58 260h246" />
            <rect class="inset" x="57" y="273" width="248" height="53" rx="8" />
            <rect class="selection after-two" x="57" y="273" width="248" height="53" rx="8" />
            {@render glyph("link", 72, 289, 18)}
            {@render label(steps[1], 105, 280, 182, 38)}
            <path class="connector after-one" d="M322 168h26" />
            <g data-tour-demo="resource-list">
              <rect class="ghost-panel" x="348" y="76" width="332" height="273" rx="14" />
              {@render label(steps[2], 367, 88, 289, 39, "strong")}
              <g class="after-one resource-in" data-tour-demo="directory-resource">
                <rect class="panel" x="366" y="142" width="296" height="69" rx="10" />
                <rect class="accent-wash" x="380" y="157" width="37" height="37" rx="9" />
                {@render glyph("resources", 389, 166, 19, "accent")}
                <text class="code" x="432" y="172">workspace/</text>
                <text class="small-code muted" x="432" y="193">src/ · docs/</text>
                {@render glyph("check", 628, 167, 16, "success")}
              </g>
              <g class="after-two" data-tour-demo="url-compose"><g class="before-three">
                <rect class="card" x="366" y="239" width="296" height="83" rx="10" />
                <text class="small-code muted" x="382" y="269">https://example.com</text>
                <path class="divider" d="M381 281h221" />
                <rect class="accent-wash" x="614" y="265" width="32" height="32" rx="7" />
                {@render glyph("plus", 622, 273, 16, "accent")}
              </g></g>
              <g class="after-three resource-in" data-tour-demo="url-resource">
                <rect class="panel" x="366" y="229" width="296" height="91" rx="10" />
                <rect class="inset" x="380" y="244" width="37" height="37" rx="9" />
                {@render glyph("link", 390, 254, 18)}
                <text class="small-code" x="432" y="260">example.com</text>
                <rect class="skeleton" x="432" y="273" width="154" height="5" rx="2.5" />
                <text class="small-code muted" x="382" y="304">https://example.com</text>
                {@render glyph("check", 628, 253, 16, "success")}
              </g>
            </g>
          </g>
        {:else if tour === "memories"}
          <g data-tour-scene="memories">
            <rect class="panel" x="43" y="76" width="388" height="273" rx="14" />
            <path class="notebook-rule" d="M76 91v242" />
            {#each [116, 171, 226, 281] as y}
              <circle class="binding" cx="44" cy={y} r="4" />
            {/each}
            {@render label(steps[0], 94, 91, 312, 42, "strong")}
            <path class="divider" d="M94 146h312" />
            <g data-tour-demo="memory-editor">
              <rect class="inset" x="94" y="163" width="312" height="96" rx="9" />
              <rect class="selection after-one" x="94" y="163" width="312" height="96" rx="9" />
              <g class="typing-one">
                <text class="code" x="111" y="189">pnpm test</text>
                <text class="small-code muted" x="111" y="215">*.test.ts</text>
                <rect class="skeleton" x="111" y="235" width="230" height="5" rx="2.5" />
              </g>
            </g>
            <rect class="accent-wash" x="264" y="284" width="142" height="39" rx="8" />
            {@render label(steps[1], 274, 287, 101, 32, "accent")}
            <g class="before-two">{@render glyph("arrow", 379, 296, 16, "accent")}</g>
            <g class="after-two" data-tour-demo="saved-memory">{@render glyph("check", 379, 296, 16, "success")}</g>
            <path class="connector memory-draw after-two" d="M431 211h31" />
            <g data-tour-demo="memory-agent">
              <rect class="panel" x="462" y="114" width="215" height="211" rx="13" />
              <circle class="accent-fill" cx="489" cy="141" r="11" />
              <text class="avatar-letter" x="489" y="145" text-anchor="middle">L</text>
              <text class="code" x="509" y="146">@Lumi</text>
              <path class="divider" d="M478 161h183" />
              <rect class="inset" x="478" y="174" width="183" height="33" rx="7" />
              <rect class="skeleton" x="491" y="188" width="121" height="5" rx="2.5" />
              {@render glyph("send", 631, 183, 14)}
              <g class="after-three" data-tour-demo="reused-memory">
                <rect class="accent-wash" x="478" y="220" width="183" height="88" rx="9" />
                {@render label(steps[2], 490, 228, 160, 38, "strong")}
                <text class="small-code accent" x="491" y="290">pnpm test</text>
                {@render glyph("check", 629, 278, 16, "success")}
              </g>
            </g>
          </g>
        {:else if tour === "schedules"}
          <g data-tour-scene="schedules">
            <rect class="panel" x="40" y="76" width="304" height="274" rx="15" />
            {@render label(steps[0], 61, 87, 261, 39, "strong")}
            <rect class="inset" x="60" y="132" width="264" height="49" rx="9" />
            <rect class="selection after-one" x="60" y="132" width="264" height="49" rx="9" />
            {@render glyph("clock", 76, 147, 19)}
            <g data-tour-demo="time-picker">
              <text class="time-code before-one" x="113" y="164">09:00</text>
              <text class="time-code after-one" x="113" y="164">09:30</text>
            </g>
            <path class="hairline" d="m298 153 4 4 4-4" />
            {@render label(steps[1], 61, 188, 261, 30)}
            <g data-tour-demo="weekday-picker">
              {#each [1, 2, 3, 4, 5, 6, 7] as day}
                <rect class="inset" x={60 + (day - 1) * 38} y="220" width="34" height="34" rx="8" />
                <text class="small-code" x={77 + (day - 1) * 38} y="242" text-anchor="middle">{day}</text>
              {/each}
              <g class="after-two">
                <rect class="selection" x="136" y="220" width="34" height="34" rx="8" />
                <rect class="accent-wash" x="139" y="223" width="28" height="28" rx="6" />
                <text class="small-code accent" x="153" y="242" text-anchor="middle">3</text>
              </g>
            </g>
            <path class="divider" d="M60 274h264" />
            <text class="small-code muted before-one" x="63" y="309">09:00</text>
            <text class="small-code muted after-one" x="63" y="309">09:30</text>
            <rect class="accent-wash" x="250" y="290" width="73" height="34" rx="8" />
            <g class="before-three">{@render glyph("arrow", 277, 299, 17, "accent")}</g>
            <g class="after-three">{@render glyph("check", 277, 299, 17, "success")}</g>
            <g data-tour-demo="schedule-clock" transform="translate(516 146)">
              <circle class="clock-face" r="49" />
              {#each [0, 90, 180, 270] as angle}
                <path class="clock-tick" d="M0-37v5" transform="rotate({angle})" />
              {/each}
              <path class="clock-hour" d="M0 0h-21" />
              <g class="clock-hand"><path class="clock-minute" d="M0 0v-29" /></g>
              <circle class="accent-fill" r="3.5" />
            </g>
            <g class="after-three" data-tour-demo="scheduled-issue">
              <path class="connector trigger-draw" d="M516 195v30 M344 307h42q18 0 18-18v-46q0-18 18-18h29" />
              <rect class="panel" x="408" y="227" width="270" height="113" rx="12" />
              <rect class="accent-wash" x="424" y="243" width="31" height="31" rx="8" />
              {@render glyph("issues", 432, 251, 15, "accent")}
              <text class="code" x="467" y="264">#129</text>
              <text class="small-code muted" x="581" y="264">09:30</text>
              {@render label(steps[2], 425, 284, 233, 40, "strong")}
            </g>
          </g>
        {:else if tour === "changes"}
          <g data-tour-scene="changes">
            <rect class="panel" x="40" y="76" width="640" height="273" rx="14" />
            <path class="divider" d="M253 77v271 M254 123h425" />
            {@render label(steps[0], 57, 87, 180, 38, "strong")}
            <text class="small-code muted" x="63" y="145">src/</text>
            <path class="tree-line" d="M70 153v41h10 M70 167h10 M70 205v47h10" />
            <rect class="accent-wash after-one" x="83" y="152" width="156" height="29" rx="6" />
            {@render glyph("file", 89, 159, 14)}
            <text class="small-code" x="111" y="171">App.svelte</text>
            <text class="small-code muted" x="89" y="202">lib/</text>
            {@render glyph("file", 89, 237, 14)}
            <text class="small-code muted" x="111" y="249">theme.ts</text>
            <g class="before-one">
              {@render lines(277, 146, 217)}
              {@render lines(277, 212, 285)}
            </g>
            <g class="after-one" data-tour-demo="read-only-diff">
              {@render glyph("file", 271, 93, 16)}
              <text class="code" x="296" y="106">App.svelte</text>
              <text class="small-code success" x="570" y="106">+04</text>
              <text class="small-code danger" x="620" y="106">−02</text>
              {@render label(steps[1], 274, 130, 383, 36)}
              <rect class="diff-removed" x="264" y="174" width="405" height="30" rx="3" />
              <rect class="diff-added" x="264" y="205" width="405" height="30" rx="3" />
              <text class="small-code muted" x="275" y="194">12</text>
              <text class="small-code danger" x="306" y="194">− const size = 8;</text>
              <text class="small-code muted" x="275" y="225">12</text>
              <text class="small-code success" x="306" y="225">+ const size = 12;</text>
              <text class="small-code muted" x="275" y="257">13</text>
              <text class="small-code" x="322" y="257">{'<Panel size={size} />'}</text>
              <rect class="selection after-two" x="264" y="205" width="405" height="30" rx="3" />
              <path class="divider" d="M271 284h390" />
              <rect class="inset" x="582" y="300" width="65" height="33" rx="8" />
              {@render glyph("eye", 603, 309, 17)}
              <g class="after-three" data-tour-demo="review-confirmed">
                {@render glyph("lock", 275, 310, 16, "success")}
                {@render label(steps[2], 301, 299, 267, 36, "success")}
              </g>
            </g>
          </g>
        {/if}

        <g class="cursor-presence" data-tour-cursor>
          <g class="cursor-horizontal">
            <g class="cursor-vertical">
              {#each ["one", "two", "three"] as click}
                <circle class="click-wave click-{click}" data-tour-click={click} r="14" />
              {/each}
              <g class="cursor-press">
                <path class="cursor-outline" d="M0 0 3 24l6-6 5 11 5-2-5-11 9-1Z" />
                <path class="cursor-inner" d="m3 5 2 14 4-5 6 12 1-.5-6-12 7-1Z" />
              </g>
            </g>
          </g>
        </g>
      </g>
    </svg>
  {/key}
</div>

<style>
  .tour-visual {
    --tour-duration: 11.2s;
    width: 100%;
    min-width: 0;
    overflow: hidden;
    isolation: isolate;
    background: var(--color-canvas);
    color: var(--color-text);
    user-select: none;
    pointer-events: none;
  }
  .stage { display: block; width: 100%; height: auto; aspect-ratio: 720 / 390; overflow: hidden; }
  .panel, .card { fill: var(--color-surface); stroke: var(--color-border); stroke-width: 1; }
  .inset { fill: var(--color-surface-subtle); }
  .ghost-panel { fill: var(--color-surface); fill-opacity: .55; stroke: var(--color-border); stroke-dasharray: 4 5; }
  .selection { fill: none; stroke: var(--color-accent); stroke-width: 1.5; }
  .accent-wash { fill: var(--color-accent-subtle); }
  .accent-fill { fill: var(--color-control); }
  .success-fill { fill: var(--color-success); }
  .success-wash { fill: color-mix(in srgb, var(--color-success) 12%, var(--color-surface)); }
  .skeleton { fill: var(--color-border); opacity: .65; }
  .divider, .tree-line, .hairline { stroke: var(--color-border); stroke-width: 1; stroke-linecap: round; }
  .tree-line { stroke-linejoin: round; }
  .hairline { stroke: var(--color-secondary); }
  .connector { stroke: var(--color-accent); stroke-width: 1.6; stroke-linecap: round; fill: none; }
  .glyph { stroke: currentColor; stroke-width: 1.7; stroke-linecap: round; stroke-linejoin: round; overflow: visible; }
  .glyph path { fill: none; }
  .scene-label {
    display: flex;
    align-items: center;
    width: 100%;
    height: 100%;
    color: var(--color-secondary);
    font: 13.5px / 1.35 var(--font-sans);
    overflow: hidden;
  }
  .scene-label span { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; overflow: hidden; overflow-wrap: anywhere; }
  .scene-title { color: var(--color-text); font-size: 16px; font-weight: 600; }
  .scene-title span { -webkit-line-clamp: 1; line-clamp: 1; }
  .strong { color: var(--color-text); font-weight: 500; }
  .secondary { color: var(--color-secondary); }
  .muted { color: var(--color-muted); fill: var(--color-muted); }
  .accent { color: var(--color-accent-text); fill: var(--color-accent-text); }
  .success { color: var(--color-success); fill: var(--color-success); }
  .danger { color: var(--color-danger); fill: var(--color-danger); }
  .code, .small-code, .time-code, .number, .metric { fill: currentColor; color: var(--color-text); font-family: var(--font-mono); }
  .code { font-size: 13px; }
  .small-code { font-size: 11.5px; }
  .number { font-size: 13px; }
  .metric { font-size: 25px; letter-spacing: -1px; }
  .time-code { font-size: 24px; letter-spacing: 2px; }
  text.muted { color: var(--color-muted); }
  text.accent { color: var(--color-accent-text); }
  text.success { color: var(--color-success); }
  text.danger { color: var(--color-danger); }
  .avatar-letter { fill: var(--color-control-text); font: 600 11px var(--font-sans); }
  .status-circle { stroke: var(--color-muted); stroke-width: 1.5; }
  .graph-grid { stroke: var(--color-border); stroke-width: .6; opacity: .3; }
  .notebook-rule { stroke: var(--color-accent); opacity: .25; }
  .binding { fill: var(--color-canvas); stroke: var(--color-border); }
  .clock-face { fill: var(--color-surface); stroke: var(--color-border); stroke-width: 1.5; }
  .clock-tick { stroke: var(--color-muted); stroke-width: 1.5; stroke-linecap: round; }
  .clock-hour { stroke: var(--color-text); stroke-width: 3; stroke-linecap: round; }
  .clock-minute { stroke: var(--color-accent); stroke-width: 2.5; stroke-linecap: round; }
  .diff-removed { fill: color-mix(in srgb, var(--color-danger) 11%, var(--color-surface)); }
  .diff-added { fill: color-mix(in srgb, var(--color-success) 12%, var(--color-surface)); }
  .cursor-outline { fill: var(--color-surface); stroke: var(--color-text); stroke-width: 1.8; stroke-linejoin: round; }
  .cursor-inner { fill: var(--color-text); }
  .click-wave { fill: none; stroke: var(--color-accent); stroke-width: 2; opacity: 0; transform-origin: 0 0; }

  /* Base values are the completed, useful still illustration. Motion alone
     introduces the earlier states, so reduced motion does not need a JS fork. */
  .scene-cycle { animation-name: scene-cycle; }
  .after-one { animation-name: reveal-one; }
  .after-two { animation-name: reveal-two; }
  .after-three { animation-name: reveal-three; }
  .before-one { opacity: 0; animation-name: before-one; }
  .before-two { opacity: 0; animation-name: before-two; }
  .before-three { opacity: 0; animation-name: before-three; }
  .slide-right.after-two { animation-name: slide-two; }
  .resource-in.after-one { animation-name: resource-one; }
  .resource-in.after-three { animation-name: resource-three; }
  .typing-one { animation-name: typing-one; }
  .progress-fill { animation-name: progress-fill; transform-origin: 0 0; }
  .branch-draw { stroke-dasharray: 270; animation-name: branch-draw; }
  .memory-draw { stroke-dasharray: 32; animation-name: memory-draw; }
  .trigger-draw { stroke-dasharray: 240; animation-name: trigger-draw; }
  .clock-hand { transform: rotate(180deg); transform-origin: 0 0; animation-name: clock-hand; }
  .cursor-presence { animation-name: cursor-presence; }
  .cursor-horizontal { transform: translateX(var(--x3)); animation-name: cursor-horizontal; }
  .cursor-vertical { transform: translateY(var(--y3)); animation-name: cursor-vertical; }
  .cursor-press { transform-origin: 0 0; animation-name: cursor-press; }
  .click-one { animation-name: click-one; }
  .click-two { animation-name: click-two; }
  .click-three { animation-name: click-three; }
  .scene-cycle, .after-one, .after-two, .after-three, .before-one, .before-two, .before-three,
  .typing-one, .progress-fill, .branch-draw, .memory-draw, .trigger-draw, .clock-hand,
  .cursor-presence, .cursor-horizontal, .cursor-vertical, .cursor-press, .click-wave {
    animation-duration: var(--tour-duration);
    animation-iteration-count: infinite;
    animation-timing-function: cubic-bezier(.22, .7, .24, 1);
  }
  .cursor-horizontal { animation-timing-function: cubic-bezier(.3, 0, .2, 1); }
  .cursor-vertical { animation-timing-function: cubic-bezier(.5, 0, .15, 1); }
  /* These opt-in instructional clips are functional content, not decorative
     page motion. Keep their real timeline despite the app's global 0.01ms /
     one-iteration reduced-motion reset; never override the playback state. */
  .tour-visual[data-allow-motion="true"] * {
    animation-duration: var(--tour-duration) !important;
    animation-iteration-count: infinite !important;
  }
  .tour-visual[data-paused="true"], .tour-visual[data-paused="true"] * { animation-play-state: paused !important; }

  /* UI responses follow a cursor press, not an arbitrary card carousel.
     At 97–98% the whole illustration is invisible while every state resets. */
  @keyframes scene-cycle { 0%, 92%, 100% { opacity: 1; } 97%, 98% { opacity: 0; } }
  @keyframes reveal-one {
    0%, 15%, 98%, 100% { opacity: 0; transform: translateY(8px); }
    19%, 97% { opacity: 1; transform: translateY(0); }
  }
  @keyframes reveal-two {
    0%, 45%, 98%, 100% { opacity: 0; transform: translateY(8px); }
    50%, 97% { opacity: 1; transform: translateY(0); }
  }
  @keyframes reveal-three {
    0%, 75%, 98%, 100% { opacity: 0; transform: translateY(8px); }
    80%, 97% { opacity: 1; transform: translateY(0); }
  }
  @keyframes before-one { 0%, 15%, 98%, 100% { opacity: 1; } 19%, 97% { opacity: 0; } }
  @keyframes before-two { 0%, 45%, 98%, 100% { opacity: 1; } 50%, 97% { opacity: 0; } }
  @keyframes before-three { 0%, 75%, 98%, 100% { opacity: 1; } 80%, 97% { opacity: 0; } }
  @keyframes slide-two {
    0%, 45%, 98%, 100% { opacity: 0; transform: translateX(24px); }
    51%, 97% { opacity: 1; transform: translateX(0); }
  }
  @keyframes resource-one {
    0%, 15%, 98%, 100% { opacity: 0; transform: translate(-46px, 8px); }
    23%, 97% { opacity: 1; transform: translate(0, 0); }
  }
  @keyframes resource-three {
    0%, 75%, 98%, 100% { opacity: 0; transform: translateY(20px); }
    83%, 97% { opacity: 1; transform: translateY(0); }
  }
  @keyframes typing-one {
    0%, 17%, 98%, 100% { clip-path: inset(0 100% 0 0); }
    31%, 97% { clip-path: inset(0 0 0 0); }
  }
  @keyframes progress-fill { 0%, 15%, 98%, 100% { transform: scaleX(.44); } 33%, 97% { transform: scaleX(1); } }
  @keyframes branch-draw { 0%, 45%, 98%, 100% { stroke-dashoffset: 270; } 55%, 97% { stroke-dashoffset: 0; } }
  @keyframes memory-draw { 0%, 45%, 98%, 100% { stroke-dashoffset: 32; } 55%, 97% { stroke-dashoffset: 0; } }
  @keyframes trigger-draw { 0%, 75%, 98%, 100% { stroke-dashoffset: 240; } 82%, 97% { stroke-dashoffset: 0; } }
  @keyframes clock-hand { 0%, 45%, 98%, 100% { transform: rotate(-90deg); } 73%, 97% { transform: rotate(180deg); } }
  @keyframes cursor-presence { 0%, 3%, 95%, 100% { opacity: 0; } 6%, 92% { opacity: 1; } }
  @keyframes cursor-horizontal {
    0%, 4%, 98%, 100% { transform: translateX(351px); }
    12%, 25% { transform: translateX(var(--x1)); }
    42%, 55% { transform: translateX(var(--x2)); }
    72%, 95% { transform: translateX(var(--x3)); }
  }
  @keyframes cursor-vertical {
    0%, 4%, 98%, 100% { transform: translateY(352px); }
    12%, 25% { transform: translateY(var(--y1)); }
    42%, 55% { transform: translateY(var(--y2)); }
    72%, 95% { transform: translateY(var(--y3)); }
  }
  @keyframes cursor-press {
    0%, 12%, 17%, 42%, 47%, 72%, 77%, 100% { transform: scale(1); }
    14%, 44%, 74% { transform: scale(.82); }
  }
  @keyframes click-one {
    0%, 13%, 20%, 100% { opacity: 0; transform: scale(.3); }
    14% { opacity: .85; transform: scale(.4); }
    19% { opacity: 0; transform: scale(1.6); }
  }
  @keyframes click-two {
    0%, 43%, 50%, 100% { opacity: 0; transform: scale(.3); }
    44% { opacity: .85; transform: scale(.4); }
    49% { opacity: 0; transform: scale(1.6); }
  }
  @keyframes click-three {
    0%, 73%, 80%, 100% { opacity: 0; transform: scale(.3); }
    74% { opacity: .85; transform: scale(.4); }
    79% { opacity: 0; transform: scale(1.6); }
  }
  @media (prefers-reduced-motion: reduce) {
    .tour-visual:not([data-allow-motion="true"]), .tour-visual:not([data-allow-motion="true"]) * { animation: none !important; transition: none !important; }
    .tour-visual:not([data-allow-motion="true"]) .cursor-presence { display: none; }
  }
</style>
