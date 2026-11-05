<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { location } from "./lib/router.svelte";
  import { api } from "./lib/api";
  import { setCsrf } from "./lib/stores.svelte";
  import { navigate } from "./lib/router.svelte";
  import { onboardingOverlay, requestOnboarding } from "./lib/onboardingOverlay.svelte";
  import type { Onboarding, User } from "./lib/types";
  import { t } from "./lib/i18n.svelte";
  import Loading from "./components/Loading.svelte";
  import ErrorNotice from "./components/ErrorNotice.svelte";
  import Toast from "./components/Toast.svelte";
  import RemoteLogin from "./pages/RemoteLogin.svelte";
  import Modal from "./ui/Modal.svelte";
  import { remoteAuth } from "./lib/remoteAuth.svelte";

  interface AuthBootstrap {
    csrf_token: string;
    user: User;
  }

  let ConsoleShellComponent = $state<typeof import("./pages/ConsoleShell.svelte").default | null>(
    null
  );
  let OnboardingComponent = $state<typeof import("./pages/Onboarding.svelte").default | null>(null);
  let shellError = $state<Error | null>(null);
  let shellLoad: Promise<void> | null = null;
  let onboardingLoad: Promise<void> | null = null;

  function loadShell(): Promise<void> {
    if (shellLoad) return shellLoad;
    shellError = null;
    shellLoad = import("./pages/ConsoleShell.svelte")
      .then((module) => { ConsoleShellComponent = module.default; })
      .catch((error) => { shellError = error instanceof Error ? error : new Error(t("shell.consoleError")); })
      .finally(() => { shellLoad = null; });
    return shellLoad;
  }

  function loadOnboarding(): Promise<void> {
    if (onboardingLoad) return onboardingLoad;
    onboardingLoad = import("./pages/Onboarding.svelte")
      .then((module) => { OnboardingComponent = module.default; })
      .catch((error) => { shellError = error instanceof Error ? error : new Error(t("shell.consoleError")); })
      .finally(() => { onboardingLoad = null; });
    return onboardingLoad;
  }

  function retryShell() {
    shellError = null;
    if (!ConsoleShellComponent) void loadShell();
    if (onboardingOpen && !OnboardingComponent) void loadOnboarding();
  }

  const me = createQuery<User>(() => ({
    queryKey: ["me"],
    enabled: location.path !== "/remote/login",
    queryFn: async () => {
      const restored = await api<AuthBootstrap>("/auth/local-session", {
        method: "POST",
        body: {}
      });
      if (!restored.csrf_token) throw new Error(t("errors.invalidResponse"));
      setCsrf(restored.csrf_token);
      return restored.user;
    },
    retry: false,
    staleTime: 30_000
  }));

  $effect(() => {
    if (me.data && location.path === "/") {
      navigate("/workbench", { replace: true });
    }
  });

  const onboarding = createQuery<Onboarding>(() => ({
    queryKey: ["onboarding"],
    enabled: Boolean(me.data) && location.path !== "/remote/login",
    queryFn: () => api<Onboarding>("/system/onboarding"),
    staleTime: 5_000,
    refetchInterval: 15_000
  }));

  // The guide is a modal over the running app: the default entry point opens it
  // automatically, an explicit request (banner / settings / /onboarding alias)
  // opens it over the current page.
  const defaultEntry = $derived(location.path === "/" || location.path === "/workbench");
  const onboardingOpen = $derived(
    (Boolean(onboarding.data?.needed) && defaultEntry) || onboardingOverlay.requested
  );

  $effect(() => {
    // Once the automatic guide opens, only Finish/Skip may close it. Saving
    // Lumi makes `needed` false on the next poll, but setup still has more steps.
    if (defaultEntry && onboarding.data?.needed && !onboarding.data.dismissed && !onboardingOverlay.requested) {
      requestOnboarding();
    }
  });

  $effect(() => {
    if (location.path !== "/onboarding") return;
    requestOnboarding();
    navigate("/workbench", { replace: true });
  });

  $effect(() => {
    if (!onboardingOpen || !me.data || OnboardingComponent) return;
    void loadOnboarding();
  });

  $effect(() => {
    if (location.path !== "/remote/login" && me.data && !ConsoleShellComponent) {
      void loadShell();
    }
  });

  $effect(() => {
    if (!remoteAuth.active || location.path === "/remote/login") return;
    // Also notice revocation/expiry on a page whose only live traffic is SSE.
    const check = () => {
      void api<void>("/auth/remote-session").catch(() => {});
    };
    const timer = setInterval(check, 15_000);
    window.addEventListener("focus", check);
    return () => {
      clearInterval(timer);
      window.removeEventListener("focus", check);
    };
  });
</script>

<div class="contents"><Toast /></div>
{#if location.path === "/remote/login"}
  <RemoteLogin />
{:else if me.isLoading}
  <Loading label={t("common.loading")} />
{:else if me.isError || shellError}
  <main class="grid min-h-dvh place-items-center bg-canvas p-5">
    <section class="my-6 grid w-full max-w-[560px] gap-5">
      <ErrorNotice
        error={me.error ?? shellError!}
        onRetry={() => (me.isError ? me.refetch() : retryShell())}
      />
    </section>
  </main>
{:else}
  <div class="contents" data-console-background>
    {#if ConsoleShellComponent}
      <ConsoleShellComponent />
    {:else}
      <Loading label={t("common.loading")} />
    {/if}
  </div>
  {#if onboardingOpen}
    <div class="contents">
      {#if OnboardingComponent}
        <OnboardingComponent />
      {:else}
        <Modal title={t("onboarding.title")} dismissible={false} onClose={() => {}} maxWidth={860} layer={90} centered>
          <Loading label={t("common.loading")} />
        </Modal>
      {/if}
    </div>
  {/if}
{/if}
