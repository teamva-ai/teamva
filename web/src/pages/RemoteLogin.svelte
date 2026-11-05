<script lang="ts">
  import brandIcon from "../../../assets/brand/icon.png";
  import { Button, Checkbox, Field, Input } from "../ui";
  import { onMount } from "svelte";
  import { ArrowRight, Eye, EyeOff, LoaderCircle, LockKeyhole } from "lucide-svelte";
  import { api, APIError } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { remoteReturnPath } from "../lib/remoteAuth.svelte";

  let password = $state("");
  let visible = $state(false);
  let busy = $state(false);
  let error = $state("");
  let trustDevice = $state(false);

  function continueToWorkbench() {
    window.location.replace(remoteReturnPath(new URLSearchParams(window.location.search).get("next")));
  }

  onMount(() => {
    const controller = new AbortController();
    // A bookmarked login URL should also honor this browser's existing trust.
    void api<void>("/auth/remote-session", { signal: controller.signal }).then(() => {
      if (!controller.signal.aborted) continueToWorkbench();
    }).catch(() => { /* Anonymous browsers stay on the password form. */ });
    return () => controller.abort();
  });

  async function login(event: SubmitEvent) {
    event.preventDefault();
    if (busy || !password) return;
    busy = true;
    error = "";
    try {
      await api<void>("/auth/remote-session", { method: "POST", body: { password, trust_device: trustDevice } });
      password = "";
      // Reload within this origin, clearing any prior workbench query caches.
      continueToWorkbench();
    } catch (cause) {
      error = cause instanceof APIError && cause.code === "remote_password_invalid"
        ? t("remoteLogin.invalid")
        : cause instanceof APIError && cause.status === 429 ? t("remoteLogin.limited") : t("remoteLogin.failed");
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>{t("remoteLogin.title")} · Teamva</title></svelte:head>
<main class="grid min-h-dvh place-items-center bg-canvas px-5 py-10">
  <section class="grid w-full max-w-[420px] gap-7 rounded-xl border border-border bg-surface p-7 shadow-sm max-sm:p-5" aria-labelledby="remote-login-title">
    <div class="flex items-center gap-2.5 text-md font-semibold tracking-tight">
      <img src={brandIcon} alt="" class="size-7" />Teamva
    </div>
    <div>
      <h1 id="remote-login-title" class="mb-2 flex items-center gap-2 text-xl font-semibold tracking-tight"><LockKeyhole size={20} class="text-accent" />{t("remoteLogin.title")}</h1>
      <p class="text-sm leading-relaxed text-text-secondary">{t("remoteLogin.description")}</p>
    </div>
    <form class="grid gap-4" onsubmit={login}>
      <Field id="remote-password" label={t("remoteLogin.password")}>
        <div class="flex items-center gap-2">
          <Input id="remote-password" name="password" type={visible ? "text" : "password"} bind:value={password} autocomplete="current-password" required maxlength={72} disabled={busy} class="min-w-0 flex-1" />
          <Button type="button" icon controlSize="md" aria-label={visible ? t("settings.networkAccessHide") : t("settings.networkAccessShow")} onclick={() => (visible = !visible)}>
            {#if visible}<EyeOff size={16} />{:else}<Eye size={16} />{/if}
          </Button>
        </div>
      </Field>
      <div class="grid gap-1.5">
        <label class="flex min-h-9 cursor-pointer items-center gap-2.5 text-sm">
          <Checkbox type="checkbox" bind:checked={trustDevice} disabled={busy} aria-describedby="remote-trust-hint" />
          {t("remoteLogin.trustDevice")}
        </label>
        <p id="remote-trust-hint" class="text-2xs leading-relaxed text-muted">{t("remoteLogin.trustDeviceHint")}</p>
      </div>
      {#if error}<p role="alert" class="text-sm text-danger">{error}</p>{/if}
      <Button variant="primary" type="submit" disabled={!password || busy}>
        {#if busy}<LoaderCircle size={16} class="animate-spin" />{t("remoteLogin.signingIn")}{:else}{t("remoteLogin.signIn")}<ArrowRight size={16} />{/if}
      </Button>
    </form>
  </section>
</main>
