import { v4 } from "uuid";
import { session, clearSession, setCsrf } from "./stores.svelte";
import { localeInfo, t } from "./i18n.svelte";
import { localizedAPIErrorMessage, unknownAPIErrorMessage } from "./localizedMessages";
import type { Envelope } from "./types";
import { remoteAuth, requireRemoteLogin } from "./remoteAuth.svelte";
import { workClientHeaders } from "./workRecords";

export interface APIErrorBody {
  error?: {
    code?: string;
    message?: string;
    retryable?: boolean;
    details?: Record<string, unknown>;
  };
  meta?: { request_id?: string };
}

export class APIError extends Error {
  code: string;
  status: number;
  retryable: boolean;
  requestId?: string;
  details?: Record<string, unknown>;
  /** Seconds the client should wait before retrying (parsed from Retry-After). */
  retryAfter?: number;

  constructor(status: number, body: APIErrorBody, retryAfter?: string | null) {
    const code = body.error?.code ?? "http_error";
    super(
      localizedAPIErrorMessage(code, status) ??
      unknownAPIErrorMessage(body.error?.message, status)
    );
    this.name = "APIError";
    this.code = code;
    this.status = status;
    this.retryable = body.error?.retryable ?? status >= 500;
    this.requestId = body.meta?.request_id;
    this.details = body.error?.details;
    if (retryAfter) {
      const seconds = Number.parseInt(retryAfter, 10);
      if (Number.isFinite(seconds) && seconds > 0) {
        this.retryAfter = seconds;
      }
    }
  }
}

const stateChangingMethods = new Set(["POST", "PUT", "PATCH", "DELETE"]);

// Browser and Wails use the same local HTTP contract. No remote-origin build
// profile: a desktop build must never send local data to a configured cloud API.
export const apiBaseURL = "/v1";

interface RequestOptions {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

async function request<T>(path: string, options: RequestOptions): Promise<T> {
  const method = options.method ?? (options.body === undefined ? "GET" : "POST");
  const normalizedMethod = method.toUpperCase();
  const headers: Record<string, string> = {
    "Accept": "application/json",
    "Accept-Language": localeInfo.value,
    ...workClientHeaders(),
    ...(options.body === undefined ? {} : { "Content-Type": "application/json" }),
    ...options.headers
  };
  if (stateChangingMethods.has(normalizedMethod)) {
    if (session.csrf) {
      headers["X-CSRF-Token"] = session.csrf;
    }
  }
  const response = await fetch(`${apiBaseURL}${path}`, {
    method,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    headers,
    signal: options.signal,
    credentials: "include"
  });
  if (response.headers.get("X-Teamva-Remote") === "true") remoteAuth.active = true;
  if (response.status === 204) {
    return undefined as T;
  }
  let parsed: Envelope<T> | APIErrorBody;
  try {
    parsed = (await response.json()) as Envelope<T> | APIErrorBody;
  } catch {
    throw new APIError(response.status, {
      error: {
        code: "invalid_response",
        message: t("errors.invalidResponse")
      }
    }, response.headers.get("Retry-After"));
  }
  if (!response.ok) {
    const errorBody = parsed as APIErrorBody;
    if (response.status === 401 && errorBody.error?.code === "remote_auth_required") {
      clearSession();
      requireRemoteLogin();
    }
    if (
      response.status === 401 &&
      ["unauthenticated", "invalid_session"].includes(errorBody.error?.code ?? "")
    ) {
      clearSession();
    }
    throw new APIError(response.status, errorBody, response.headers.get("Retry-After"));
  }
  return (parsed as Envelope<T>).data;
}

let csrfRefresh: Promise<void> | undefined;

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  try {
    return await request<T>(path, options);
  } catch (error) {
    // Two fresh tabs can race to establish the shared HttpOnly cookie while
    // retaining different in-memory CSRF values. csrf_failed is emitted before
    // any handler runs, so exactly one retry after restoring the cookie's token
    // is safe, even for mutations without an idempotency key.
    if (!(error instanceof APIError) || error.code !== "csrf_failed") throw error;
    csrfRefresh ??= request<{ csrf_token: string }>("/auth/csrf", {
      signal: AbortSignal.timeout(10_000)
    }).then((restored) => {
      if (!restored.csrf_token) throw new APIError(502, { error: { code: "invalid_response" } });
      setCsrf(restored.csrf_token);
    }).finally(() => { csrfRefresh = undefined; });
    await csrfRefresh;
    return request<T>(path, options);
  }
}

/** Generate a UUIDv4 idempotency key for state-changing requests. */
export function idempotencyKey(prefix: string): string {
  return `${prefix}-${v4()}`;
}

export function combineAbortSignals(...signals: Array<AbortSignal | undefined>): AbortSignal | undefined {
  const active = signals.filter((signal): signal is AbortSignal => Boolean(signal));
  if (!active.length) return undefined;
  if (active.length === 1) return active[0];
  if (typeof AbortSignal.any === "function") return AbortSignal.any(active);
  const controller = new AbortController();
  const abort = () => controller.abort();
  for (const signal of active) {
    if (signal.aborted) {
      controller.abort();
      break;
    }
    signal.addEventListener("abort", abort, { once: true });
  }
  return controller.signal;
}
