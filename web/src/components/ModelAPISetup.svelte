<script lang="ts">
  import { Button, Input, Select } from "../ui";
  import { createQuery } from "@tanstack/svelte-query";
  import { LoaderCircle, CheckCircle2 } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import { queryClient } from "../lib/query";
  import ErrorNotice from "./ErrorNotice.svelte";
  import Field from "../ui/Field.svelte";
  import ModelPicker from "../ui/ModelPicker.svelte";

  export interface ConfiguredModelProvider {
    preset_id: string; name: string; base_url: string; model: string; model_ref: string; key_configured: boolean;
  }
  interface Settings {
    presets: Array<{ id: string; name: string; base_url: string; protocol: string; models: string[] }>;
    configured: ConfiguredModelProvider[];
  }
  let { onSaved, onReadyChange, onBusyChange }: {
    onSaved: (value: ConfiguredModelProvider) => Promise<void>;
    onReadyChange: (ready: boolean) => void;
    onBusyChange: (busy: boolean) => void;
  } = $props();
  const path = "/system/programming-tools/opencode/model-providers";
  const settings = createQuery<Settings>(() => ({ queryKey: ["model-provider-settings", "opencode"], queryFn: () => api(path) }));
  let presetID = $state("");
  let baseURL = $state("");
  let model = $state("");
  let apiKey = $state(""); // Session memory only. Never storage, logs or query data.
  let busy = $state<"save" | "test" | "">("");
  let error = $state<unknown>();
  let savedFingerprint = $state("");
  let testResult = $state<{ fingerprint: string; ok: boolean; code: string; duration_ms: number }>();
  const preset = $derived(settings.data?.presets.find((item) => item.id === presetID));
  const existing = $derived(settings.data?.configured.find((item) => item.preset_id === presetID && item.base_url === baseURL.trim().replace(/\/+$/, "")));
  const fingerprint = $derived(JSON.stringify([presetID, baseURL.trim(), model.trim(), apiKey]));
  const ready = $derived(Boolean(presetID && baseURL.trim() && model.trim() && !busy));
  $effect(() => { onReadyChange(Boolean(savedFingerprint && savedFingerprint === fingerprint && !busy)); });
  $effect(() => { onBusyChange(Boolean(busy)); });

  function selectPreset(value: string) {
    presetID = value;
    const selected = settings.data?.presets.find((item) => item.id === value);
    const saved = settings.data?.configured.find((item) => item.preset_id === value);
    baseURL = saved?.base_url ?? selected?.base_url ?? "";
    model = saved?.model ?? selected?.models[0] ?? "";
    apiKey = "";
    error = undefined;
    testResult = undefined;
  }

  async function submit(action: "save" | "test") {
    if (!ready) return;
    const snapshot = { preset_id: presetID, base_url: baseURL.trim(), model: model.trim(), api_key: apiKey };
    const submittedFingerprint = fingerprint;
    busy = action;
    error = undefined;
    try {
      if (action === "test") {
        const result = await api<{ ok: boolean; code: string; duration_ms: number }>(path + "/test", { body: snapshot });
        testResult = { ...result, fingerprint: submittedFingerprint };
      } else {
        const result = await api<ConfiguredModelProvider>(path, { method: "PUT", body: snapshot });
        queryClient.setQueryData<Settings>(["model-provider-settings", "opencode"], (old) => old ? {
          ...old, configured: [...old.configured.filter((item) => item.preset_id !== result.preset_id), result]
        } : old);
        await onSaved(result);
        if (fingerprint === submittedFingerprint) {
          apiKey = "";
          savedFingerprint = JSON.stringify([presetID, baseURL.trim(), model.trim(), ""]);
        }
      }
    } catch (caught) { error = caught; }
    finally { busy = ""; }
  }
  const testCodes = new Set(["success", "authentication_failed", "rate_limited", "request_failed", "unreachable", "timeout", "invalid_response"]);
</script>

<!-- Modal owns the content width and inset; do not add a second form card/inset. -->
<div class="grid min-w-0 w-full gap-4 text-left">
  <p class="text-sm leading-relaxed text-text-secondary">{t("onboarding.apiDescription")}</p>
  {#if settings.error}<ErrorNotice error={settings.error} onRetry={() => void settings.refetch()} />{/if}
  {#if error}<ErrorNotice {error} />{/if}
  <form class="grid min-w-0 w-full gap-4" onsubmit={(event) => { event.preventDefault(); void submit("save"); }}>
    <fieldset class="grid min-w-0 gap-4 border-0 p-0" disabled={Boolean(busy) || !settings.data} aria-busy={Boolean(busy)}>
      <Field label={t("onboarding.apiProvider")}>
        <Select aria-label={t("onboarding.apiProvider")} value={presetID} onchange={(event) => selectPreset(event.currentTarget.value)}>
          <option value="">{settings.isLoading ? t("common.loading") : "—"}</option>
          {#each settings.data?.presets ?? [] as item (item.id)}<option value={item.id}>{item.id === "custom" ? t("onboarding.apiCustom") : item.name}</option>{/each}
        </Select>
      </Field>
      <Field label={t("onboarding.apiBaseURL")}>
        <Input type="url" aria-label={t("onboarding.apiBaseURL")} bind:value={baseURL} required maxlength={2048} placeholder="https://api.example.com/v1" />
      </Field>
      <Field label={t("onboarding.apiModel")} hint={t("onboarding.apiModelHint")}>
        <ModelPicker models={preset?.models ?? []} bind:value={model} label={t("onboarding.apiModel")} />
      </Field>
      <Field label="API Key">
        <Input type="password" aria-label="API Key" bind:value={apiKey} autocomplete="new-password" spellcheck={false} maxlength={8192} placeholder={existing?.key_configured ? t("onboarding.apiKeyRetained") : ""} />
      </Field>
      <p class="text-2xs leading-relaxed text-muted">{t("onboarding.apiPrivacy")}</p>
      <div class="flex min-w-0 flex-wrap justify-end gap-3">
        <Button type="button" disabled={!ready} onclick={() => void submit("test")}>
          {#if busy === "test"}<LoaderCircle size={14} class="animate-spin" />{/if}{t("onboarding.apiTest")}
        </Button>
        <Button type="submit" variant="primary" disabled={!ready}>
          {#if busy === "save"}<LoaderCircle size={14} class="animate-spin" />{/if}{t("onboarding.apiSave")}
        </Button>
      </div>
    </fieldset>
  </form>
  {#if testResult?.fingerprint === fingerprint}
    <p role={testResult.ok ? "status" : "alert"} class="text-xs leading-relaxed {testResult.ok ? 'text-success' : 'text-danger'}">
      {t(`onboarding.apiTest_${testCodes.has(testResult.code) ? testResult.code : "request_failed"}`)}
      {#if testResult.ok}<span class="ml-1 tabular-nums">({testResult.duration_ms} ms)</span>{/if}
    </p>
  {/if}
  {#if savedFingerprint && savedFingerprint === fingerprint && !busy}
    <p class="flex items-center gap-2 text-xs text-success" role="status"><CheckCircle2 size={14} />{t("onboarding.generalSaved")}</p>
  {/if}
</div>
