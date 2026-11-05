<script lang="ts">
  import { Button, Input, Select } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import { GitFork, LoaderCircle, Maximize, Minus, Pencil, Plus } from "lucide-svelte";
  import { onDestroy, tick } from "svelte";
  import { localeInfo, t } from "../lib/i18n.svelte";
  import { deepLinkTranslations } from "../locales/deepLink";
  import { api, idempotencyKey } from "../lib/api";
  import { queryClient } from "../lib/query";
  import type { ConversationGoal, ConversationGraph, ConversationGraphDetail, ConversationMessage, ConversationNode, Page } from "../lib/types";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Field from "../ui/Field.svelte";
  import Link from "../components/Link.svelte";
  import Loading from "../components/Loading.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import RequireProject from "../components/RequireProject.svelte";
  import ConversationNodeCard from "../components/ConversationNodeCard.svelte";
  import { useConsole } from "./shared.svelte";
  import { location, setQueryParam } from "../lib/router.svelte";
  import { observeWorkLocation } from "../lib/workNavigation";
  import { createConversationZoom } from "../lib/conversationZoom";

  const context = useConsole();
  const project = $derived(context.project);
  const agents = $derived(context.agents ?? []);
  const availableAgents = $derived(agents.filter((agent) => agent.status === "active" && Boolean(agent.current_revision_id)));

  const deepLinkText = $derived(deepLinkTranslations[localeInfo.value]);
  let active = true;
  let messageFocusTimer: ReturnType<typeof setTimeout> | undefined;
  onDestroy(() => { active = false; if (messageFocusTimer) clearTimeout(messageFocusTimer); });
  let selectionEpoch = 0;
  let observedProjectId = "";
  let observedGraphId: string | null | undefined;
  let focusedViewportIntent = "";
  let focusedViewportElement: HTMLDivElement | undefined;
  let selectedNodeId = $state("");
  let viewportEl = $state<HTMLDivElement>();
  let viewport = $state({ x: 80, y: 120, scale: 1 });
  let panning = $state<{ startX: number; startY: number; originX: number; originY: number } | null>(null);
  let nodeDrag = $state<{ nodeId: string; startX: number; startY: number; originX: number; originY: number } | null>(null);
  let nodeOffsets = $state<Record<string, { x: number; y: number } | undefined>>({});
  let pendingDelete = $state<ConversationNode | null>(null);
  let rootAgentIds = $state<Record<string, string>>({});
  let editingGraphId = $state("");
  let graphTitleDraft = $state("");
  let sendingNodes = $state<Record<string, boolean>>({});
  const wheelZoom = createConversationZoom({ getViewport: () => viewport, setViewport: (value) => { viewport = value; } });
  onDestroy(() => { wheelZoom.destroy(); if (dragFrame !== undefined) cancelAnimationFrame(dragFrame); });

  const graphs = createQuery<Page<ConversationGraph>>(() => ({
    queryKey: ["conversation-graphs", project?.id],
    queryFn: ({ queryKey, signal }) => api<Page<ConversationGraph>>(`/projects/${encodeURIComponent(String(queryKey[1]))}/conversation-graphs`, { signal }),
    enabled: Boolean(project),
    // 标题由后台主会话异步生成，列表需要及时刷新；不因此重置相机或编辑草稿。
    refetchInterval: 2_000
  }));
  const graphItems = $derived((graphs.data?.items ?? []).filter((item) => item.project_id === project?.id));
  const requestedGraphId = $derived(new URLSearchParams(location.search).get("graph"));
  // Never fetch/render the first graph while resolving an explicit deep link.
  const activeGraphId = $derived(graphs.isError ? "" : requestedGraphId !== null
    ? graphItems.find((item) => item.id === requestedGraphId)?.id ?? ""
    : graphItems[0]?.id ?? "");
  const missingGraph = $derived(Boolean(requestedGraphId !== null && graphs.isSuccess && !graphs.isFetching && !activeGraphId));
  $effect(() => {
    const projectId = project?.id ?? "";
    if (observedProjectId === projectId && observedGraphId === requestedGraphId) return;
    observedGraphId = requestedGraphId;
    selectionEpoch++;
    if (observedProjectId !== projectId) {
      observedProjectId = projectId;
      editingGraphId = "";
      pendingDelete = null;
    }
  });

  function selectGraph(id: string) {
    const params = new URLSearchParams(location.search);
    if (project && params.has("project") && params.get("project") !== project.id) setQueryParam("project", project.id, true);
    setQueryParam("graph", id);
    for (const name of ["node", "message"]) setQueryParam(name, null, true);
  }

  const detail = createQuery<ConversationGraphDetail>(() => ({
    queryKey: ["conversation-detail", activeGraphId],
    queryFn: ({ queryKey, signal }) => api<ConversationGraphDetail>(`/conversation-graphs/${encodeURIComponent(String(queryKey[1]))}`, { signal }),
    enabled: Boolean(activeGraphId),
    // 节点运行中时短轮询，拿到 Agent 的流式结果和状态变化。
    refetchInterval: (query) =>
      ((query.state.data as ConversationGraphDetail | undefined)?.nodes.some((node) => node.status === "running") ||
        (query.state.data as ConversationGraphDetail | undefined)?.goals?.some((goal) => goal.status === "active"))
        ? 1_500 : 10_000
  }));
  const currentDetail = $derived(!detail.isError && detail.data?.graph.id === activeGraphId && detail.data.graph.project_id === project?.id
    ? detail.data : undefined);
  const currentGraph = $derived.by(() => {
    const listed = graphItems.find((item) => item.id === activeGraphId);
    const loaded = currentDetail?.graph;
    return listed && (!loaded || listed.version >= loaded.version) ? listed : loaded;
  });
  const invalidDetail = $derived(Boolean(activeGraphId && detail.isSuccess && !detail.isFetching && !currentDetail));
  const nodes = $derived(currentDetail?.nodes ?? []);
  const messages = $derived(currentDetail?.messages ?? []);
  const goalsByNode = $derived(new Map((currentDetail?.goals ?? []).map((goal) => [goal.node_id, goal])));

  async function updateGoal(node: ConversationNode, path: string, body: unknown, key?: string) {
    const goal = await api<ConversationGoal>(path, {
      method: "POST", body, headers: key ? { "Idempotency-Key": key } : undefined
    });
    queryClient.setQueryData<ConversationGraphDetail>(["conversation-detail", node.graph_id], (current) => current ? {
      ...current, goals: [...(current.goals ?? []).filter((item) => item.node_id !== node.id), goal]
    } : current);
    // Refresh only the graph captured by the action, even if selection changed.
    void queryClient.invalidateQueries({ queryKey: ["conversation-detail", node.graph_id] });
    return goal;
  }
  // 空数组必须是稳定引用，否则每次派生都会把新数组传给所有节点卡片，
  // 让拖动时的画布重渲染所有消息列表。
  const EMPTY_MESSAGES: ConversationMessage[] = [];
  const messagesByNode = $derived.by(() => {
    const grouped: Record<string, ConversationMessage[]> = {};
    for (const message of messages) {
      (grouped[message.node_id] ??= []).push(message);
    }
    return grouped;
  });
  const edges = $derived.by(() => nodes
    .filter((node) => Boolean(node.parent_id))
    .map((node) => {
      const parent = nodes.find((item) => item.id === node.parent_id);
      if (!parent) return null;
      const from = nodePosition(parent);
      const to = nodePosition(node);
      return { id: node.id, x1: from.x + (parent.width ?? 380), y1: from.y + 22, x2: to.x, y2: to.y + 22 };
    })
    .filter((edge): edge is NonNullable<typeof edge> => edge !== null));

  function nodePosition(node: ConversationNode): { x: number; y: number } {
    const offset = nodeOffsets[node.id];
    return offset ?? { x: node.position_x, y: node.position_y };
  }

  type GraphCreation = { projectId: string; epoch: number };
  const createGraph = createMutation<ConversationGraphDetail, Error, GraphCreation>(() => ({
    mutationFn: (draft) => api<ConversationGraphDetail>(`/projects/${draft.projectId}/conversation-graphs`, {
      method: "POST", body: {}
    }),
    onSuccess: (created, draft) => {
      queryClient.setQueryData<Page<ConversationGraph>>(["conversation-graphs", created.graph.project_id], (current) => ({
        items: [created.graph, ...(current?.items ?? [])], next_cursor: current?.next_cursor
      }));
      void queryClient.invalidateQueries({ queryKey: ["conversation-graphs", created.graph.project_id] });
      queryClient.setQueryData(["conversation-detail", created.graph.id], created);
      if (!active || project?.id !== draft.projectId || selectionEpoch !== draft.epoch) return;
      selectGraph(created.graph.id);
      setQueryParam("node", created.nodes[0]?.id ?? null, true);
    }
  }));

  const createRoot = createMutation<ConversationNode, Error, { graph: ConversationGraph; agentId: string }>(() => ({
    mutationFn: ({ graph, agentId }) => api<ConversationNode>(`/conversation-graphs/${graph.id}/root`, {
      body: { agent_id: agentId, expected_version: graph.version }
    }),
    onSuccess: (created) => {
      queryClient.setQueryData<ConversationGraphDetail>(["conversation-detail", created.graph_id], (current) => current ? {
        ...current, nodes: [created], graph: { ...current.graph, node_count: 1 }
      } : current);
      void queryClient.invalidateQueries({ queryKey: ["conversation-detail", created.graph_id] });
      void queryClient.invalidateQueries({ queryKey: ["conversation-graphs"] });
    },
    onError: (_error, input) => { void queryClient.invalidateQueries({ queryKey: ["conversation-detail", input.graph.id] }); }
  }));

  const renameGraph = createMutation<ConversationGraph, Error, { graph: ConversationGraph; title: string }>(() => ({
    mutationFn: ({ graph, title }) => api<ConversationGraph>(`/conversation-graphs/${graph.id}`, {
      method: "PATCH", body: { title, expected_version: graph.version }
    }),
    onSuccess: (updated) => {
      queryClient.setQueryData<Page<ConversationGraph>>(["conversation-graphs", updated.project_id], (current) => current ? {
        ...current, items: current.items.map((item) => item.id === updated.id ? updated : item)
      } : current);
      queryClient.setQueryData<ConversationGraphDetail>(["conversation-detail", updated.id], (current) => current ? { ...current, graph: updated } : current);
      if (editingGraphId === updated.id) editingGraphId = "";
    },
    onSettled: (_result, _error, { graph }) => {
      void queryClient.invalidateQueries({ queryKey: ["conversation-graphs", graph.project_id] });
      void queryClient.invalidateQueries({ queryKey: ["conversation-detail", graph.id] });
    }
  }));

  type AgentBinding = { expected_agent_id: string; expected_agent_revision_id: string };
  const sendMessage = createMutation<ConversationMessage, Error, { nodeId: string; graphId: string; body: string; key: string; binding: AgentBinding }>(() => ({
    mutationFn: ({ nodeId, body, key, binding }) => api<ConversationMessage>(`/conversation-nodes/${nodeId}/messages`, {
      body: { body, ...binding },
      headers: { "Idempotency-Key": key }
    }),
    onMutate: ({ nodeId }) => { sendingNodes[nodeId] = true; },
    onSettled: (_data, _error, { nodeId }) => { delete sendingNodes[nodeId]; },
    onSuccess: async (_message, input) => {
      await queryClient.invalidateQueries({ queryKey: ["conversation-detail", input.graphId] });
      void queryClient.invalidateQueries({ queryKey: ["conversation-graphs"] });
    }
  }));

  const forkNode = createMutation<ConversationNode, Error, { nodeId: string; messageId: string }>(() => ({
    mutationFn: (input) => api<ConversationNode>(`/conversation-nodes/${input.nodeId}/fork`, {
      body: { message_id: input.messageId }
    }),
    onSuccess: async (created) => {
      const stillSelected = active && currentDetail?.graph.id === created.graph_id;
      if (stillSelected) {
        selectedNodeId = created.id;
      }
      await queryClient.invalidateQueries({ queryKey: ["conversation-detail", created.graph_id] });
      void queryClient.invalidateQueries({ queryKey: ["conversation-graphs"] });
      if (active && currentDetail?.graph.id === created.graph_id) {
        nodeOffsets = {};
        resetView();
      }
    }
  }));

  const updateNode = createMutation<ConversationNode, Error, {
    node: ConversationNode;
    x?: number;
    y?: number;
    width?: number;
    height?: number;
    agentId?: string;
    title?: string;
  }>(() => ({
    mutationFn: ({ node, x, y, width, height, agentId, title }) => api<ConversationNode>(`/conversation-nodes/${node.id}`, {
      method: "PATCH",
      body: {
        expected_version: node.version,
        ...(x !== undefined ? { position_x: x } : {}),
        ...(y !== undefined ? { position_y: y } : {}),
        ...(width !== undefined ? { width } : {}),
        ...(height !== undefined ? { height } : {}),
        ...(agentId !== undefined ? { agent_id: agentId } : {}),
        ...(title !== undefined ? { title } : {})
      }
    }),
    onSuccess: (updated) => {
      queryClient.setQueryData<ConversationGraphDetail>(["conversation-detail", updated.graph_id], (current) => current ? {
        ...current, nodes: current.nodes.map((node) => node.id === updated.id ? updated : node)
      } : current);
    },
    onSettled: (_result, _error, { node }) => {
      delete nodeOffsets[node.id];
      void queryClient.invalidateQueries({ queryKey: ["conversation-detail", node.graph_id] });
    }
  }));

  const deleteNode = createMutation<void, Error, ConversationNode>(() => ({
    mutationFn: (node) => api<void>(`/conversation-nodes/${node.id}?expected_version=${node.version}`, {
      method: "DELETE"
    }),
    onSuccess: (_result, node) => {
      if (pendingDelete?.id === node.id) pendingDelete = null;
      if (node.parent_id) void queryClient.invalidateQueries({ queryKey: ["conversation-detail", node.graph_id] });
      void queryClient.invalidateQueries({ queryKey: ["conversation-graphs"] });
    }
  }));

  const error = $derived(graphs.error ?? detail.error ?? createGraph.error ?? forkNode.error ?? updateNode.error);

  async function stopRun(runId: string) {
    await api(`/runs/${runId}/stop`, { method: "POST", body: { mode: "graceful" }, headers: { "Idempotency-Key": idempotencyKey("stop") } });
    await queryClient.invalidateQueries({ queryKey: ["conversation-detail"] });
  }

  function handleWheel(event: WheelEvent) {
    if (window.matchMedia("(max-width: 767px)").matches) { wheelZoom.cancel(); return; }
    // A card owns its wheel input (history, editor and nested scroll areas).
    // Only the surrounding canvas zooms; no Ctrl/Command modifier is needed.
    if (event.target instanceof Element && event.target.closest(".conversation-node")) { wheelZoom.cancel(); return; }
    const rect = viewportEl?.getBoundingClientRect();
    if (!rect || !Number.isFinite(event.deltaY) || event.deltaY === 0) return;
    event.preventDefault();
    wheelZoom.wheel({ deltaY: event.deltaY, deltaMode: event.deltaMode, height: rect.height,
      x: event.clientX - rect.left, y: event.clientY - rect.top, ctrlKey: event.ctrlKey });
  }

  function startPan(event: PointerEvent) {
    if (event.button !== 0 || event.target !== viewportEl || window.matchMedia("(max-width: 767px)").matches) return;
    wheelZoom.cancel();
    panning = { startX: event.clientX, startY: event.clientY, originX: viewport.x, originY: viewport.y };
    viewportEl?.setPointerCapture(event.pointerId);
  }

  function movePan(event: PointerEvent) {
    if (!panning) return;
    viewport = {
      ...viewport,
      x: panning.originX + (event.clientX - panning.startX),
      y: panning.originY + (event.clientY - panning.startY)
    };
  }

  function endPan(event: PointerEvent) {
    if (!panning) return;
    if (viewportEl?.hasPointerCapture(event.pointerId)) viewportEl.releasePointerCapture(event.pointerId);
    panning = null;
  }

  function startNodeDrag(node: ConversationNode, event: PointerEvent) {
    if (event.button !== 0 || updateNode.isPending) return;
    event.preventDefault();
    wheelZoom.cancel();
    const origin = nodePosition(node);
    nodeDrag = { nodeId: node.id, startX: event.clientX, startY: event.clientY, originX: origin.x, originY: origin.y };
    (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  }

  // 拖动期间用 requestAnimationFrame 合并 pointermove，避免每个原生事件都
  // 触发一次画布与连线重算。
  let dragFrame: number | undefined;
  let latestDragPosition: { nodeId: string; x: number; y: number } | null = null;

  function flushNodeDrag() {
    if (dragFrame !== undefined) {
      cancelAnimationFrame(dragFrame);
      dragFrame = undefined;
    }
    if (!latestDragPosition) return;
    const { nodeId, x, y } = latestDragPosition;
    nodeOffsets = { ...nodeOffsets, [nodeId]: { x, y } };
  }

  function moveNodeDrag(event: PointerEvent) {
    if (!nodeDrag) return;
    latestDragPosition = {
      nodeId: nodeDrag.nodeId,
      x: nodeDrag.originX + (event.clientX - nodeDrag.startX) / viewport.scale,
      y: nodeDrag.originY + (event.clientY - nodeDrag.startY) / viewport.scale
    };
    if (dragFrame === undefined) {
      dragFrame = requestAnimationFrame(() => {
        dragFrame = undefined;
        flushNodeDrag();
      });
    }
  }

  function endNodeDrag(event: PointerEvent) {
    if (!nodeDrag) return;
    const state = nodeDrag;
    nodeDrag = null;
    const handle = event.currentTarget as HTMLElement;
    if (handle.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
    flushNodeDrag();
    latestDragPosition = null;
    if (event.type === "pointercancel") { delete nodeOffsets[state.nodeId]; return; }
    const node = nodes.find((item) => item.id === state.nodeId);
    const position = nodeOffsets[state.nodeId];
    if (!node || !position) return;
    if (Math.round(node.position_x) === Math.round(position.x) && Math.round(node.position_y) === Math.round(position.y)) {
      delete nodeOffsets[state.nodeId];
      return;
    }
    updateNode.mutate({ node, x: position.x, y: position.y });
  }

  function zoomBy(factor: number) {
    wheelZoom.cancel();
    const rect = viewportEl?.getBoundingClientRect();
    const px = (rect?.width ?? 800) / 2;
    const py = (rect?.height ?? 600) / 2;
    const next = Math.min(2, Math.max(0.2, viewport.scale * factor));
    const ratio = next / viewport.scale;
    viewport = {
      x: px - (px - viewport.x) * ratio,
      y: py - (py - viewport.y) * ratio,
      scale: next
    };
  }

  function resetView() {
    wheelZoom.cancel();
    const rect = viewportEl?.getBoundingClientRect();
    if (!rect || !nodes.length) { viewport = { x: 40, y: 80, scale: 1 }; return; }
    const left = Math.min(...nodes.map((node) => node.position_x));
    const top = Math.min(...nodes.map((node) => node.position_y));
    const width = Math.max(...nodes.map((node) => node.position_x + (node.width || 380))) - left;
    const height = Math.max(...nodes.map((node) => node.position_y + (node.height || 560))) - top;
    const scale = Math.min(1, Math.max(0.2, Math.min((rect.width - 80) / width, (rect.height - 100) / height)));
    viewport = { x: (rect.width - width * scale) / 2 - left * scale, y: 70 - top * scale, scale };
  }

  function openCreate() {
    if (project && !createGraph.isPending) createGraph.mutate({ projectId: project.id, epoch: selectionEpoch });
  }

  $effect(() => {
    void activeGraphId;
    wheelZoom.cancel();
    editingGraphId = "";
    createRoot.reset();
    selectedNodeId = new URLSearchParams(location.search).get("node")??"";
    nodeOffsets = {};
    viewport = { x: 40, y: 80, scale: 1 };
  });
  $effect(()=>{if(project&&graphs.isSuccess&&(!activeGraphId||currentDetail))observeWorkLocation({project_id:project.id,graph_id:activeGraphId,node_id:nodes.some(n=>n.id===selectedNodeId)?selectedNodeId:undefined});});
  $effect(()=>{
    const params=new URLSearchParams(location.search);const node=params.get("node"),message=params.get("message");
    if (!node) {
      focusedViewportIntent = ""; focusedViewportElement = undefined;
      if (messageFocusTimer) clearTimeout(messageFocusTimer);
      messageFocusTimer = undefined;
      return;
    }
    if(!node || !viewportEl || !nodes.some(n=>n.id===node))return;
    // A deep link is a navigation intent, not a camera-follow subscription.
    // Polling, a completed Run or a persisted drag/resize must not undo the
    // user's later pan/zoom. A remounted canvas still applies its initial link.
    const intent = JSON.stringify([project?.id, activeGraphId, node, message]);
    if (focusedViewportIntent === intent && focusedViewportElement === viewportEl) return;
    focusedViewportIntent = intent;
    focusedViewportElement = viewportEl;
    if (messageFocusTimer) clearTimeout(messageFocusTimer);
     wheelZoom.cancel();
     const item=nodes.find(n=>n.id===node)!;viewport={x:40-item.position_x,y:80-item.position_y,scale:1};
     if (!message && window.matchMedia("(max-width: 767px)").matches) {
       // Mobile cards are stacked and intentionally ignore the canvas transform.
       // Follow this navigation intent after their DOM is committed, not every
       // polling update; otherwise later manual scrolling would be undone.
       const canvas = viewportEl;
       void tick().then(() => {
         if (!active || focusedViewportIntent !== intent || focusedViewportElement !== canvas || !canvas.isConnected) return;
         const target = Array.from(canvas.querySelectorAll<HTMLElement>("[data-node-id]"))
           .find((element) => element.getAttribute("data-node-id") === node);
         target?.scrollIntoView({ block: "center", inline: "nearest" });
       });
     }
    if (message) messageFocusTimer = setTimeout(() => {
      if (!active || focusedViewportIntent !== intent || !focusedViewportElement?.isConnected) return;
      const target = document.getElementById(`message-${message}`);
      if (target?.closest("[data-node-id]")?.getAttribute("data-node-id") === node) target.scrollIntoView({ block: "center" });
      messageFocusTimer = undefined;
    }, 100);
  });
  $effect(() => {
    const element = viewportEl;
    element?.addEventListener("wheel", handleWheel, { passive: false });
    return () => element?.removeEventListener("wheel", handleWheel);
  });
</script>

<RequireProject>
  <section class="flex h-full min-h-0 flex-col">
  <PageHeader title={t("conversations.title")} description={t("conversations.desc")}>
    {#snippet actions()}
      <Button
        variant="primary"
        type="button"
        onclick={openCreate}
        disabled={!project || createGraph.isPending}
      ><GitFork size={14} />{t("conversations.newGraph")}</Button>
    {/snippet}
  </PageHeader>
  {#if error}<div class="border-b border-border px-5 py-3"><ErrorNotice {error} /></div>{/if}
  {#if graphs.isLoading}
    <Loading block />
  {:else}
    <div class="flex min-h-0 flex-1 max-md:flex-col">
      <div class="hidden shrink-0 border-b border-border p-3 max-md:block">
        <Select class="w-full" aria-label={t("conversations.title")} value={activeGraphId} onchange={(event) => selectGraph(event.currentTarget.value)}>
          <option value="" disabled>{t("conversations.rootNodes")}</option>
          {#each graphItems as graph (graph.id)}<option value={graph.id}>{graph.title}</option>{/each}
        </Select>
      </div>
      <aside class="flex w-[260px] shrink-0 flex-col border-r border-border bg-surface-subtle max-md:hidden" aria-label={t("conversations.rootNodes")}>
        <header class="flex min-h-11 items-center justify-between border-b border-border px-3">
          <strong class="text-xs font-semibold">{t("conversations.rootNodes")}</strong>
          <Button type="button" icon controlSize="sm" variant="ghost" aria-label={t("conversations.newGraph")} title={t("conversations.newGraph")} onclick={openCreate} disabled={createGraph.isPending}>
            <Plus size={14} aria-hidden="true" />
          </Button>
        </header>
        <div class="min-h-0 flex-1 overflow-y-auto p-1.5">
          {#each graphItems as graph (graph.id)}
            <button
              class="grid w-full cursor-pointer gap-0.5 rounded-sm border-0 px-2.5 py-2 text-left text-xs {activeGraphId === graph.id ? 'bg-surface font-semibold shadow-sm' : 'bg-transparent text-secondary hover:bg-surface-hover'}"
              type="button"
              aria-current={activeGraphId === graph.id ? "page" : undefined}
              onclick={() => selectGraph(graph.id)}
            >
              <span class="truncate">{graph.title}</span>
              <span class="text-2xs font-normal text-muted">{t("conversations.graphMeta", { count: graph.node_count })}</span>
            </button>
          {/each}
          {#if !graphItems.length}
            <p class="px-2.5 py-3 text-2xs leading-relaxed text-muted">{t("conversations.empty")}</p>
          {/if}
        </div>
      </aside>

      <div class="relative min-h-0 min-w-0 flex-1 bg-canvas">
        {#if missingGraph || invalidDetail}
          <div role="alert" class="grid h-full place-items-center p-6">
            <EmptyState title={deepLinkText.graphUnavailable} description={deepLinkText.graphUnavailableHint} />
          </div>
        {:else if requestedGraphId !== null && !activeGraphId && graphs.isFetching}
          <Loading block />
        {:else if !activeGraphId}
          <div class="grid h-full place-items-center p-6">
            <EmptyState title={t("conversations.empty")} description={t("conversations.emptyDesc")} />
          </div>
        {:else if detail.isLoading || (!currentDetail && detail.isFetching)}<Loading block />
        {:else if !currentDetail}
          <div class="grid h-full place-items-center p-6">
            <EmptyState title={deepLinkText.graphUnavailable} description={deepLinkText.graphUnavailableHint} />
          </div>
        {:else}
          <div class="flex h-full min-h-0 flex-col">
          <header class="z-20 flex min-h-11 shrink-0 items-center gap-2 border-b border-border bg-surface px-4 py-1.5 pr-48 max-md:pr-4">
            {#if editingGraphId === currentDetail.graph.id}
              <form class="flex min-w-0 flex-1 flex-wrap items-center gap-2" onsubmit={(event) => {
                event.preventDefault();
                if (currentGraph && graphTitleDraft.trim() && !renameGraph.isPending) renameGraph.mutate({ graph: currentGraph, title: graphTitleDraft.trim() });
              }}>
                <Input class="min-w-0 flex-1" controlSize="sm" bind:value={graphTitleDraft} maxlength={200} required aria-label={t("conversations.graphTitle")} disabled={renameGraph.isPending} />
                <Button controlSize="sm" type="submit" disabled={!graphTitleDraft.trim() || renameGraph.isPending}>{t("common.save")}</Button>
                <Button controlSize="sm" type="button" disabled={renameGraph.isPending} onclick={() => editingGraphId = ""}>{t("common.cancel")}</Button>
                {#if renameGraph.error}<ErrorNotice error={renameGraph.error} />{/if}
              </form>
            {:else}
              <h2 class="m-0 min-w-0 truncate text-xs font-semibold">{currentGraph?.title}</h2>
              <Button type="button" icon controlSize="sm" variant="ghost" aria-label={`${t("common.edit")} · ${t("conversations.graphTitle")}`} onclick={() => {
                renameGraph.reset(); graphTitleDraft = currentGraph?.title ?? ""; editingGraphId = activeGraphId;
              }}><Pencil size={12} /></Button>
            {/if}
          </header>
          <div class="pointer-events-none absolute right-4 top-2 z-20 flex items-center gap-1 rounded-md border border-border bg-surface p-1 shadow-sm max-md:hidden">
            <button class="pointer-events-auto grid size-7 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-secondary hover:bg-surface-hover" type="button" title={t("conversations.zoomOut")} aria-label={t("conversations.zoomOut")} onclick={() => zoomBy(1 / 1.2)}><Minus size={13} /></button>
            <span class="w-12 text-center text-2xs tabular-nums text-muted">{Math.round(viewport.scale * 100)}%</span>
            <button class="pointer-events-auto grid size-7 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-secondary hover:bg-surface-hover" type="button" title={t("conversations.zoomIn")} aria-label={t("conversations.zoomIn")} onclick={() => zoomBy(1.2)}><Plus size={13} /></button>
            <button class="pointer-events-auto grid size-7 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-secondary hover:bg-surface-hover" type="button" title={t("conversations.resetView")} aria-label={t("conversations.resetView")} onclick={resetView}><Maximize size={13} /></button>
          </div>

          <!-- The custom canvas is keyboard-pannable; its children remain ordinary form controls. -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
          <div
            bind:this={viewportEl}
            class="min-h-0 w-full flex-1 touch-none cursor-grab overflow-hidden outline-offset-[-2px] max-md:touch-auto max-md:cursor-auto max-md:overflow-y-auto {panning ? 'cursor-grabbing' : ''}"
            role="application"
            tabindex="0"
            aria-label={t("conversations.canvas")}
            onpointerdown={startPan}
            onpointermove={movePan}
            onpointerup={endPan}
            onpointercancel={endPan}
            onkeydown={(event) => {
              if (event.target !== viewportEl) return;
              const directions: Record<string, number[]> = { ArrowLeft: [40, 0], ArrowRight: [-40, 0], ArrowUp: [0, 40], ArrowDown: [0, -40] };
              const delta = directions[event.key];
              if (delta) { event.preventDefault(); wheelZoom.cancel(); viewport = { ...viewport, x: viewport.x + delta[0], y: viewport.y + delta[1] }; }
            }}
          >
            <div
              class="pointer-events-none relative h-full w-full origin-top-left max-md:flex max-md:h-auto max-md:flex-col max-md:gap-4 max-md:p-4 max-md:!transform-none"
              style:transform={`translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.scale})`}
            >
              <svg class="pointer-events-none absolute left-0 top-0 overflow-visible max-md:hidden" width="1" height="1" aria-hidden="true">
                {#each edges as edge (edge.id)}
                  <path
                    d={`M ${edge.x1} ${edge.y1} C ${edge.x1 + 80} ${edge.y1}, ${edge.x2 - 80} ${edge.y2}, ${edge.x2} ${edge.y2}`}
                    fill="none"
                    stroke="var(--color-border-strong)"
                    stroke-width="2"
                  />
                {/each}
              </svg>
              {#if !nodes.length}
                <article class="conversation-node pointer-events-auto absolute left-0 top-0 grid w-[380px] max-w-full gap-4 rounded-lg border border-border bg-surface p-4 shadow-sm max-md:relative max-md:w-full" aria-label={t("conversations.selectAgent")}>
                  <h3 class="m-0 text-sm font-semibold">{currentDetail.graph.title}</h3>
                  <!-- No composer is mounted until the root exists: selecting an Agent cannot discard message/Goal drafts. -->
                  <Field label={t("conversations.agent")}>
                    <Select value={rootAgentIds[currentDetail.graph.id] ?? ""} disabled={createRoot.isPending} onchange={(event) => {
                      if (currentDetail) rootAgentIds[currentDetail.graph.id] = event.currentTarget.value;
                    }}>
                      <option value="">{t("conversations.selectAgent")}</option>
                      {#each availableAgents as agent (agent.id)}<option value={agent.id}>{agent.name}</option>{/each}
                    </Select>
                  </Field>
                  {#if !availableAgents.length}<Link to="/agents" class="text-sm text-accent underline">{t("agents.noAgents")} · {t("shell.agents")}</Link>{/if}
                  {#if createRoot.error}<ErrorNotice error={createRoot.error} />{/if}
                  <Button type="button" variant="primary" disabled={createRoot.isPending || !availableAgents.some((agent) => agent.id === rootAgentIds[currentDetail!.graph.id])} onclick={() => {
                    if (currentDetail && !createRoot.isPending) createRoot.mutate({ graph: currentDetail.graph, agentId: rootAgentIds[currentDetail.graph.id] });
                  }}>{#if createRoot.isPending}<LoaderCircle size={14} class="animate-spin" />{/if}{t("conversations.create")}</Button>
                </article>
              {/if}
              {#each nodes as node (node.id)}
                <ConversationNodeCard
                  {node}
                  position={nodePosition(node)}
                  messages={messagesByNode[node.id] ?? EMPTY_MESSAGES}
                  {agents}
                  selected={selectedNodeId === node.id}
                  busy={Boolean(sendingNodes[node.id]) || forkNode.isPending && forkNode.variables?.nodeId === node.id}
                  goal={goalsByNode.get(node.id) ?? null}
                  onCreateGoal={(objective, tokenBudget, key, binding) => updateGoal(node,
                    `/conversation-nodes/${node.id}/goals`, { objective, token_budget: tokenBudget, ...binding }, key)}
                  onGoalAction={(goal, action) => updateGoal(node,
                    `/conversation-nodes/${node.id}/goals/${goal.id}/${action}`, { expected_version: goal.version })}
                  scale={viewport.scale}
                  onSelect={() => (selectedNodeId = node.id)}
                  onSend={(body, key, binding) => sendMessage.mutateAsync({ nodeId: node.id, graphId: node.graph_id, body, key, binding })}
                  onChangeAgent={(agentId) => updateNode.mutateAsync({ node, agentId })}
                  onRename={(title) => updateNode.mutateAsync({ node, title })}
                  onFork={(message) => { if (!forkNode.isPending) forkNode.mutate({ nodeId: node.id, messageId: message.id }); }}
                  onDelete={() => { deleteNode.reset(); pendingDelete = node; }}
                  onStop={stopRun}
                  onDragStart={(event) => startNodeDrag(node, event)}
                  onDragMove={moveNodeDrag}
                  onDragEnd={endNodeDrag}
                  onResize={(size) => updateNode.mutate({ node, width: size.width, height: size.height })}
                  onRunSettled={() => { void queryClient.invalidateQueries({ queryKey: ["conversation-detail", node.graph_id] }); }}
                />
              {/each}
            </div>
          </div>
          </div>
        {/if}
      </div>
    </div>
  {/if}

  </section>
  {#if pendingDelete}
    <ConfirmDialog
      title={t("conversations.deleteNode")}
      description={t("conversations.deleteConfirm")}
      confirmLabel={t("common.delete")}
      danger={true}
      busy={deleteNode.isPending}
      error={deleteNode.error}
      onConfirm={() => { if (pendingDelete) deleteNode.mutate(pendingDelete); }}
      onCancel={() => (pendingDelete = null)}
    />
  {/if}
</RequireProject>
