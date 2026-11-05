<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import { Button, Checkbox, Field, FieldGroup, Input, Modal, Select, Textarea } from "../ui";
  import { api, APIError } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { remoteAuth } from "../lib/remoteAuth.svelte";
  import { useUnsavedChanges } from "../lib/unsavedChanges.svelte";
  import { buildMcpInput, isMcpHttpURL, mcpDraft, rebaseMcpSecrets, McpValidationError, type McpDraft, type McpInput, type McpScope, type McpView } from "../lib/mcp";
  import { parseMcpImport, type McpImportItem } from "../lib/mcpImport";
  import { useConsole } from "./shared.svelte";

  const context = useConsole();
  const projectId = $derived(context.project?.id);
  let items = $state<McpView[]>([]);
  let loading = $state(false);
  let error = $state("");
  let operation = $state("");
  let pendingAuth = $state<string[]>([]);
  let authorization = $state<{ key: string; url: string } | null>(null);
  let mounted = true;
  let generation = 0;
  let loadSequence = 0;
  type Editor = { id: number; draft: McpDraft; version: number; saved: string; saving: boolean; failed: string; error: string; conflict: boolean };
  let editor = $state<Editor | null>(null);
  let nextEditor = 0;
  const signature = $derived(editor ? JSON.stringify(editor.draft) : "");
  const dirty = $derived(Boolean(editor && signature !== editor.saved));
  let importOpen = $state(false);
  let importText = $state("");
  let importItems = $state<McpImportItem[]>([]);
  let selectedImports = $state<string[]>([]);
  let importScope = $state<McpScope>("user");
  let importError = $state("");
  let importing = $state(false);
  const guard = useUnsavedChanges(() => dirty || (importOpen && Boolean(importText)), { onDiscard: () => { editor = null; importOpen = false; } });
  const keyOf = (view: Pick<McpView, "scope" | "name">) => `${view.scope}:${view.name}`;
  const isShadowed = (view: McpView) => view.shadowed || (view.scope === "user" && items.some((item) => item.scope === "project" && item.name === view.name));
  const fixedOAuthCallback = (view: McpView) => Boolean(view.config.oauth && (view.config.oauth.callback_port || view.config.oauth.redirect_uri));
  const message = (err: unknown) => err instanceof McpValidationError ? t(`mcp.${err.key}`) : err instanceof APIError && err.status === 409 ? t("mcp.conflict") : t("mcp.failed");
  function upsert(view: McpView) {
    const old = items.find((item) => keyOf(item) === keyOf(view));
    if (old && old.version > view.version) return;
    items = [...items.filter((item) => keyOf(item) !== keyOf(view)), view];
  }
  async function load() {
    if (remoteAuth.active || !mounted) return;
    const ownGeneration = generation;
    const sequence = ++loadSequence;
    const project = projectId;
    loading = true;
    try {
      const result = await api<{ items: McpView[] }>(`/mcp/servers${project ? `?project_id=${encodeURIComponent(project)}` : ""}`);
      if (!mounted || ownGeneration !== generation || sequence !== loadSequence) return;
      // A GET started before a save must not roll its new version back.
      items = result.items.map((view) => {
        const newer = items.find((item) => keyOf(item) === keyOf(view) && item.version > view.version);
        return newer ?? view;
      });
      pendingAuth = pendingAuth.filter((key) => items.some((item) => keyOf(item) === key && ["needs_auth", "auth_pending", "connecting"].includes(item.status)));
      if (authorization && !pendingAuth.includes(authorization.key)) authorization = null;
      error = "";
    } catch (err) {
      if (mounted && ownGeneration === generation && sequence === loadSequence) { error = message(err); pendingAuth = []; }
    } finally {
      if (mounted && ownGeneration === generation && sequence === loadSequence) loading = false;
    }
  }
  $effect(() => {
    projectId; remoteAuth.active;
    untrack(() => {
      generation++; items = []; editor = null; authorization = null; pendingAuth = [];
      importOpen = false; importText = ""; importItems = []; operation = ""; loading = false; error = "";
      void load();
    });
  });
  $effect(() => {
    if (remoteAuth.active || loading || error || (!pendingAuth.length && !items.some((item) => item.status === "connecting"))) return;
    const timer = setTimeout(() => { void load(); }, 2000);
    return () => clearTimeout(timer);
  });
  onDestroy(() => { mounted = false; generation++; });

  function openEditor(view?: McpView) {
    if (!guard.confirm()) return;
    const draft = view ? mcpDraft(view.config, view.name, view.scope) : mcpDraft();
    editor = { id: ++nextEditor, draft, version: view?.version ?? 0, saved: view ? JSON.stringify(draft) : "", saving: false, failed: "", error: "", conflict: false };
  }
  function closeEditor() { if (guard.confirm()) editor = null; }
  const validation = $derived.by(() => {
    if (!editor) return "";
    try { buildMcpInput(editor.draft, editor.version, projectId); return ""; } catch (err) { return message(err); }
  });
  async function save() {
    const current = editor;
    if (!current || current.saving || remoteAuth.active || current.conflict) return;
    const snapshot = JSON.stringify(current.draft);
    let input: McpInput;
    try {
      input = buildMcpInput(current.draft, current.version, projectId);
      if (!current.version && items.some((item) => item.name === input.name && item.scope === input.scope)) throw new McpValidationError("duplicate");
    } catch (err) { current.error = message(err); current.failed = snapshot; return; }
    const ownGeneration = generation;
    current.saving = true; current.error = "";
    try {
      const result = await api<McpView>("/mcp/servers", { body: input });
      if (!mounted || ownGeneration !== generation) return;
      upsert(result);
      if (editor?.id !== current.id) return;
      current.version = result.version;
      const submitted = JSON.parse(snapshot) as McpDraft;
      current.draft = rebaseMcpSecrets(current.draft, submitted, result.config);
      current.saved = JSON.stringify(rebaseMcpSecrets(submitted, submitted, result.config)); current.failed = "";
      // Never replace the live draft: edits made while awaiting POST stay queued.
    } catch (err) {
      if (mounted && ownGeneration === generation && editor?.id === current.id) {
        current.error = message(err); current.failed = snapshot;
        current.conflict = err instanceof APIError && err.status === 409;
      }
    } finally {
      if (mounted && ownGeneration === generation && editor?.id === current.id) current.saving = false;
    }
  }
  $effect(() => {
    if (!editor?.version || editor.saving || editor.conflict || !dirty || validation || editor.failed === signature || remoteAuth.active) return;
    const timer = setTimeout(() => { void save(); }, 600);
    return () => clearTimeout(timer);
  });

  async function action(view: McpView, kind: "connect" | "disconnect" | "auth" | "revoke" | "delete") {
    if (remoteAuth.active || operation || dirty) return;
    if (kind === "delete" && !window.confirm(t("mcp.deleteConfirm"))) return;
    const ownGeneration = generation;
    const key = keyOf(view);
    operation = `${key}:${kind}`; error = "";
    try {
      if (kind === "delete") {
        const params = new URLSearchParams({ scope: view.scope, version: String(view.version) });
        if (view.project_id) params.set("project_id", view.project_id);
        await api(`/mcp/servers/${encodeURIComponent(view.name)}?${params}`, { method: "DELETE" });
        if (mounted && ownGeneration === generation) items = items.filter((item) => keyOf(item) !== key);
      } else {
        const result = await api<McpView | { authorization_url?: string; status: string }>(`/mcp/servers/${encodeURIComponent(view.name)}/${kind}`, {
          body: { scope: view.scope, ...(view.project_id ? { project_id: view.project_id } : {}), version: view.version }
        });
        if (!mounted || ownGeneration !== generation) return;
        if ("config" in result) upsert(result);
        if (kind === "auth") {
          pendingAuth = [...new Set([...pendingAuth, key])];
          if ("authorization_url" in result && result.authorization_url) {
            if (!isMcpHttpURL(result.authorization_url)) throw new McpValidationError("invalidURL");
            authorization = { key, url: result.authorization_url };
          }
        }
        if (kind === "revoke" || kind === "disconnect") {
          pendingAuth = pendingAuth.filter((item) => item !== key);
          if (authorization?.key === key) authorization = null;
        }
      }
      if (mounted && ownGeneration === generation) await load();
    } catch (err) {
      if (mounted && ownGeneration === generation) {
        // A failed connect/auth can still transition the manager to error.
        // Re-read its public status; never leave a stale disconnected badge.
        await load();
        if (mounted && ownGeneration === generation) error = message(err);
      }
    }
    finally { if (mounted && ownGeneration === generation) operation = ""; }
  }
  function openAuthorization() {
    // Only this explicit user click opens a URL. Never auto-navigate on auth POST.
    if (authorization && isMcpHttpURL(authorization.url)) window.open(authorization.url, "_blank", "noopener,noreferrer");
  }
  function parseImport() {
    try { importItems = parseMcpImport(importText); selectedImports = importItems.map((item) => item.name); importError = ""; }
    catch (err) { importError = message(err); importItems = []; selectedImports = []; }
  }
  function closeImport() {
    if (importing || (importText && !window.confirm(t("common.unsavedChanges")))) return;
    importOpen = false; importText = ""; importItems = []; selectedImports = []; importError = "";
  }
  async function importSelected() {
    if (remoteAuth.active || importing || !selectedImports.length) return;
    const selection = importItems.filter((item) => selectedImports.includes(item.name));
    if (selection.some((entry) => items.some((item) => item.name === entry.name && item.scope === importScope))) { importError = t("mcp.duplicate"); return; }
    if (importScope === "project" && !projectId) { importError = t("mcp.needProject"); return; }
    const ownGeneration = generation;
    const project = projectId;
    const scope = importScope;
    importing = true; importError = "";
    try {
      for (const entry of selection) {
        const result = await api<McpView>("/mcp/servers", { body: buildMcpInput(mcpDraft(entry.config, entry.name, scope), 0, project) });
        if (!mounted || ownGeneration !== generation) return;
        upsert(result); importItems = importItems.filter((item) => item.name !== entry.name);
        selectedImports = selectedImports.filter((name) => name !== entry.name);
      }
      if (!importItems.length) { importOpen = false; importText = ""; }
    } catch (err) { if (mounted && ownGeneration === generation) importError = `${t("mcp.partialImport")} ${message(err)}`; }
    finally { if (mounted && ownGeneration === generation) importing = false; }
  }
</script>

<section class="min-h-0 flex-1 overflow-y-auto p-5 max-md:p-3" aria-label={t("mcp.tab")}>
  {#if remoteAuth.active}
    <p class="rounded-md bg-surface-subtle p-4 text-sm text-muted">{t("mcp.localOnly")}</p>
  {:else}
    <p class="mb-4 text-xs leading-relaxed text-muted">{t("mcp.capability")}</p>
    <div class="mb-4 flex flex-wrap items-center gap-2">
      <Button onclick={() => openEditor()}>{t("mcp.add")}</Button>
      <Button onclick={() => { importOpen = true; }} disabled={dirty}>{t("mcp.import")}</Button>
      <Button onclick={() => { void load(); }} disabled={loading || Boolean(operation)}>{t("mcp.refresh")}</Button>
      <span class="text-xs text-muted">{t("mcp.override")}</span>
    </div>
    {#if error}<p role="alert" class="mb-3 text-xs text-danger">{error}</p>{/if}
    {#if pendingAuth.length}<p class="mb-3 text-xs text-muted">{t("mcp.authPending")}</p>{/if}
    {#if authorization}<Button class="mb-3" onclick={openAuthorization}>{t("mcp.authReady")}</Button>{/if}
    {#if loading && !items.length}<p role="status" class="text-xs text-muted">{t("common.loading")}</p>{/if}
    {#if !loading && !items.length}<p class="py-6 text-sm text-muted">{t("mcp.empty")}</p>{/if}
    <div class="grid gap-3">
      {#each items as view (keyOf(view))}
        <article class="min-w-0 rounded-md border border-border bg-surface-subtle p-4" aria-label={`${view.name} (${t(`mcp.${view.scope}`)})`}>
          <div class="flex flex-wrap items-center gap-2">
            <strong class="break-all text-sm">{view.name}</strong>
            <span class="text-xs text-muted">{t(`mcp.${view.scope}`)} · {t(`mcp.${view.config.type}`)}</span>
            <span class="rounded-sm bg-surface px-2 py-1 text-xs" role="status">{t(`mcp.status.${view.status}`)}</span>
          </div>
          {#if isShadowed(view)}<p class="mt-2 text-xs text-muted">{t("mcp.shadowed")}</p>{/if}
          {#if view.error}<p class="mt-2 text-xs text-danger">{view.error}</p>{/if}
          <div class="mt-3 flex flex-wrap gap-2">
            <Button controlSize="sm" onclick={() => openEditor(view)}>{t("common.edit")}</Button>
            <Button controlSize="sm" disabled={Boolean(operation) || dirty || (isShadowed(view) && view.status !== "connected") || (view.config.disabled && view.status !== "connected") || view.status === "unsupported" || view.status === "connecting" || view.status === "auth_pending"} onclick={() => { void action(view, view.status === "connected" ? "disconnect" : "connect"); }}>{t(view.status === "connected" ? "mcp.disconnect" : "mcp.connect")}</Button>
            {#if view.config.type === "remote" && view.config.oauth !== false}
              <Button controlSize="sm" title={fixedOAuthCallback(view) ? t("mcp.managerOAuthHint") : undefined} disabled={Boolean(operation) || dirty || isShadowed(view) || view.config.disabled || view.status === "unsupported" || fixedOAuthCallback(view)} onclick={() => { void action(view, "auth"); }}>{t("mcp.auth")}</Button>
              <Button controlSize="sm" disabled={Boolean(operation) || dirty || view.status === "unsupported"} onclick={() => { void action(view, "revoke"); }}>{t("mcp.revoke")}</Button>
            {/if}
            <Button controlSize="sm" variant="danger" disabled={Boolean(operation) || dirty} onclick={() => { void action(view, "delete"); }}>{t("common.delete")}</Button>
          </div>
          {#if view.config.type === "remote" && view.config.oauth !== false && fixedOAuthCallback(view)}<p class="mt-2 text-xs text-muted">{t("mcp.managerOAuthHint")}</p>{/if}
          <details class="mt-3 text-xs">
            <summary class="cursor-pointer text-muted">{t("mcp.tools")} ({view.tools?.length ?? 0}) · {t("mcp.prompts")} ({view.prompts?.length ?? 0}) · {t("mcp.resources")} ({view.resources?.length ?? 0})</summary>
            <p class="my-2 text-muted">{t("mcp.directoryOnly")}</p>
            {#if !view.tools?.length && !view.prompts?.length && !view.resources?.length}<p class="text-muted">{t("mcp.noCatalog")}</p>{/if}
            {#each ["tools", "prompts", "resources"] as category}
              {@const entries = category === "tools" ? view.tools : category === "prompts" ? view.prompts : view.resources}
              {#if entries?.length}<h3 class="mt-3 font-semibold">{t(`mcp.${category}`)}</h3><ul class="space-y-2 pt-2">{#each entries as entry}<li class="break-words"><strong>{entry.name}</strong>{#if "uri" in entry}<span class="ml-2 text-muted">{String(entry.uri)}</span>{/if}{#if entry.description}<p class="text-muted">{entry.description}</p>{/if}</li>{/each}</ul>{/if}
            {/each}
          </details>
        </article>
      {/each}
    </div>
  {/if}
</section>

{#if editor && !remoteAuth.active}
  <Modal title={editor.version ? `${t("common.edit")} · ${editor.draft.name}` : t("mcp.add")} onClose={closeEditor} maxWidth={820}>
    <div class="space-y-4">
      <p class="text-xs text-muted">{t(editor.version ? "mcp.autosave" : "mcp.creatingHint")}</p>
      <FieldGroup>
        <Field label={t("mcp.name")}><Input bind:value={editor.draft.name} disabled={editor.version > 0 || editor.saving} autocomplete="off" /></Field>
        <Field label={t("mcp.scope")}><Select bind:value={editor.draft.scope} disabled={editor.version > 0 || editor.saving}><option value="user">{t("mcp.user")}</option><option value="project" disabled={!projectId}>{t("mcp.project")}</option></Select></Field>
      </FieldGroup>
      <FieldGroup>
        <Field label={t("mcp.type")}><Select bind:value={editor.draft.type}><option value="local">{t("mcp.local")}</option><option value="remote">{t("mcp.remote")}</option></Select></Field>
        <Field label={t("mcp.enabled")}><Checkbox checked={!editor.draft.disabled} onchange={(event) => { if (editor) editor.draft.disabled = !event.currentTarget.checked; }} /></Field>
      </FieldGroup>
      {#if editor.draft.type === "local"}
        <Field label={t("mcp.command")}><Textarea rows={4} bind:value={editor.draft.command} spellcheck={false} /></Field>
        <Field label={t("mcp.cwd")}><Input bind:value={editor.draft.cwd} /></Field>
      {:else}
        <Field label={t("mcp.url")}><Input bind:value={editor.draft.url} type="url" /></Field>
      {/if}
      {#each (editor.draft.type === "local" ? ["environment"] : ["environment", "headers"]) as category}
        {@const rows = category === "environment" ? editor.draft.environment : editor.draft.headers}
        <section class="space-y-2" aria-label={t(`mcp.${category}`)}>
          <h3 class="text-xs font-semibold">{t(`mcp.${category}`)}</h3>
          <p class="text-xs text-muted">{t("mcp.secretHint")}</p>
          {#each rows as row, index}
            <FieldGroup>
              <Field label={`${t("mcp.key")} ${index + 1}`}><Input bind:value={row.key} autocomplete="off" /></Field>
              <Field label={`${t("mcp.value")} ${index + 1}`}><Input type="password" bind:value={row.value} placeholder={row.keepKey ? t("mcp.secretSet") : ""} autocomplete="new-password" /></Field>
              {#snippet actions()}<Button controlSize="sm" onclick={() => { if (editor) { if (category === "environment") editor.draft.environment = rows.filter((_, i) => i !== index); else editor.draft.headers = rows.filter((_, i) => i !== index); } }}>{t("mcp.remove")}</Button>{/snippet}
            </FieldGroup>
          {/each}
          <Button controlSize="sm" onclick={() => rows.push({ key: "", value: "" })}>{t("mcp.addPair")}</Button>
        </section>
      {/each}
      {#if editor.draft.type === "remote"}
        <label class="flex items-center gap-2 text-xs"><Checkbox bind:checked={editor.draft.oauthEnabled} />{t("mcp.oauth")}</label>
        <p class="text-xs text-muted">{t("mcp.managerOAuthHint")}</p>
        <p class="text-xs text-muted">{t("mcp.unsupportedOAuth")}</p>
        {#if editor.draft.oauthEnabled}
          <FieldGroup>
            {#each ["client_id", "scope", "callback_port", "redirect_uri", "auth_server_metadata_url"] as field}
              <Field label={t(`mcp.${field === "scope" ? "scopeOAuth" : field}`)}><Input value={String(editor.draft.oauth[field] ?? "")} inputmode={field === "callback_port" ? "numeric" : undefined} oninput={(event) => { if (editor) editor.draft.oauth[field] = event.currentTarget.value; }} /></Field>
            {/each}
            <Field label={t("mcp.client_secret")}><Input type="password" bind:value={editor.draft.clientSecret} placeholder={editor.draft.keepClientSecret ? t("mcp.secretSet") : ""} autocomplete="new-password" /></Field>
          </FieldGroup>
          {#if editor.draft.keepClientSecret}<Button controlSize="sm" onclick={() => { if (editor) { editor.draft.keepClientSecret = false; editor.draft.clientSecret = ""; } }}>{t("mcp.deleteSecret")}</Button>{/if}
        {/if}
      {/if}
      <FieldGroup>
        <Field label={t("mcp.protocol")}><Select bind:value={editor.draft.protocol}><option value="legacy">legacy</option><option value="auto">auto</option><option value="2026-07-28">2026-07-28</option></Select></Field>
        <Field label={t("mcp.codemode")}><Select bind:value={editor.draft.codemode}>{#each ["default", "on", "off"] as choice}<option value={choice}>{t(`mcp.${choice}`)}</option>{/each}</Select></Field>
      </FieldGroup>
      <FieldGroup columns={3}>
        {#each (editor.draft.type === "local" ? ["startup", "catalog", "execution"] as const : ["catalog", "execution"] as const) as field}
          <Field label={t(`mcp.${field}`)}><Input inputmode="numeric" bind:value={editor.draft[field]} /></Field>
        {/each}
      </FieldGroup>
      {#if validation && dirty}<p role="alert" class="text-xs text-danger">{validation}</p>{/if}
      {#if editor.error}<p role="alert" class="text-xs text-danger">{editor.error}</p>{/if}
      <p role="status" class="text-xs text-muted">{t(editor.saving ? "mcp.saving" : dirty ? "mcp.dirty" : "mcp.saved")}</p>
    </div>
    {#snippet footer()}
      {#if editor?.conflict}<Button onclick={() => { if (guard.confirm()) { editor = null; void load(); } }}>{t("mcp.reload")}</Button>{/if}
      <Button onclick={closeEditor}>{t("common.close")}</Button>
      <Button variant="primary" disabled={editor?.saving || Boolean(validation) || editor?.conflict || !dirty} onclick={() => { void save(); }}>{t(editor?.version ? "common.save" : "mcp.create")}</Button>
    {/snippet}
  </Modal>
{/if}

{#if importOpen && !remoteAuth.active}
  <Modal title={t("mcp.import")} onClose={closeImport} closeDisabled={importing} maxWidth={720}>
    <div class="space-y-4">
      <p class="text-xs text-muted">{t("mcp.importHint")}</p>
      <p class="text-xs text-muted">{t("mcp.unsupportedOAuth")}</p>
      <Field label={t("mcp.paste")}><Textarea rows={9} bind:value={importText} disabled={importing} spellcheck={false} oninput={() => { importItems = []; selectedImports = []; }} /></Field>
      <Button onclick={parseImport} disabled={importing || !importText.trim()}>{t("mcp.parse")}</Button>
      <Field label={t("mcp.scope")}><Select bind:value={importScope} disabled={importing}><option value="user">{t("mcp.user")}</option><option value="project" disabled={!projectId}>{t("mcp.project")}</option></Select></Field>
      {#if importItems.length}<fieldset class="space-y-2"><legend class="mb-2 text-xs">{t("mcp.selectServers")}</legend>{#each importItems as entry}<label class="flex items-center gap-2 text-xs"><Checkbox checked={selectedImports.includes(entry.name)} disabled={importing} onchange={(event) => { selectedImports = event.currentTarget.checked ? [...selectedImports, entry.name] : selectedImports.filter((name) => name !== entry.name); }} />{entry.name}{#if items.some((item) => item.name === entry.name && item.scope === importScope)}<span class="text-danger">{t("mcp.duplicate")}</span>{/if}</label>{/each}</fieldset>{/if}
      {#if importError}<p role="alert" class="text-xs text-danger">{importError}</p>{/if}
    </div>
    {#snippet footer()}<Button onclick={closeImport} disabled={importing}>{t("common.cancel")}</Button><Button variant="primary" disabled={importing || !selectedImports.length} onclick={() => { void importSelected(); }}>{t("mcp.importSelected")}</Button>{/snippet}
  </Modal>
{/if}
