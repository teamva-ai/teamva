<script lang="ts">
  import { Button, Input, Select, Switch, FieldGroup } from "../ui";
  import { onDestroy, onMount, untrack } from "svelte";
  import ThemeCard from "../ui/ThemeCard.svelte";
  import { APPEARANCE_PRESETS, appearanceTokens, normalizeAppearance, type AppearancePalette } from "../lib/appearanceThemes";
  import { applyTheme, createThemePreview, resolveTheme } from "../lib/theme";
  import { createSettingsAutoSave, type AutoSaveStatus } from "../lib/settingsAutoSave";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import {
    Copy,
    Download,
    Eye,
    EyeOff,
    LoaderCircle,
    MonitorCheck,
    Wifi
  } from "lucide-svelte";
  import { applyLocale, LOCALE_OPTIONS, t } from "../lib/i18n.svelte";
  import { api, APIError } from "../lib/api";
  import { queryClient } from "../lib/query";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Field from "../ui/Field.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import { useConsole } from "./shared.svelte";
  import Logs from "./Logs.svelte";
  import ProgrammingToolsPanel from "../components/ProgrammingToolsPanel.svelte";
  import ProviderRuntimeSettings from "../components/ProviderRuntimeSettings.svelte";
  import { location, navigate, registerNavigationGuard, setQueryParam } from "../lib/router.svelte";
  import { requestOnboarding } from "../lib/onboardingOverlay.svelte";
  const activeTab = $derived(new URLSearchParams(location.search).get("tab") ?? "general");
  import type {
    Computer,
    ComputerUseInstallResult,
    ComputerUseStatus,
    NetworkAccess,
    UserPreferences,
    UserPreferencesPatch
  } from "../lib/types";

  // 设置页承载本机显示偏好、远程访问与本机 Computer Use 开关。
  const context = useConsole();
  const computer = $derived(context.computer);
  let preferenceLocale = $state(context.preferences.locale);
  let preferenceTheme = $state(context.preferences.theme);
  let preferenceAppearance = $state(normalizeAppearance(context.preferences.appearance));
  const preview = createThemePreview();
  $effect(() => { preview.update(preferenceTheme, preferenceAppearance); });
  let systemMode = $state(resolveTheme("system"));
  onMount(() => {
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    const changed = () => { systemMode = resolveTheme("system"); };
    media?.addEventListener?.("change", changed);
    return () => media?.removeEventListener?.("change", changed);
  });
  const previewMode = $derived(preferenceTheme === "system" ? systemMode : preferenceTheme);
  const currentTokens = $derived(appearanceTokens(preferenceAppearance, previewMode));
  let preferenceError: unknown = $state(null);
  let preferenceStatus = $state<AutoSaveStatus>("idle");
  let preferenceRefreshed = $state(false);
  let preferenceRefreshing = $state(false);
  let preferenceRefreshError: unknown = $state(null);
  let leavingNotice = $state(false);
  let baseline: UserPreferences = context.preferences;
  type PreferenceIntent = Pick<UserPreferences, "locale" | "theme"> & { palette: AppearancePalette; accent: string | undefined; background: string | undefined; control: string | undefined };
  type ColorKey = "accent" | "background" | "control";
  let colorDraft = $state<Partial<Record<ColorKey, string>>>({});
  let preferenceIntent: Partial<PreferenceIntent> = {};
  function appearanceWithIntent(intent: Partial<PreferenceIntent>, base = baseline) {
    const appearance = { ...normalizeAppearance(base.appearance) };
    for (const key of ["palette", "accent", "background", "control"] as const) {
      if (key in intent) Object.assign(appearance, { [key]: intent[key] });
    }
    return normalizeAppearance(appearance);
  }
  const preferenceQueue = createSettingsAutoSave<PreferenceIntent, UserPreferences>({
    save: async (intent) => {
      if (context.preferences.version >= baseline.version) baseline = context.preferences;
      const patch: UserPreferencesPatch = {};
      if ("locale" in intent) patch.locale = intent.locale;
      if ("theme" in intent) patch.theme = intent.theme;
      if (["palette", "accent", "background", "control"].some((key) => key in intent)) patch.appearance = appearanceWithIntent(intent);
      return context.updatePreferences(patch);
    },
    saved: (updated) => { baseline = updated; },
    changed: ({ intent, status, error }) => {
      preferenceIntent = intent;
      preferenceLocale = intent.locale ?? baseline.locale;
      preferenceTheme = intent.theme ?? baseline.theme;
      preferenceAppearance = normalizeAppearance({ ...appearanceWithIntent(intent), ...colorDraft });
      preferenceStatus = status;
      preferenceError = error;
      if (status === "error") preferenceRefreshed = false;
      preview.update(preferenceTheme, preferenceAppearance);
      // Console's error path can rehydrate an older locale; keep the user's
      // explicit preview visible, without writing during initial hydration.
      if ("locale" in intent) applyLocale(preferenceLocale, true);
    }
  });

  function previewColor(key: ColorKey, value: string) {
    // Native palettes emit input throughout a drag, including pauses longer
    // than a debounce. Preview only: no PATCH/version/audit record until commit.
    colorDraft = { ...colorDraft, [key]: value };
    preferenceQueue.sync();
  }
  function commitColors(key?: ColorKey, value?: string) {
    if (key && value !== undefined) colorDraft = { ...colorDraft, [key]: value };
    if (!Object.keys(colorDraft).length) return;
    const appearance = appearanceWithIntent(preferenceIntent);
    const tokens = appearanceTokens(appearance, previewMode);
    const patch: Partial<PreferenceIntent> = {};
    for (const key of Object.keys(colorDraft) as ColorKey[]) {
      const effective = appearance[key] ?? tokens[key === "background" ? "canvas" : key];
      if (colorDraft[key] !== effective) patch[key] = colorDraft[key];
    }
    colorDraft = {};
    // A change followed by blur, or dragging back to the original color, is a
    // no-op. Keep real audit records rather than hiding/coalescing server logs.
    if (Object.keys(patch).length) preferenceQueue.enqueue(patch);
    else preferenceQueue.sync();
  }
  let computerUseError: unknown = $state(null);
  let accessPassword = $state("");
  let accessPasswordVisible = $state(false);
  let accessEnabled = $state(false);
  let accessStatus = $state<AutoSaveStatus>("idle");
  let accessError: unknown = $state(null);
  let accessValidation = $state(false);
  let accessNotice = $state("");

  $effect(() => {
    const value = context.preferences;
    const version = value.version;
    untrack(() => {
      if (version > baseline.version) { baseline = value; preferenceQueue.sync(); }
    });
  });

  async function refreshPreferences() {
    if (preferenceRefreshing) return;
    preferenceRefreshing = true;
    preferenceRefreshError = null;
    try {
      await queryClient.refetchQueries({ queryKey: ["user-preferences"], type: "active" }, { throwOnError: true });
      const latest = queryClient.getQueryData<UserPreferences>(["user-preferences"]);
      if (!latest) throw new Error(t("settings.preferencesUnavailable"));
      if (latest.version >= baseline.version) baseline = latest;
      preferenceQueue.sync();
      preferenceRefreshed = true;
    } catch (error) {
      preferenceRefreshError = error;
    } finally { preferenceRefreshing = false; }
  }

  function choosePalette(palette: AppearancePalette) {
    colorDraft = {};
    preferenceQueue.enqueue({ palette, accent: undefined, background: undefined, control: undefined });
  }
  function paletteKeydown(event: KeyboardEvent) {
    const keys = ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End"];
    if (!keys.includes(event.key)) return;
    event.preventDefault();
    const ids = APPEARANCE_PRESETS.map((item) => item.id);
    const current = ids.indexOf(preferenceAppearance.palette);
    const index = event.key === "Home" ? 0 : event.key === "End" ? ids.length - 1 : (current + (["ArrowLeft", "ArrowUp"].includes(event.key) ? -1 : 1) + ids.length) % ids.length;
    choosePalette(ids[index]);
    document.querySelector<HTMLElement>(`[data-palette-option="${ids[index]}"]`)?.focus({ preventScroll: true });
  }

  const computerUseMutation = createMutation<Computer, Error, boolean>(() => ({
    mutationFn: (enabled) =>
      api<Computer>(`/computers/${computer!.id}/computer-use`, {
        method: "PATCH",
        body: { enabled, expected_version: computer!.version }
      }),
    onMutate: () => (computerUseError = null),
    onSuccess: async () => {
      await context.refresh();
      await queryClient.invalidateQueries({ queryKey: ["computers"] });
    },
    onError: (error) => (computerUseError = error)
  }));

  const toggleComputerUse = (enabled: boolean) => {
    if (!computer || computerUseMutation.isPending) return;
    computerUseMutation.mutate(enabled);
  };

  const computerUseStatus = createQuery<ComputerUseStatus>(() => ({
    queryKey: ["computer-use-status", computer?.id],
    queryFn: () => api<ComputerUseStatus>(`/computers/${computer!.id}/computer-use`),
    enabled: Boolean(computer)
  }));
  const installComputerUse = createMutation<ComputerUseInstallResult, Error, void>(() => ({
    mutationFn: () =>
      api<ComputerUseInstallResult>(`/computers/${computer!.id}/computer-use`, {
        method: "POST"
      }),
    onMutate: () => (computerUseError = null),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["computer-use-status", computer?.id] }),
    onError: (error) => (computerUseError = error)
  }));

  const networkAccess = createQuery<NetworkAccess>(() => ({
    queryKey: ["network-access"],
    queryFn: () => api<NetworkAccess>("/system/network-access")
  }));
  let accessInitialized = $state(false);
  $effect(() => {
    if (!accessInitialized && networkAccess.data) {
      accessInitialized = true;
      accessEnabled = networkAccess.data.external_access;
    }
  });
  type NetworkDraft = { enabled: boolean; password: string };
  const accessValid = $derived(
    accessPassword === ""
      ? !accessEnabled || networkAccess.data?.password_set
      : accessPassword.trim() !== "" &&
          [...accessPassword].length >= 8 &&
          new TextEncoder().encode(accessPassword).length <= 72
  );
  const accessQueue = createSettingsAutoSave<NetworkDraft, NetworkAccess>({
    save: (intent) => api<NetworkAccess>("/system/network-access", {
      method: "PUT", body: { enabled: intent.enabled ?? accessEnabled, password: intent.password ?? "" }
    }),
    saved: (value, draft) => {
      queryClient.setQueryData(["network-access"], value);
      if (accessPassword === draft.password) accessPassword = "";
    },
    changed: ({ status, error }) => {
      accessStatus = status;
      accessError = error;
    }
  });
  function toggleAccess(enabled: boolean) {
    accessEnabled = enabled;
    accessValidation = false;
    // A toggle never submits a partially typed replacement password. Initial
    // password setup is committed only by Enter or leaving the password group.
    if (networkAccess.data?.can_manage && (!enabled || networkAccess.data.password_set)) {
      accessQueue.enqueue({ enabled, password: "" });
    }
  }
  function commitPassword() {
    if (!networkAccess.data?.can_manage || !accessPassword) return;
    accessValidation = !accessValid;
    if (accessValid) accessQueue.enqueue({ enabled: accessEnabled, password: accessPassword });
  }
  function retryAccess() {
    if (!networkAccess.data?.can_manage) return;
    accessValidation = !accessValid;
    if (!accessValid) return;
    // Retry the currently visible draft, never a stale replacement password
    // that the user has since edited or cleared after a failed request.
    accessQueue.enqueue({ enabled: accessEnabled, password: accessPassword });
    void accessQueue.retry();
  }
  function passwordFocusOut(event: FocusEvent) {
    if (event.relatedTarget instanceof Node && event.currentTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) return;
    commitPassword();
  }

  onMount(() => {
    const removeGuard = registerNavigationGuard((to, intent) => {
      commitColors();
      if (!preferenceQueue.pending && !accessQueue.pending) return true;
      // Project changes do not unmount Settings; preserve the original project
      // switch action instead of replacing it with a plain route navigation.
      if (intent.kind === "project") {
        void preferenceQueue.flush();
        void accessQueue.flush();
        return true;
      }
      if (preferenceQueue.failed || accessQueue.failed) return false;
      leavingNotice = true;
      // Stay on the page on failure, so its retained draft and retry are never
      // silently abandoned. Successful flushes continue the original navigation.
      void Promise.all([preferenceQueue.flush(), accessQueue.flush()]).then(() => {
        if (!preferenceQueue.pending && !accessQueue.pending) navigate(to);
      });
      return false;
    });
    const beforeUnload = (event: BeforeUnloadEvent) => {
      commitColors();
      if (!preferenceQueue.pending && !accessQueue.pending) return;
      void preferenceQueue.flush();
      void accessQueue.flush();
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => { removeGuard(); window.removeEventListener("beforeunload", beforeUnload); };
  });
  onDestroy(() => {
    commitColors();
    void Promise.all([preferenceQueue.flush(), accessQueue.flush()]).then(() => {
      if (preferenceQueue.pending) return;
      // Successful unmounts keep the applied values, rather than exposing the
      // old server theme between disposal and the last response.
      applyTheme(preferenceTheme, preferenceAppearance);
      preview.dispose();
    });
  });
  async function copyText(value: string) {
    accessNotice = "";
    try {
      await navigator.clipboard.writeText(value);
      accessNotice = t("settings.networkAccessCopied");
    } catch {
      // Addresses remain selectable when HTTP disables the Clipboard API.
      accessNotice = t("settings.networkAccessCopyHint");
    }
    setTimeout(() => (accessNotice = ""), 3_000);
  }

  const error = $derived(
    preferenceError ??
      computerUseError ??
      computerUseStatus.error ??
      installComputerUse.error ??
      networkAccess.error ??
      accessError
  );
</script>

<PageHeader title={t("settings.title")} description={t("settings.desc")} />
{#if error}
  <div class="border-b border-border px-5 py-3"><ErrorNotice {error} /></div>
{/if}

<div
  role="tablist"
  aria-label={t("settings.title")}
  class="flex gap-1 overflow-x-auto border-b border-border px-5"
>
  <button
    role="tab"
    aria-selected={activeTab === "general"}
    class="shrink-0 border-b-2 px-4 py-3 text-sm {activeTab === 'general'
      ? 'border-accent text-accent'
      : 'border-transparent text-muted'}"
    onclick={() => setQueryParam("tab", null)}>{t("work.general")}</button
  >
  <button
    role="tab"
    aria-selected={activeTab === "cli"}
    class="shrink-0 border-b-2 px-4 py-3 text-sm {activeTab === 'cli'
      ? 'border-accent text-accent'
      : 'border-transparent text-muted'}"
    onclick={() => setQueryParam("tab", "cli")}>{t("onboarding.settingsTab")}</button
  >
  <button
    role="tab"
    aria-selected={activeTab === "logs"}
    class="shrink-0 border-b-2 px-4 py-3 text-sm {activeTab === 'logs'
      ? 'border-accent text-accent'
      : 'border-transparent text-muted'}"
    onclick={() => setQueryParam("tab", "logs")}>{t("work.logs")}</button
  >
</div>
{#if activeTab === "logs"}
  <div role="tabpanel"><Logs /></div>
{:else if activeTab === "cli"}
  <div role="tabpanel" class="grid gap-5 bg-surface px-5 py-5 max-md:px-4">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <p class="max-w-[640px] text-pretty text-sm leading-[1.5] text-text-secondary">
        {t("onboarding.settingsCliDesc")}
      </p>
      <Button
        class="shrink-0"
        type="button"
        onclick={requestOnboarding}>{t("onboarding.rerun")}</Button
      >
    </div>
    {#if computer}
      <ProviderRuntimeSettings />
      <div class="overflow-hidden rounded-lg border border-border bg-surface">
        <ProgrammingToolsPanel {computer} />
      </div>
    {:else}
      <EmptyState
        title={t("programmingTools.needComputer")}
        description={t("programmingTools.needComputerDesc")}
      />
    {/if}
  </div>
{:else}
  <div role="tabpanel" class="grid min-w-0 bg-surface px-5 max-md:px-4">
    <section class="py-5">
      <div class="mb-4 flex items-start justify-between gap-4">
        <div>
          <h2 class="mb-1 text-md font-semibold tracking-[-0.01em]">
            {t("settings.personalPreferences")}
          </h2>
          <p class="text-pretty text-sm leading-[1.5] text-text-secondary">
            {t("settingsAutoSave.displayHint")}
          </p>
        </div>
      </div>
      <div class="grid gap-4">
        <FieldGroup data-field-group="display-preferences">
          <Field label={t("settings.language")}>
            <Select value={preferenceLocale} onchange={(event) => preferenceQueue.enqueue({ locale: event.currentTarget.value as UserPreferences["locale"] })}>
              {#each LOCALE_OPTIONS as option}
                <option value={option.value}>{option.label}</option>
              {/each}
            </Select>
          </Field>
          <Field label={t("settings.theme")}>
            <Select value={preferenceTheme} onchange={(event) => preferenceQueue.enqueue({ theme: event.currentTarget.value as UserPreferences["theme"] })}>
              <option value="system">{t("settings.themeSystem")}</option>
              <option value="light">{t("settings.themeLight")}</option>
              <option value="dark">{t("settings.themeDark")}</option>
            </Select>
          </Field>
        </FieldGroup>
        <div class="mt-2 grid gap-4" data-appearance-settings>
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div><h3 class="text-sm font-semibold">{t("appearance.title")}</h3><p class="mt-1 max-w-[700px] text-xs leading-relaxed text-muted">{t("settingsAutoSave.appearanceHint")}</p></div>
          </div>
          <div role="radiogroup" aria-label={t("appearance.title")} class="grid grid-cols-3 gap-3 max-xl:grid-cols-2 max-sm:grid-cols-1">
            {#each APPEARANCE_PRESETS as preset (preset.id)}
              <ThemeCard id={preset.id} label={t(preset.labelKey)} description={t(preset.descriptionKey)} tokens={appearanceTokens({ palette: preset.id }, previewMode)} selected={preferenceAppearance.palette === preset.id} tabindex={preferenceAppearance.palette === preset.id ? 0 : -1} onchange={() => choosePalette(preset.id)} onkeydown={paletteKeydown} />
            {/each}
          </div>
          <div class="grid gap-4 rounded-md border border-border bg-surface-subtle p-4">
            <div><h4 class="text-xs font-semibold">{t("appearance.customize")}</h4><p class="mt-1 text-xs leading-relaxed text-muted">{t("appearance.customizeHint")}</p></div>
            <FieldGroup columns={3} collapseAt="md" data-field-group="appearance-colors">
              <Field label={t("appearance.background")}><div class="flex items-center gap-2"><Input type="color" aria-label={t("appearance.background")} value={preferenceAppearance.background ?? currentTokens.canvas} class="w-12 cursor-pointer !p-1" oninput={(e) => previewColor("background", e.currentTarget.value)} onchange={(e) => commitColors("background", e.currentTarget.value)} onblur={() => commitColors()} /><code class="text-xs text-secondary">{preferenceAppearance.background ?? currentTokens.canvas}</code></div></Field>
              <Field label={t("appearance.accent")}><div class="flex items-center gap-2"><Input type="color" aria-label={t("appearance.accent")} value={preferenceAppearance.accent ?? currentTokens.accent} class="w-12 cursor-pointer !p-1" oninput={(e) => previewColor("accent", e.currentTarget.value)} onchange={(e) => commitColors("accent", e.currentTarget.value)} onblur={() => commitColors()} /><code class="text-xs text-secondary">{preferenceAppearance.accent ?? currentTokens.accent}</code></div></Field>
              <Field label={t("appearance.control")} hint={t("appearance.controlHint")}><div class="flex items-center gap-2"><Input type="color" aria-label={t("appearance.control")} value={preferenceAppearance.control ?? currentTokens.control} class="w-12 cursor-pointer !p-1" oninput={(e) => previewColor("control", e.currentTarget.value)} onchange={(e) => commitColors("control", e.currentTarget.value)} onblur={() => commitColors()} /><code class="text-xs text-secondary">{preferenceAppearance.control ?? currentTokens.control}</code></div></Field>
              {#snippet actions()}<Button type="button" disabled={!preferenceAppearance.accent && !preferenceAppearance.background && !preferenceAppearance.control} onclick={() => choosePalette(preferenceAppearance.palette)}>{t("appearance.resetColors")}</Button>{/snippet}
            </FieldGroup>
          </div>
        </div>
        <div class="flex flex-wrap items-center justify-end gap-3" aria-live="polite" data-preferences-autosave>
          {#if preferenceRefreshError}<ErrorNotice error={preferenceRefreshError} />{/if}
          {#if preferenceStatus === "saving"}<span role="status" class="text-2xs text-muted">{t("settingsAutoSave.saving")}</span>
          {:else if preferenceStatus === "saved" && !Object.keys(colorDraft).length}<span role="status" class="text-2xs text-success">{t("settingsAutoSave.saved")}</span>
          {:else if preferenceStatus === "error"}
            <span role="status" class="text-2xs text-warning">{t(preferenceError instanceof APIError && preferenceError.status === 409 ? (preferenceRefreshed ? "settingsAutoSave.refreshed" : "settingsAutoSave.conflict") : "settingsAutoSave.failed")}</span>
            {#if preferenceError instanceof APIError && preferenceError.status === 409 && !preferenceRefreshed}
              <Button type="button" disabled={preferenceRefreshing} onclick={refreshPreferences}>{t("settingsAutoSave.refresh")}</Button>
            {:else}<Button type="button" onclick={() => { commitColors(); preferenceRefreshed = false; void preferenceQueue.retry(); }}>{t("settingsAutoSave.retry")}</Button>{/if}
          {/if}
        </div>
        {#if leavingNotice}<p role="status" class="text-2xs text-muted">{t("settingsAutoSave.leaving")}</p>{/if}
      </div>
    </section>

    <section class="border-t border-border py-5">
      <div class="mb-4 flex items-start justify-between gap-4">
        <div>
          <h2 class="mb-1 flex items-center gap-2 text-md font-semibold tracking-[-0.01em]">
            <MonitorCheck size={15} class="text-accent" />{t("settings.computerUse")}
          </h2>
          <p class="max-w-[640px] text-pretty text-sm leading-[1.5] text-text-secondary">
            {t("settings.computerUseDesc")}
          </p>
        </div>
      </div>
      {#if !computer}
        <EmptyState
          title={t("settings.computerUseNeedComputer")}
          description={t("settings.computerUseNeedComputerDesc")}
        />
      {:else}
        <div class="grid grid-cols-2 items-center gap-4 max-md:grid-cols-1">
          <div>
            <strong class="block text-sm font-semibold">{t("settings.computerUse")}</strong>
            <p class="mt-1 text-pretty text-2xs leading-relaxed text-muted">
              {computer.computer_use_enabled
                ? t("settings.computerUseHint")
                : t("settings.computerUseDisabledHint")}
            </p>
            <p class="mt-2 text-2xs leading-relaxed text-muted">
              {#if computerUseStatus.isLoading}
                <LoaderCircle size={12} class="mr-1 inline animate-spin" />{t(
                  "settings.computerUseChecking"
                )}
              {:else if computerUseStatus.data?.binary.installed}
                <span class="inline-flex items-center text-accent">
                  <MonitorCheck size={12} class="mr-1" />{t("settings.computerUseInstalled", {
                    version: computerUseStatus.data.binary.version ?? "?"
                  })}
                </span>
                <span class="block">{t("settings.computerUseInstallHint")}</span>
              {:else}
                <span class="text-disabled">{t("settings.computerUseNotInstalled")}</span>
                {#if computerUseStatus.data?.binary.error}
                  <span class="mt-0.5 block text-[10px] leading-relaxed text-muted"
                    >{computerUseStatus.data.binary.error}</span
                  >
                {/if}
                <span class="block">{t("settings.computerUseInstallHint")}</span>
              {/if}
            </p>
          </div>
          <div class="flex justify-end gap-2">
            {#if computer.computer_use_enabled}
              <Button
                type="button"
                disabled={computerUseMutation.isPending}
                onclick={() => toggleComputerUse(false)}
              >
                {#if computerUseMutation.isPending}<LoaderCircle
                    size={14}
                    class="animate-spin"
                  />{t("settings.computerUseSaving")}{:else}{t("settings.computerUseDisable")}{/if}
              </Button>
            {:else}
              <Button
                variant="primary"
                type="button"
                disabled={computerUseMutation.isPending}
                onclick={() => toggleComputerUse(true)}
              >
                {#if computerUseMutation.isPending}<LoaderCircle
                    size={14}
                    class="animate-spin"
                  />{t("settings.computerUseSaving")}{:else}{t("settings.computerUseEnable")}{/if}
              </Button>
            {/if}
            {#if !computerUseStatus.data?.binary.installed}
              <Button
                type="button"
                disabled={installComputerUse.isPending}
                onclick={() => installComputerUse.mutate()}
              >
                {#if installComputerUse.isPending}<LoaderCircle size={14} class="animate-spin" />{t(
                    "settings.computerUseInstalling"
                  )}{:else}<Download size={14} />{t("settings.computerUseInstall")}{/if}
              </Button>
            {/if}
          </div>
        </div>
      {/if}
    </section>

    <section class="border-t border-border py-5">
      <div class="mb-4 flex items-start justify-between gap-4">
        <div>
          <h2 class="mb-1 flex items-center gap-2 text-md font-semibold tracking-[-0.01em]">
            <Wifi size={15} class="text-accent" />{t("settings.networkAccess")}
          </h2>
          <p class="max-w-[640px] text-pretty text-sm leading-[1.5] text-text-secondary">
            {t("settings.networkAccessDesc")}
          </p>
        </div>
        <span
          class="shrink-0 rounded-full border border-border px-2 py-0.5 text-2xs font-medium {networkAccess
            .data?.external_access
            ? 'text-success'
            : 'text-muted'}"
        >
          {networkAccess.data?.external_access
            ? t("settings.networkAccessEnabled")
            : t("settings.networkAccessDisabled")}
        </span>
      </div>

      {#if networkAccess.isLoading}
        <p class="text-2xs text-muted">
          <LoaderCircle size={12} class="mr-1 inline animate-spin" />{t("common.loading")}
        </p>
      {:else if networkAccess.data}
        {#if !networkAccess.data?.external_access}
          <p class="mb-3 text-2xs leading-relaxed text-muted">
            {t("settings.networkAccessDisabledHint")}
          </p>
        {:else}
          <ul class="mb-3 grid gap-1">
            {#each networkAccess.data.external_urls as url (url)}
              <li class="flex items-center gap-2">
                <code
                  class="min-w-0 flex-1 select-text break-all rounded-sm border border-border bg-surface-subtle px-2 py-1 text-2xs"
                  >{url}</code
                >
                <button
                  class="inline-grid size-7 shrink-0 cursor-pointer place-items-center rounded-sm border border-border bg-surface p-0 text-secondary hover:bg-surface-hover hover:text-text"
                  type="button"
                  aria-label={t("settings.networkAccessCopy")}
                  title={t("settings.networkAccessCopy")}
                  onclick={() => copyText(url)}><Copy size={12} /></button
                >
              </li>
            {/each}
          </ul>
        {/if}

        {#if networkAccess.data.listen_error}<p role="alert" class="mb-3 text-2xs text-warning">
            {t("settings.networkAccessListenError")}
          </p>{/if}
        {#if !networkAccess.data.can_manage}<p class="mb-3 text-2xs text-muted">
            {t("settings.networkAccessLocalOnly")}
          </p>{/if}
        <form
          class="grid max-w-[640px] gap-3"
          onsubmit={(event) => {
            event.preventDefault();
            commitPassword();
          }}
        >
          <label class="flex cursor-pointer items-center gap-3 text-sm font-medium">
            <Switch
              type="checkbox"
              role="switch"
              checked={accessEnabled}
              onchange={(event) => toggleAccess(event.currentTarget.checked)}
              disabled={!networkAccess.data.can_manage}
            />
            {t("settings.networkAccessToggle")}
          </label>
          <div class="text-sm text-text-secondary">
            {t("settings.networkAccessPort")}：<code>{networkAccess.data.port}</code>
            <p class="mt-1 text-2xs text-muted">{t("settings.networkAccessPortHint")}</p>
          </div>
          <Field
            label={t("settings.networkAccessPassword")}
            hint={t("settingsAutoSave.passwordHint")}
          >
            <div class="flex items-center gap-2" onfocusout={passwordFocusOut}>
              <Input
                type={accessPasswordVisible ? "text" : "password"}
                bind:value={accessPassword}
                oninput={() => { accessValidation = false; }}
                onkeydown={(event) => { if (event.key === "Enter") { event.preventDefault(); commitPassword(); } }}
                autocomplete="new-password"
                spellcheck={false}
                maxlength={72}
                placeholder={networkAccess.data.password_set
                  ? t("settings.networkAccessPasswordKeep")
                  : ""}
                aria-label={t("settings.networkAccessPassword")}
                disabled={!networkAccess.data.can_manage}
                class="min-w-0 flex-1"
              />
              <Button
                icon
                controlSize="md"
                type="button"
                aria-label={accessPasswordVisible
                  ? t("settings.networkAccessHide")
                  : t("settings.networkAccessShow")}
                title={accessPasswordVisible
                  ? t("settings.networkAccessHide")
                  : t("settings.networkAccessShow")}
                onpointerdown={(event) => event.preventDefault()}
                onclick={() => (accessPasswordVisible = !accessPasswordVisible)}
                >{#if accessPasswordVisible}<EyeOff size={14} />{:else}<Eye
                    size={14}
                  />{/if}</Button
              >
            </div>
          </Field>
          {#if accessValidation}<p role="alert" class="text-2xs text-warning">{t("settingsAutoSave.passwordInvalid")}</p>
          {:else if accessEnabled && !networkAccess.data.password_set && accessStatus !== "saving"}<p role="status" class="text-2xs text-muted">{t("settingsAutoSave.passwordPending")}</p>{/if}
          {#if accessNotice}<p class="text-2xs text-muted">{accessNotice}</p>{/if}
          <p class="text-2xs leading-relaxed text-warning">{t("settings.networkAccessWarning")}</p>
          <div class="flex flex-wrap items-center justify-end gap-3" aria-live="polite" data-access-autosave>
            {#if accessStatus === "saving"}<span role="status" class="text-2xs text-muted">{t("settingsAutoSave.saving")}</span>
            {:else if accessStatus === "saved" && !accessPassword}<span role="status" class="text-2xs text-success">{t("settingsAutoSave.saved")}</span>
            {:else if accessStatus === "error"}<span role="status" class="text-2xs text-warning">{t("settingsAutoSave.failed")}</span>
              {#if networkAccess.data.can_manage}<Button type="button" onclick={retryAccess}>{t("settingsAutoSave.retry")}</Button>{/if}
            {:else if networkAccess.data.listen_error && networkAccess.data.can_manage}
              <Button type="button" onclick={() => accessQueue.enqueue({ enabled: accessEnabled, password: "" })}>{t("settingsAutoSave.retry")}</Button>
            {/if}
          </div>
        </form>
      {/if}
    </section>
  </div>
{/if}
