import { writeSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";

const OPEN_TIMEOUT_MS = 10_000;
const COMMAND_TIMEOUT_MS = 15_000;
const FETCH_COMMAND_TIMEOUT_MS = 5_000;
const FETCH_SETTLE_TIMEOUT_MS = 10_000;
const FETCH_TIMEOUT_MS = 3_000;
const EVALUATION_TIMEOUT_MS = 60_000;
const redactions = [];

const required = (name) => {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`missing ${name}`);
  return value;
};

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function waitFor(label, probe, timeout = 30_000) {
  const deadline = Date.now() + timeout;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const value = await probe();
      if (value) return value;
    } catch (error) {
      lastError = error;
    }
    await sleep(100);
  }
  throw new Error(`${label} timed out${lastError instanceof Error ? `: ${lastError.message}` : ""}`);
}

async function fetchJSONWithTimeout(url, options = {}, timeout = FETCH_TIMEOUT_MS) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error("fetch timed out")), timeout);
  try {
    const signal = options.signal
      ? AbortSignal.any([options.signal, controller.signal])
      : controller.signal;
    const response = await fetch(url, { ...options, signal });
    const payload = await response.json();
    return { response, payload };
  } finally {
    clearTimeout(timer);
  }
}

class CDPClient {
  constructor(url) {
    this.socket = new WebSocket(url);
    this.nextId = 1;
    this.pending = new Map();
    this.listeners = new Map();
    this.closedError = undefined;

    this.socket.addEventListener("message", (event) => this.handleMessage(event));
    this.socket.addEventListener("error", () => {
      this.failPending(new Error("Chrome CDP connection failed"));
    });
    this.socket.addEventListener("close", () => {
      this.failPending(new Error("Chrome CDP connection closed"));
    });
  }

  async open(timeout = OPEN_TIMEOUT_MS) {
    if (this.socket.readyState === WebSocket.OPEN) return;
    await new Promise((resolve, reject) => {
      let settled = false;
      const finish = (callback, value) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        this.socket.removeEventListener("open", onOpen);
        this.socket.removeEventListener("error", onError);
        this.socket.removeEventListener("close", onClose);
        callback(value);
      };
      const onOpen = () => finish(resolve);
      const onError = () => finish(reject, new Error("Chrome CDP connection failed"));
      const onClose = () => finish(reject, new Error("Chrome CDP connection closed before opening"));
      const timer = setTimeout(() => {
        finish(reject, new Error("Chrome CDP connection open timed out"));
        try {
          this.socket.close();
        } catch {
          // The open timeout is already authoritative.
        }
      }, timeout);
      this.socket.addEventListener("open", onOpen, { once: true });
      this.socket.addEventListener("error", onError, { once: true });
      this.socket.addEventListener("close", onClose, { once: true });
    });
  }

  handleMessage(event) {
    let message;
    try {
      message = JSON.parse(String(event.data));
    } catch {
      this.failPending(new Error("Chrome CDP returned an invalid message"));
      this.close();
      return;
    }
    if (message.id !== undefined) {
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      clearTimeout(pending.timer);
      if (message.error) pending.reject(new Error(message.error.message || "Chrome CDP command failed"));
      else pending.resolve(message.result ?? {});
      return;
    }
    for (const listener of this.listeners.get(message.method) ?? []) {
      try {
        listener(message.params ?? {});
      } catch {
        // A test listener must not destabilize the CDP transport.
      }
    }
  }

  failPending(error) {
    this.closedError = error;
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer);
      pending.reject(error);
    }
    this.pending.clear();
  }

  on(method, listener) {
    const listeners = this.listeners.get(method) ?? [];
    listeners.push(listener);
    this.listeners.set(method, listeners);
  }

  send(method, params = {}, timeout = COMMAND_TIMEOUT_MS) {
    if (this.closedError) return Promise.reject(this.closedError);
    if (this.socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error("Chrome CDP connection is not open"));
    }
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        if (!this.pending.delete(id)) return;
        reject(new Error(`Chrome CDP command ${method} timed out`));
      }, timeout);
      this.pending.set(id, { resolve, reject, timer });
      try {
        this.socket.send(JSON.stringify({ id, method, params }));
      } catch {
        this.pending.delete(id);
        clearTimeout(timer);
        reject(new Error(`Chrome CDP command ${method} could not be sent`));
      }
    });
  }

  close() {
    this.failPending(new Error("Chrome CDP connection was closed by the smoke test"));
    try {
      if (this.socket.readyState === WebSocket.CONNECTING || this.socket.readyState === WebSocket.OPEN) {
        this.socket.close();
      }
    } catch {
      // The shell owns the bounded Chrome process shutdown.
    }
  }
}

function validateRuntimeOrigin(raw) {
  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error("invalid Chrome runtime origin");
  }
  const loopbacks = new Set(["127.0.0.1", "[::1]", "::1"]);
  const port = Number(parsed.port);
  if (parsed.protocol !== "http:" || !loopbacks.has(parsed.hostname) || !parsed.port ||
      !Number.isInteger(port) || port < 1 || port > 65535 || parsed.username || parsed.password ||
      parsed.search || parsed.hash || parsed.pathname !== "/" || raw !== parsed.origin) {
    throw new Error("invalid Chrome runtime origin");
  }
  return parsed.origin;
}

function runtimeURL(raw, origin) {
  try {
    const parsed = new URL(raw);
    return parsed.origin === origin ? parsed : undefined;
  } catch {
    return undefined;
  }
}

const pageNetworkProtocols = new Set(["http:", "https:", "ws:", "wss:"]);

function classifyPageNetworkURL(raw, targetOrigins) {
  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    return /^(?:https?|wss?):/iu.test(String(raw)) ? "forbidden" : "non-network";
  }
  if (!pageNetworkProtocols.has(parsed.protocol)) return "non-network";
  if (parsed.username || parsed.password || !targetOrigins.has(parsed.origin)) return "forbidden";
  return "target";
}

function diffChangeId(raw, origin) {
  const parsed = runtimeURL(raw, origin);
  if (!parsed) return undefined;
  const match = parsed.pathname.match(/\/git\/repositories\/[^/]+\/changes\/([^/]+)\/diff$/u);
  if (!match) return undefined;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return undefined;
  }
}

function safeFailureMessage(error) {
  let message = error instanceof Error ? error.message : "unexpected CDP failure";
  for (const value of redactions) {
    if (value) message = message.split(value).join("<redacted>");
  }
  message = message.replace(/nonce=[^&\s)]+/giu, "nonce=<redacted>");
  return message.slice(0, 600);
}

async function main() {
  const profileDirectory = required("TEAMVA_CHROME_PROFILE_DIR");
  const origin = validateRuntimeOrigin(required("TEAMVA_CHROME_ORIGIN"));
  const alphaRoot = required("TEAMVA_CHROME_ALPHA_ROOT");
  const betaRoot = required("TEAMVA_CHROME_BETA_ROOT");
  const screenshotPath = required("TEAMVA_CHROME_SCREENSHOT_PATH");
  let forbiddenTexts;
  let expectedText;
  try {
    forbiddenTexts = JSON.parse(required("TEAMVA_CHROME_FORBIDDEN_TEXTS"));
    expectedText = JSON.parse(required("TEAMVA_CHROME_EXPECTED_TEXT"));
  } catch {
    throw new Error("invalid browser smoke JSON configuration");
  }
  const singleLabel = required("TEAMVA_CHROME_SINGLE_LABEL");
  const stackedLabel = required("TEAMVA_CHROME_STACKED_LABEL");
  const stackedSummary = required("TEAMVA_CHROME_STACKED_SUMMARY");

  if (!Array.isArray(expectedText) ||
      expectedText.some((item) => typeof item !== "string" || !item)) {
    throw new Error("invalid expected browser text");
  }
  if (!Array.isArray(forbiddenTexts) ||
      forbiddenTexts.some((item) => typeof item !== "string" || !item)) {
    throw new Error("invalid forbidden browser text");
  }
  const forbiddenVariants = [...new Set(forbiddenTexts.flatMap((item) => [
    item,
    encodeURI(item),
    encodeURIComponent(item),
  ]))];
  redactions.push(alphaRoot, betaRoot, screenshotPath, profileDirectory, ...forbiddenVariants);

  const activePort = await waitFor("Chrome DevToolsActivePort", async () => {
    const content = await readFile(`${profileDirectory}/DevToolsActivePort`, "utf8");
    const [portText] = content.trim().split(/\r?\n/u);
    const port = Number(portText);
    return /^\d+$/u.test(portText) && Number.isInteger(port) && port > 0 && port <= 65535 &&
      String(port) === portText ? portText : "";
  });

  const target = await waitFor("unique about:blank Chrome page target", async () => {
    const { response, payload: targets } = await fetchJSONWithTimeout(
      `http://127.0.0.1:${activePort}/json/list`,
      { cache: "no-store" },
    );
    if (!response.ok) return undefined;
    if (!Array.isArray(targets)) throw new Error("Chrome target list is malformed");
    const pages = targets.filter((item) => item?.type === "page" && item.webSocketDebuggerUrl);
    const blanks = pages.filter((item) => item.url === "about:blank");
    if (pages.length > 1 || blanks.length > 1) {
      throw new Error("Chrome exposed multiple page targets");
    }
    return pages.length === 1 && blanks.length === 1 ? blanks[0] : undefined;
  });

  let debuggerURL;
  try {
    debuggerURL = new URL(target.webSocketDebuggerUrl);
  } catch {
    throw new Error("Chrome page target has an invalid debugger URL");
  }
  if (debuggerURL.protocol !== "ws:" || debuggerURL.hostname !== "127.0.0.1" ||
      debuggerURL.port !== activePort || debuggerURL.username || debuggerURL.password ||
      debuggerURL.search || debuggerURL.hash || !debuggerURL.pathname.startsWith("/devtools/page/")) {
    throw new Error("Chrome page target debugger URL is not exact loopback CDP");
  }

  const client = new CDPClient(debuggerURL.href);
  await client.open();

  const runtimeErrors = [];
  const consoleErrors = [];
  const requestURLs = new Map();
  const requestKinds = new Map();
  const failedRequests = [];
  const forbiddenNetworkRequests = [];
  const forbiddenNetworkRequestIds = new Set();
  const forbiddenFetchRequests = [];
  const forbiddenFetchRequestIds = new Set();
  const fetchCommandErrors = [];
  const pausedFetchRequestIds = new Set();
  const pendingFetchCommands = new Set();
  const successfulDiffIds = new Set();
  const activePageNetworkRequests = new Set();
  const targetNetworkOrigins = new Set([origin, origin.replace(/^http:/u, "ws:")]);
  let interceptedFetchRequests = 0;
  let targetFetchRequests = 0;
  let lastPageNetworkActivity = Date.now();

  const finishObservedRequest = (requestKey) => {
    if (activePageNetworkRequests.delete(requestKey)) lastPageNetworkActivity = Date.now();
  };
  const recordForbiddenNetworkRequest = (requestKey) => {
    if (!forbiddenNetworkRequestIds.has(requestKey)) {
      forbiddenNetworkRequestIds.add(requestKey);
      forbiddenNetworkRequests.push("non-target page network request");
    }
    lastPageNetworkActivity = Date.now();
  };
  const recordForbiddenFetchRequest = (requestId) => {
    if (forbiddenFetchRequestIds.has(requestId)) return;
    forbiddenFetchRequestIds.add(requestId);
    forbiddenFetchRequests.push("non-target page network request blocked by Fetch");
  };
  const resolvePausedFetchRequest = (requestId, method, params = {}) => {
    if (!requestId) {
      fetchCommandErrors.push("Fetch interception lacked a request id");
      return;
    }
    if (pausedFetchRequestIds.has(requestId)) {
      fetchCommandErrors.push("Fetch interception was resolved more than once");
      return;
    }
    pausedFetchRequestIds.add(requestId);
    let task;
    task = client.send(method, { requestId, ...params }, FETCH_COMMAND_TIMEOUT_MS)
      .catch((error) => {
        fetchCommandErrors.push(`${method} failed: ${safeFailureMessage(error)}`);
      })
      .finally(() => {
        pausedFetchRequestIds.delete(requestId);
        pendingFetchCommands.delete(task);
      });
    pendingFetchCommands.add(task);
  };

  client.on("Runtime.exceptionThrown", () => {
    runtimeErrors.push("runtime exception");
  });
  client.on("Runtime.consoleAPICalled", ({ type }) => {
    if (type === "error" || type === "assert") consoleErrors.push("console error");
  });
  client.on("Fetch.requestPaused", ({ requestId, request }) => {
    interceptedFetchRequests += 1;
    if (typeof request?.url !== "string" || !request.url) {
      fetchCommandErrors.push("Fetch interception lacked a request URL");
      resolvePausedFetchRequest(requestId, "Fetch.failRequest", { errorReason: "BlockedByClient" });
      return;
    }
    const kind = classifyPageNetworkURL(request.url, targetNetworkOrigins);
    if (kind === "forbidden") {
      recordForbiddenFetchRequest(requestId || "request-without-id");
      resolvePausedFetchRequest(requestId, "Fetch.failRequest", { errorReason: "BlockedByClient" });
      return;
    }
    if (kind === "target") targetFetchRequests += 1;
    resolvePausedFetchRequest(requestId, "Fetch.continueRequest");
  });
  client.on("Network.requestWillBeSent", ({ requestId, request, type, frameId }) => {
    if (!requestId || !request?.url) return;
    finishObservedRequest(requestId);
    requestURLs.set(requestId, request.url);
    const kind = classifyPageNetworkURL(request.url, targetNetworkOrigins);
    requestKinds.set(requestId, kind);
    if (kind === "forbidden") {
      recordForbiddenNetworkRequest(requestId);
      return;
    }
    const parsed = kind === "target" ? runtimeURL(request.url, origin) : undefined;
    if (!parsed || type === "EventSource" || parsed.pathname.endsWith("/git/events")) return;
    if (/^\/assets\/worker-[A-Za-z0-9_-]+\.js$/u.test(parsed.pathname)) {
      // Chrome reports this page-created Worker request on the page session,
      // but its completion belongs to the Worker target and is not mirrored
      // back as Network.loadingFinished.
      requestURLs.delete(requestId);
      requestKinds.delete(requestId);
      return;
    }
    // A page-target session can observe a Worker script request without
    // receiving that subtarget's matching loadingFinished event. Keep enforcing
    // URL policy above, but only use frame-owned requests for the settle barrier.
    if (!frameId) {
      requestURLs.delete(requestId);
      requestKinds.delete(requestId);
      return;
    }
    activePageNetworkRequests.add(requestId);
    lastPageNetworkActivity = Date.now();
  });
  client.on("Network.loadingFinished", ({ requestId }) => {
    finishObservedRequest(requestId);
    requestURLs.delete(requestId);
    requestKinds.delete(requestId);
  });
  client.on("Network.loadingFailed", ({ requestId, errorText, canceled }) => {
    const raw = requestURLs.get(requestId) ?? "";
    const kind = requestKinds.get(requestId) ?? classifyPageNetworkURL(raw, targetNetworkOrigins);
    const parsed = kind === "target" ? runtimeURL(raw, origin) : undefined;
    finishObservedRequest(requestId);
    requestURLs.delete(requestId);
    requestKinds.delete(requestId);
    if (kind === "forbidden" || !parsed || canceled || errorText === "net::ERR_ABORTED") return;
    failedRequests.push(`${parsed.pathname}: request failed`);
  });
  client.on("Network.responseReceived", ({ requestId, response }) => {
    const kind = classifyPageNetworkURL(response?.url ?? "", targetNetworkOrigins);
    if (kind === "forbidden") recordForbiddenNetworkRequest(requestId || "response-without-id");
    const parsed = runtimeURL(response?.url ?? "", origin);
    if (!parsed) return;
    const changeId = response.status === 200 ? diffChangeId(response.url, origin) : undefined;
    if (changeId) successfulDiffIds.add(changeId);
    if (response.status >= 400 && !parsed.pathname.endsWith("/favicon.ico")) {
      failedRequests.push(`${parsed.pathname}: HTTP ${response.status}`);
    }
  });
  client.on("Network.webSocketCreated", ({ requestId, url }) => {
    if (!requestId || classifyPageNetworkURL(url ?? "", targetNetworkOrigins) !== "forbidden") return;
    recordForbiddenNetworkRequest(`websocket:${requestId}`);
  });
  client.on("Network.webSocketClosed", ({ requestId }) => {
    if (requestId) finishObservedRequest(`websocket:${requestId}`);
  });
  client.on("Network.webTransportCreated", ({ transportId, url }) => {
    if (!transportId || classifyPageNetworkURL(url ?? "", targetNetworkOrigins) !== "forbidden") return;
    recordForbiddenNetworkRequest(`webtransport:${transportId}`);
  });
  client.on("Network.webTransportClosed", ({ transportId }) => {
    if (transportId) finishObservedRequest(`webtransport:${transportId}`);
  });

  const evaluate = async (expression, timeout = COMMAND_TIMEOUT_MS) => {
    const result = await client.send("Runtime.evaluate", {
      expression,
      awaitPromise: true,
      returnByValue: true,
    }, timeout);
    if (result.exceptionDetails) {
      throw new Error(result.exceptionDetails.exception?.description ||
        result.exceptionDetails.text || "browser evaluation failed");
    }
    return result.result?.value;
  };

  const assertNoObservedErrors = (stage) => {
    if (runtimeErrors.length || consoleErrors.length || failedRequests.length ||
        forbiddenNetworkRequests.length || forbiddenFetchRequests.length || fetchCommandErrors.length ||
        pausedFetchRequestIds.size || pendingFetchCommands.size || activePageNetworkRequests.size) {
      throw new Error(`browser runtime, console, or network-policy errors were observed ${stage}`);
    }
    if (interceptedFetchRequests === 0 || targetFetchRequests === 0) {
      throw new Error(`Fetch did not intercept target-origin page requests ${stage}`);
    }
  };

  try {
    // Register listeners before enabling the domains, then navigate the sole
    // about:blank target. Fetch pauses every page request before dispatch.
    await Promise.all([
      client.send("Runtime.enable"),
      client.send("Page.enable"),
      client.send("Network.enable"),
      client.send("Fetch.enable", {
        patterns: [{ urlPattern: "*", requestStage: "Request" }],
      }),
    ]);

    const navigation = await client.send("Page.navigate", { url: new URL("/workbench", origin).href });
    if (navigation.errorText) throw new Error("local workbench navigation failed");

    await waitFor("authenticated browser origin", async () => {
      const state = await evaluate(`({
        origin: location.origin,
        ready: document.readyState,
        consoleReady: Boolean(document.querySelector("main") && document.querySelector("nav"))
      })`);
      return state?.origin === origin && state.ready === "complete" && state.consoleReady;
    });

    const fixture = await evaluate(`(async () => {
      const api = async (path, options = {}) => {
        const method = options.method || "GET";
        const headers = { Accept: "application/json", ...(options.headers || {}) };
        if (options.csrf) headers["X-CSRF-Token"] = options.csrf;
        if (options.body !== undefined) headers["Content-Type"] = "application/json";
        const response = await fetch("/v1" + path, {
          method,
          headers,
          body: options.body === undefined ? undefined : JSON.stringify(options.body),
          cache: "no-store",
          credentials: "same-origin",
          signal: AbortSignal.timeout(10_000),
        });
        const payload = await response.json();
        if (!response.ok) {
          throw new Error(method + " " + path + " returned HTTP " + response.status +
            " (" + (payload?.error?.code || "unknown") + ")");
        }
        return payload.data;
      };
      const auth = await api("/auth/bootstrap");
      const computers = await api("/computers");
      const computer = computers.items?.[0];
      if (!computer) throw new Error("local computer is unavailable");
      const preferences = await api("/me/preferences");
      await api("/me/preferences", {
        method: "PATCH",
        csrf: auth.csrf_token,
        body: { expected_version: preferences.version, locale: "zh-CN" },
      });
      const project = await api("/projects", {
        method: "POST",
        csrf: auth.csrf_token,
        body: { computer_id: computer.id, name: "Chrome Changes 验收", description: "真实双仓库 UI" },
      });
      const register = async (localPath, name) => {
        const command = await api("/computers/" + encodeURIComponent(computer.id) + "/resource-register", {
          method: "POST",
          csrf: auth.csrf_token,
          body: { path: localPath },
        });
        let result;
        for (let attempt = 0; attempt < 100; attempt += 1) {
          result = await api("/computers/" + encodeURIComponent(computer.id) +
            "/directory-list/" + encodeURIComponent(command.command_id));
          if (result.status === "completed") break;
          await new Promise((resolve) => setTimeout(resolve, 50));
        }
        if (result?.status !== "completed") throw new Error("resource registration did not complete");
        const resource = await api("/projects/" + encodeURIComponent(project.id) + "/resources", {
          method: "POST",
          csrf: auth.csrf_token,
          body: { kind: "code_directory", name, path: result.result.path },
        });
        const validated = await api("/projects/" + encodeURIComponent(project.id) +
          "/resources/" + encodeURIComponent(resource.id) + "/validate", {
          method: "POST",
          csrf: auth.csrf_token,
          body: { expected_version: resource.version },
        });
        if (validated.status !== "ready" || validated.git_detected !== true) {
          throw new Error("Git resource validation failed");
        }
      };
      await register(${JSON.stringify(alphaRoot)}, "Alpha");
      await register(${JSON.stringify(betaRoot)}, "Beta");
      const repositories = await api("/projects/" + encodeURIComponent(project.id) +
        "/git/repositories?refresh=true");
      return {
        computerId: computer.id,
        projectId: project.id,
        truncated: repositories.truncated,
        warnings: repositories.warnings,
        repositories: repositories.repositories.map((item) => ({
          rootDisplay: item.root_display,
          scope: item.scope,
          state: item.state,
          unstaged: item.counts?.unstaged,
        })),
      };
    })()`, EVALUATION_TIMEOUT_MS);

    const repositories = [...(fixture?.repositories ?? [])].sort((left, right) =>
      left.rootDisplay.localeCompare(right.rootDisplay));
    if (fixture?.truncated || !Array.isArray(fixture?.warnings) || fixture.warnings.length ||
        repositories.length !== 2 ||
        repositories[0]?.rootDisplay !== "Alpha" || repositories[0]?.scope !== "full" ||
        repositories[0]?.state !== "dirty" || repositories[0]?.unstaged !== 8 ||
        repositories[1]?.rootDisplay !== "Beta" || repositories[1]?.scope !== "full" ||
        repositories[1]?.state !== "dirty" || repositories[1]?.unstaged !== 1) {
      // This projection is deliberately path-free and safe to retain in CI
      // diagnostics; never include repository IDs, warning messages, or roots.
      throw new Error(`browser fixture repository summary is incorrect: ${JSON.stringify({
        truncated: fixture?.truncated,
        warningCount: Array.isArray(fixture?.warnings) ? fixture.warnings.length : -1,
        repositories,
      })}`);
    }
    if (repositories.some((repository) => forbiddenVariants.some((text) =>
      repository.rootDisplay.includes(text)))) {
      throw new Error("browser fixture repository summary exposed a local path");
    }

    await evaluate(`sessionStorage.setItem("teamva-selection", ${JSON.stringify(JSON.stringify({
      computerId: fixture.computerId,
      projectId: fixture.projectId,
    }))})`);
    const changesNavigation = await client.send("Page.navigate", { url: `${origin}/changes` });
    if (changesNavigation.errorText) throw new Error("Changes navigation failed");

    const initialState = await waitFor("multi-repository Changes UI", async () => {
      const state = await evaluate(`(() => {
        const openShadowRoots = (root) => {
          const roots = [];
          const visit = (scope) => {
            for (const element of scope.querySelectorAll("*")) {
              if (!element.shadowRoot) continue;
              roots.push(element.shadowRoot);
              visit(element.shadowRoot);
            }
          };
          visit(root);
          return roots;
        };
        const innerText = document.body?.innerText || "";
        const outerHTML = document.documentElement?.outerHTML || "";
        const shadowRoots = openShadowRoots(document);
        const shadowText = shadowRoots.map((root) => root.textContent || "").join(" ");
        const shadowHTML = shadowRoots.map((root) => root.innerHTML || "").join(" ");
        const expected = ${JSON.stringify(expectedText)};
        const forbidden = ${JSON.stringify(forbiddenVariants)};
        if (location.pathname !== "/changes" || document.readyState !== "complete" ||
            !expected.every((text) => innerText.includes(text))) return undefined;
        return {
          innerLeak: forbidden.some((text) => innerText.includes(text) || shadowText.includes(text)),
          htmlLeak: forbidden.some((text) => outerHTML.includes(text) || shadowHTML.includes(text)),
        };
      })()`);
      return state;
    });
    if (initialState.innerLeak || initialState.htmlLeak) {
      throw new Error("Changes UI exposed an absolute fixture path");
    }

    const selectedAlpha = await evaluate(`(() => {
      const row = [...document.querySelectorAll('button[role="treeitem"]')]
        .find((item) => item.textContent?.includes("alpha-01.txt"));
      if (!row) return false;
      row.click();
      return true;
    })()`);
    if (!selectedAlpha) throw new Error("Alpha change row was not rendered");
    await waitFor("Alpha change selection", async () => evaluate(`
      [...document.querySelectorAll('button[role="treeitem"]')].some((item) =>
        item.textContent?.includes("alpha-01.txt") && item.getAttribute("aria-selected") === "true")
    `));

    const singleClicked = await evaluate(`(() => {
      const wanted = ${JSON.stringify(singleLabel)};
      const button = [...document.querySelectorAll("button")]
        .find((item) => item.textContent?.trim() === wanted);
      if (!button) return false;
      button.click();
      return true;
    })()`);
    if (!singleClicked) throw new Error("single Diff control was not rendered");
    await waitFor("single Diff mode", async () => evaluate(`(() => {
      const wanted = ${JSON.stringify(singleLabel)};
      const button = [...document.querySelectorAll("button")]
        .find((item) => item.textContent?.trim() === wanted);
      const cards = [...document.querySelectorAll("article[data-diff-change-id]")];
      return button?.getAttribute("aria-pressed") === "true" && cards.length === 1 &&
        cards[0]?.querySelector("header strong")?.textContent?.trim() === "alpha-01.txt";
    })()`));

    const waitForRenderedDiff = async (filename, number) => {
      const probe = `(() => {
        const openShadowRoots = (root) => {
          const roots = [];
          const visit = (scope) => {
            for (const element of scope.querySelectorAll("*")) {
              if (!element.shadowRoot) continue;
              roots.push(element.shadowRoot);
              visit(element.shadowRoot);
            }
          };
          visit(root);
          return roots;
        };
        const card = [...document.querySelectorAll("article[data-diff-change-id]")]
          .find((item) => item.querySelector("header strong")?.textContent?.trim() === ${JSON.stringify(filename)});
        if (!card) return false;
        const shadowRoots = openShadowRoots(card);
        const text = [card.innerText || "", ...shadowRoots.map((root) => root.textContent || "")]
          .join(" ").replace(/\\s+/gu, " ");
        const compactText = text.replace(/\\s+/gu, "");
        const pierreRendered = shadowRoots.some((root) => root.querySelector("pre") &&
          root.querySelector("[data-line], [data-code]"));
        return Boolean(pierreRendered &&
          (text.includes(${JSON.stringify(`base ${number}`)}) || compactText.includes(${JSON.stringify(`base${number}`)})) &&
          (text.includes(${JSON.stringify(`changed ${number}`)}) || compactText.includes(${JSON.stringify(`changed${number}`)})));
      })()`;
      try {
        return await waitFor(`${filename} real Diff`, async () => evaluate(probe));
      } catch {
        const diagnostics = await evaluate(`(() => {
          const openShadowRoots = (root) => {
            const roots = [];
            const visit = (scope) => {
              for (const element of scope.querySelectorAll("*")) {
                if (!element.shadowRoot) continue;
                roots.push(element.shadowRoot);
                visit(element.shadowRoot);
              }
            };
            visit(root);
            return roots;
          };
          const cards = [...document.querySelectorAll("article[data-diff-change-id]")];
          const card = cards.find((item) =>
            item.querySelector("header strong")?.textContent?.trim() === ${JSON.stringify(filename)});
          if (!card) return { cardCount: cards.length, cardExists: false };
          const shadowRoots = openShadowRoots(card);
          const normalText = (card.innerText || "").replace(/\\s+/gu, " ");
          const shadowText = shadowRoots.map((root) => root.textContent || "")
            .join(" ").replace(/\\s+/gu, " ");
          const compactShadowText = shadowText.replace(/\\s+/gu, "");
          const shadowHTML = shadowRoots.map((root) => root.innerHTML || "").join(" ");
          const compactShadowHTML = shadowHTML.replace(/\\s+/gu, "");
          const host = card.querySelector(".git-pierre-diff");
          return {
            cardCount: cards.length,
            cardExists: true,
            shadowRootCount: shadowRoots.length,
            shadowHosts: shadowRoots.map((root) => root.host?.tagName || "").filter(Boolean),
            hasPre: shadowRoots.some((root) => root.querySelector("pre")),
            hasDataLine: shadowRoots.some((root) => root.querySelector("[data-line], [data-code]")),
            normalHasBase: normalText.includes(${JSON.stringify(`base ${number}`)}),
            normalHasChanged: normalText.includes(${JSON.stringify(`changed ${number}`)}),
            shadowHasBase: shadowText.includes(${JSON.stringify(`base ${number}`)}),
            shadowHasChanged: shadowText.includes(${JSON.stringify(`changed ${number}`)}),
            compactShadowHasBase: compactShadowText.includes(${JSON.stringify(`base${number}`)}),
            compactShadowHasChanged: compactShadowText.includes(${JSON.stringify(`changed${number}`)}),
            shadowHTMLHasBase: shadowHTML.includes(${JSON.stringify(`base ${number}`)}) ||
              compactShadowHTML.includes(${JSON.stringify(`base${number}`)}),
            shadowHTMLHasChanged: shadowHTML.includes(${JSON.stringify(`changed ${number}`)}) ||
              compactShadowHTML.includes(${JSON.stringify(`changed${number}`)}),
            normalTextLength: normalText.length,
            shadowTextLength: shadowText.length,
            rendererHostChildren: host?.childElementCount ?? -1,
            loading: Boolean(card.querySelector('.animate-spin, [aria-busy="true"]')),
            alert: Boolean(card.querySelector('[role="alert"]')),
          };
        })()`);
        throw new Error(`${filename} real Diff timed out: ${JSON.stringify(diagnostics)}`);
      }
    };

    await waitForRenderedDiff("alpha-01.txt", "01");

    const clicked = await evaluate(`(() => {
      const wanted = ${JSON.stringify(stackedLabel)};
      const button = [...document.querySelectorAll("button")]
        .find((item) => item.textContent?.trim() === wanted);
      if (!button) return false;
      button.click();
      return true;
    })()`);
    if (!clicked) throw new Error("stacked Diff control was not rendered");

    await waitFor("stacked Diff threshold", async () => evaluate(`(() => {
      const wanted = ${JSON.stringify(stackedLabel)};
      const summary = ${JSON.stringify(stackedSummary)};
      const button = [...document.querySelectorAll("button")]
        .find((item) => item.textContent?.trim() === wanted);
      return button?.getAttribute("aria-pressed") === "true" &&
        (document.body?.innerText || "").includes(summary);
    })()`));

    const verifiedStackedCardIds = [];
    for (let index = 0; index < 6; index += 1) {
      const number = String(index + 1).padStart(2, "0");
      const scrolled = await evaluate(`(() => {
        const scroller = document.querySelector(
          '[data-testid="changes-page"] .changes-workspace > div:last-child > section > div.overflow-auto'
        );
        const wrappers = [...document.querySelectorAll('[data-lazy-diff-key]')];
        const wrapper = wrappers[${index}];
        if (!(scroller instanceof HTMLElement) || !(wrapper instanceof HTMLElement) || wrappers.length !== 6) {
          return false;
        }
        wrapper.scrollIntoView({ block: "center", inline: "nearest" });
        scroller.dispatchEvent(new Event("scroll"));
        return true;
      })()`);
      if (!scrolled) throw new Error(`stacked Diff ${number} could not be scrolled into view`);
      await waitForRenderedDiff(`alpha-${number}.txt`, number);
      const cardId = await evaluate(`(() => {
        const card = [...document.querySelectorAll("article[data-diff-change-id]")]
          .find((item) => item.querySelector("header strong")?.textContent?.trim() ===
            ${JSON.stringify(`alpha-${number}.txt`)});
        return card?.getAttribute("data-diff-change-id") || "";
      })()`);
      if (!cardId || verifiedStackedCardIds.includes(cardId)) {
        throw new Error(`stacked Diff ${number} did not expose a unique change ID`);
      }
      verifiedStackedCardIds.push(cardId);
    }
    const stackedCardIds = await waitFor("six mounted stacked Diffs", async () => evaluate(`(() => {
      const cards = [...document.querySelectorAll("article[data-diff-change-id]")];
      const ids = cards.map((card) => card.getAttribute("data-diff-change-id") || "");
      return ids.length === 6 && ids.every(Boolean) && new Set(ids).size === 6 ? ids : undefined;
    })()`));

    await waitFor("six unique successful Diff responses", async () =>
      verifiedStackedCardIds.length === 6 && stackedCardIds.length === 6 &&
      new Set([...verifiedStackedCardIds, ...stackedCardIds]).size === 6 &&
      verifiedStackedCardIds.every((id) => successfulDiffIds.has(id)) && successfulDiffIds.size >= 6);

    const scrolledToStart = await evaluate(`(() => {
      const scroller = document.querySelector(
        '[data-testid="changes-page"] .changes-workspace > div:last-child > section > div.overflow-auto'
      );
      if (!(scroller instanceof HTMLElement)) return false;
      scroller.scrollTop = 0;
      scroller.dispatchEvent(new Event("scroll"));
      return true;
    })()`);
    if (!scrolledToStart) throw new Error("stacked Diff scroller could not return to the start");
    await waitForRenderedDiff("alpha-01.txt", "01");

    await waitFor("Fetch interception commands to settle", async () =>
      pendingFetchCommands.size === 0 && pausedFetchRequestIds.size === 0,
    FETCH_SETTLE_TIMEOUT_MS);
    try {
      await waitFor("browser page network activity to settle", async () =>
        activePageNetworkRequests.size === 0 && Date.now() - lastPageNetworkActivity >= 300, 15_000);
    } catch {
      const activeRoutes = [...activePageNetworkRequests].map((requestId) => {
        const parsed = runtimeURL(requestURLs.get(requestId) ?? "", origin);
        return parsed?.pathname.replace(/\/(?:proj|repo|res|chg|gcs)_[^/]+/gu, "/:id") ?? "unknown";
      });
      throw new Error(`browser page network activity did not settle: ${JSON.stringify({
        activeCount: activePageNetworkRequests.size,
        activeRoutes: [...new Set(activeRoutes)].sort(),
      })}`);
    }

    const inspectFinalDOM = () => evaluate(`(() => {
      const openShadowRoots = (root) => {
        const roots = [];
        const visit = (scope) => {
          for (const element of scope.querySelectorAll("*")) {
            if (!element.shadowRoot) continue;
            roots.push(element.shadowRoot);
            visit(element.shadowRoot);
          }
        };
        visit(root);
        return roots;
      };
      const page = document.querySelector('[data-testid="changes-page"]');
      const scroller = document.querySelector(
        '[data-testid="changes-page"] .changes-workspace > div:last-child > section > div.overflow-auto'
      );
      const diffShell = scroller?.parentElement;
      const cards = [...document.querySelectorAll("article[data-diff-change-id]")];
      const ids = cards.map((card) => card.getAttribute("data-diff-change-id") || "");
      const firstCard = cards.find((item) =>
        item.querySelector("header strong")?.textContent?.trim() === "alpha-01.txt");
      const firstDiffReady = (() => {
        if (!firstCard) return false;
        const shadowRoots = openShadowRoots(firstCard);
        const text = [firstCard.innerText || "", ...shadowRoots.map((root) => root.textContent || "")]
          .join(" ").replace(/\\s+/gu, " ");
        const compactText = text.replace(/\\s+/gu, "");
        const pierreRendered = shadowRoots.some((root) => root.querySelector("pre") &&
          root.querySelector("[data-line], [data-code]"));
        return pierreRendered &&
          (text.includes("base 01") || compactText.includes("base01")) &&
          (text.includes("changed 01") || compactText.includes("changed01"));
      })();
      const wanted = ${JSON.stringify(stackedLabel)};
      const stacked = [...document.querySelectorAll("button")]
        .find((item) => item.textContent?.trim() === wanted);
      const innerText = document.body?.innerText || "";
      const outerHTML = document.documentElement?.outerHTML || "";
      const shadowRoots = openShadowRoots(document);
      const shadowText = shadowRoots.map((root) => root.textContent || "").join(" ");
      const shadowHTML = shadowRoots.map((root) => root.innerHTML || "").join(" ");
      const forbidden = ${JSON.stringify(forbiddenVariants)};
      return {
        pageReady: Boolean(page) && location.pathname === "/changes" && document.readyState === "complete",
        stacked: stacked?.getAttribute("aria-pressed") === "true",
        scrolledToStart: scroller instanceof HTMLElement && scroller.scrollTop <= 1,
        firstDiffReady,
        sixUniqueCards: ids.length === 6 && ids.every(Boolean) && new Set(ids).size === 6,
        loading: Boolean(diffShell?.querySelector('.animate-spin, [aria-busy="true"]')),
        error: Boolean(page?.querySelector('[role="alert"]')),
        innerLeak: forbidden.some((text) => innerText.includes(text) || shadowText.includes(text)),
        htmlLeak: forbidden.some((text) => outerHTML.includes(text) || shadowHTML.includes(text)),
        ids,
      };
    })()`);

    const assertFinalDOM = (state, stage) => {
      if (!state?.pageReady || !state.stacked || !state.scrolledToStart || !state.firstDiffReady ||
          !state.sixUniqueCards || state.loading || state.error) {
        throw new Error(`Changes UI was loading, failed, or incomplete ${stage}`);
      }
      if (state.innerLeak || state.htmlLeak) {
        throw new Error(`Changes UI innerText or outerHTML exposed a fixture path ${stage}`);
      }
      if (!state.ids.every((id) => successfulDiffIds.has(id) && verifiedStackedCardIds.includes(id))) {
        throw new Error(`Changes UI lacked six matching HTTP 200 Diff responses ${stage}`);
      }
    };

    assertNoObservedErrors("before the screenshot");
    assertFinalDOM(await inspectFinalDOM(), "before the screenshot");

    const screenshot = await client.send("Page.captureScreenshot", {
      format: "png",
      fromSurface: true,
      captureBeyondViewport: false,
    }, 20_000);
    try {
      await writeFile(screenshotPath, Buffer.from(screenshot.data, "base64"));
    } catch {
      throw new Error("Chrome screenshot could not be written");
    }

    await sleep(300);
    assertFinalDOM(await inspectFinalDOM(), "after the screenshot");
    assertNoObservedErrors("after the screenshot");
  } finally {
    client.close();
  }

}

let exitCode = 0;
let finalMessage = "";
try {
  await main();
} catch (error) {
  exitCode = 1;
  finalMessage = `FAIL: ${safeFailureMessage(error)}\n`;
}
if (finalMessage) {
  try {
    writeSync(2, finalMessage);
  } catch {
    exitCode = 1;
  }
}
process.exit(exitCode);
