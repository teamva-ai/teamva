# Teamva website

Standalone **Svelte 5 + TypeScript + Vite** frontend. No SvelteKit, backend, analytics, CDN scripts, or animation dependency.

From the repository root:

```bash
pnpm install
pnpm dev:website       # http://localhost:5174
pnpm build:website    # website/dist; includes /docs/
```

Deploy `dist/` to a static host when publication is authorized. The Vite base is relative; the site and the separate documentation page work below a subpath. This change does not deploy or publish anything.

## Product imagery

The landing page, guide header/footer, and project-context illustration use the same approved Silken mark from `../assets/brand/icon.png`. The favicon is a native-image derivative shared with the product UI; no TV lettering or separate node-logo drawing remains.

Images are imported directly from `../docs/screenshots/`, with hashed production assets. English uses the eight root images; Chinese uses the matching eight images in `zh/`. The selected site language controls both the hero and gallery assets. They show real application UI with prewritten illustrative data, including completion states. See [capture instructions](../docs/SHOWCASE.md). Preserve that distinction when updating imagery.

## Visual effects

CSS aurora, grid sweeps, animated gradient typography, orbital markers, flowing connectors, floating resource cards, hover highlights, pointer lighting, scroll progress, and viewport reveals run automatically. Effects follow the system's `prefers-reduced-motion` setting, including live changes. There is no website motion switch or explanatory control text. All imagery and fonts are bundled locally; effects use the existing CSS and browser APIs.

The hero cycles every 7 seconds through three distinct views: public agent teamwork, intervention in one agent’s sidebar, and graph conversations. The active tab’s underline is the CSS progress animation itself; there is no second progress line. Activating any tab, including the selected tab or keyboard activation, stops autoplay for the page visit and leaves a solid active underline. Language, visibility, and system-preference changes preserve that manual stop. Reduced motion, an offscreen selector, or a hidden browser page pauses progress; it resumes from the same point when those conditions clear. Tabs sit outside the image frame, with no autoplay explanation, click instructions, or imitation application controls. The product gallery exposes eight screens in each language.

## Languages and product copy

The capabilities section is an open editorial sequence, not a feature-card grid: parallel work and handoffs, exploration and local memory, then recurring work and human control. Three typographic chapters pair concise copy with decorative line diagrams; the middle chapter reverses on desktop and returns to reading order on mobile. The diagrams illustrate capabilities, not recorded model activity.

A visible EN / 中文 switch covers both the landing page and documentation. `?lang=en` or `?lang=zh` makes links shareable; the last explicit choice is stored locally and used when the URL has no language preference. Content, screenshots, image descriptions, navigation, and metadata switch together. Screenshot roles are Product Manager / 产品经理, Frontend Developer / 前端开发, Backend Developer / 后端开发, and QA Engineer / 测试工程师. Public conversations pair deep analysis for requirements and reviews with efficient execution for implementation; these are role configurations, not model-performance claims.

DeepSeek's Super-agent context is visible in the hero, with dated research attribution in its own section before the CLI integrations. Teamva is identified briefly as an independently developed local workspace. Landing-page copy focuses on capabilities and benefits: automatic CLI detection, supported existing configuration, controlled Claude Code file and task tools, and planned additional adapters. The local section explains on-device storage, model-service data policies, and password-protected access over a trusted LAN or VPN. Detailed permissions, unencrypted HTTP access, and supported scope belong in the guide.

## Checks

Run the static check from the repository root. Start the Vite server before browser tests:

```bash
pnpm --filter @teamva/website check
pnpm --filter @teamva/website test:browser
```

The browser suite covers both languages, desktop/mobile layouts, screenshot-selector geometry, all gallery choices, localized image loading, navigation, language persistence, clipboard success/failure, automatic effects, system reduced-motion behavior, and documentation navigation. Carousel checks use real Chrome CSS animations, accelerated through the Web Animations API, to verify progress, automatic cycling, permanent manual stop, offscreen pause, and simulated background visibility events. Browser tests do not run Teamva agents or model requests.

Keep product claims grounded in the current implementation. Source builds are a development preview; macOS artifacts are unsigned and not notarized. Research attribution, illustrative screenshots, and model-provider policies stay explicit.
