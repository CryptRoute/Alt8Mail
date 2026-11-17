/* JSON API transport and delegated-session headers. */
import { currentLocale } from "./format.js";
import { t, tp } from "../../i18n.mjs";
import { state } from "./state.js";

export const DELEGATED_SESSION_KEY = "a8m.delegated.window";
export const DELEGATED_SESSION_HEADER = "X-A8M-Delegated-Session";

let hooks = {};
export function configureApi(next = {}) { hooks = { ...hooks, ...next }; }

export function delegatedRequestHeaders(extra = {}) {
  const headers = { "X-A8M-Locale": currentLocale(), ...extra };
  if (state.delegatedSession?.token) headers[DELEGATED_SESSION_HEADER] = state.delegatedSession.token;
  return headers;
}

export class ApiFailure extends Error {
  constructor(status, code, message, details, params) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
    this.params = params || null;
  }
}

export async function api(path, options = {}) {
  const method = options.method || "GET";
  const headers = { Accept: "application/json", "X-A8M-Locale": currentLocale(), ...(options.headers || {}) };
  let body;
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(options.body);
  }
  if (!state.delegatedMode && state.activeAccountId) headers["X-A8M-Account"] = state.activeAccountId;
  const delegatedSession = state.delegatedSession;
  if (delegatedSession?.token) headers[DELEGATED_SESSION_HEADER] = delegatedSession.token;
  const csrf = delegatedSession?.csrf || state.csrf;
  if (csrf && method !== "GET") headers["X-CSRF-Token"] = csrf;
  let response;
  try {
    response = await fetch(path, { method, headers, body, credentials: "same-origin", cache: "no-store" });
  } catch (error) {
    hooks.setOffline?.(true);
    throw new ApiFailure(0, "NETWORK", t("api.network-unreachable"));
  }
  hooks.setOffline?.(false);
  const type = response.headers.get("content-type") || "";
  let payload = null;
  if (type.includes("application/json")) {
    try { payload = await response.json(); } catch { payload = null; }
  } else if (type.includes("text/")) {
    payload = await response.text();
  }
  if (!response.ok) {
    if (response.status === 403 && payload?.error?.code === "CSRF" && !options.csrfRetry && !options.skipAuth && method !== "GET") {
      // A preference change can race the first bootstrap after a reload or a
      // session refresh. The CSRF token is not a credential, so refresh it
      // from the session endpoint and retry the same request once.
      try {
        const session = await api("/api/session", { skipAuth: true });
        if (session?.csrf) {
          state.csrf = session.csrf;
          return api(path, {
            ...options,
            csrfRetry: true,
            headers: { ...headers, "X-CSRF-Token": session.csrf }
          });
        }
      } catch {}
    }
    if (response.status === 401 && !options.skipAuth) {
      if (state.delegatedMode) {
        const revoked = payload && payload.error ? payload.error.message : "";
        hooks.expireDelegatedWindow?.(revoked || t("api.delegated-session-ended"));
      } else {
        hooks.handleSessionExpired?.();
      }
    }
    const detail = payload && payload.error ? payload.error : {};
    const fallback = detail.message || t("api.request-failed", { status: response.status });
    const errorKey = detail.code ? `errors.${detail.code}` : "";
    const keyed = errorKey ? t(errorKey, detail.params || {}) : "";
    const message = keyed && keyed !== errorKey ? keyed : tp(fallback, detail.params || {});
    throw new ApiFailure(response.status, detail.code || "HTTP_" + response.status, message, detail.details || null, detail.params || null);
  }
  return payload;
}
