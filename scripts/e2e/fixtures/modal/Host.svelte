<script>
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../../../../web/src/lib/query";
  import { Button, Modal } from "../../../../web/src/ui";
  import OnboardingGuide from "../../../../web/src/pages/OnboardingGuide.svelte";
  import GlobalSearch from "../../../../web/src/components/GlobalSearch.svelte";

  const params = new URLSearchParams(location.search);
  const fixture = params.get("case") || "guide";
  const maxWidth = Number(params.get("width") || 640);
  const rows = Number(params.get("rows") || 1);
  const unbroken = params.get("unbroken") !== "0";
  const nested = params.get("nested") === "1";
  let open = $state(params.get("closed") !== "1");
  let nestedOpen = $state(false);
  const computer = { id: "fixture-computer", name: "Fixture Mac" };
</script>

<QueryClientProvider client={queryClient}>
  <main class="min-h-dvh bg-canvas p-6">
    <p data-testid="fixture-background">隔离弹窗布局夹具，不调用模型或业务服务。</p>
    {#if fixture === "search"}
      <GlobalSearch projectId="fixture-project" projectName="几何测试" />
    {:else}
      {#if fixture !== "guide"}<Button onclick={() => (open = true)}>打开几何弹窗</Button>{/if}
      <!-- Deliberately hostile containing block: a modal must escape all of it. -->
      <div style="transform:translate(17px,23px) scale(.94);contain:layout paint;overflow:hidden;width:320px;height:260px">
        {#if fixture === "guide"}
          <OnboardingGuide {computer} />
        {:else if open}
          <Modal title="公共弹窗几何" description={"超长说明".repeat(12)} {maxWidth} data-fixture-modal="outer" onClose={() => (open = false)}>
            <div class="grid gap-4">
              {#if nested}<Button onclick={() => (nestedOpen = true)}>打开嵌套弹窗</Button>{/if}
              {#each Array.from({ length: rows }) as _, index}<p>正文第 {index + 1} 行：尺寸应受视口约束，内容过长时仅正文滚动。</p>{/each}
              {#if unbroken}<p>{"unbroken-content-".repeat(60)}</p>{/if}
            </div>
            {#if nestedOpen}
              <Modal title="静态嵌套几何" maxWidth={480} data-fixture-modal="nested" onClose={() => (nestedOpen = false)}>
                <div class="grid gap-4">
                  {#each Array.from({ length: 60 }) as _, index}<p>嵌套正文第 {index + 1} 行：固定内容用于首帧采样，不等待查询。</p>{/each}
                </div>
                {#snippet footer()}<Button onclick={() => (nestedOpen = false)}>完成嵌套几何测试</Button>{/snippet}
              </Modal>
            {/if}
            {#snippet footer()}<Button variant="primary" onclick={() => (open = false)}>完成几何测试</Button>{/snippet}
          </Modal>
        {/if}
      </div>
    {/if}
  </main>
</QueryClientProvider>
