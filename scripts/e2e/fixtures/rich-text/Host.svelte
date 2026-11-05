<script>
  import RichText from "../../../../web/src/components/RichText.svelte";
  import { fixtures } from "./content.mjs";

  const params = new URLSearchParams(window.location.search);
  const fixture = params.get("fixture") || "mixed";
  const requestedWidth = Number(params.get("width")) === 146 ? 146 : 420;
  const compact = params.get("compact") === "true";
  const tone = params.get("tone") === "inverse" ? "inverse" : "default";
</script>

<!-- Only the parent fixture has test dimensions. RichText's production styles
     are imported unchanged, with no CSS injected into its descendants. -->
<main style="padding: 16px; max-width: 100%;">
  <section data-testid="richtext-parent" data-fixture={fixture} data-requested-width={requestedWidth} style:width={`${requestedWidth}px`} style:max-width="100%">
    <RichText text={fixtures[fixture] || fixtures.mixed} enableLinks {compact} {tone} />
  </section>
</main>
