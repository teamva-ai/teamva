<script lang="ts">
  import ArrowUpRight from "lucide-svelte/icons/arrow-up-right";
  import BrandMark from "./BrandMark.svelte";
  import { languageHref, siteLanguage } from "../language.svelte";
  import { siteCopy } from "../copy";

  let { homeHref = "./", docsHref = "./docs/" }: { homeHref?: string; docsHref?: string } = $props();
  const s = $derived(siteCopy[siteLanguage.value]);
  const zh = $derived(siteLanguage.value === "zh");
</script>

<footer class="site-footer">
  <div class="site-footer__top">
    <div class="site-footer__identity">
      <a href={languageHref(homeHref)} aria-label={s.home}><BrandMark /></a>
      <p>{s.footer.line1}</p>
    </div>
    <div class="site-footer__links">
      <div>
        <span>{zh ? "产品" : "Product"}</span>
        <a href={languageHref(`${homeHref}#product`)}>{s.nav.workspace}</a>
        <a href={languageHref(`${homeHref}#workspace`)}>{s.footer.tour}</a>
        <a href={languageHref(`${homeHref}#local-first`)}>{zh ? "本地优先" : "Local-first"}</a>
      </div>
      <div>
        <span>{zh ? "了解更多" : "Learn"}</span>
        <a href={languageHref(docsHref)}>{s.nav.docs}</a>
        <a href={languageHref(`${docsHref}#multi-agent-semantics`)}>{zh ? "多 Agent 语义" : "Agent semantics"}</a>
        <a href={languageHref(`${docsHref}#data-boundaries`)}>{zh ? "数据边界" : "Data boundaries"}</a>
      </div>
      <div>
        <span>{zh ? "当前状态" : "Status"}</span>
        <a href={languageHref(`${docsHref}#product-boundaries`)}>{zh ? "macOS 开发预览" : "macOS preview"}<ArrowUpRight size={13} /></a>
        <a href={languageHref(`${docsHref}#before-you-begin`)}>{zh ? "开发进行中" : "In development"}</a>
      </div>
    </div>
  </div>
  <div class="site-footer__bottom">
    <span>© 2026 Teamva</span>
    <span>{s.footer.support}</span>
  </div>
</footer>
