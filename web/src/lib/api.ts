// Calls to the JSON API. A change goes with the signed-in user's session or
// with a token the user types, which is kept in this tab only; when neither
// is accepted, the user is asked to sign in.

import { createElement } from "react";

import { openDialog, type Field } from "./dialog";
import { getMe, loadMe, signedIn } from "./session";
import { ProviderLink } from "../ui/parts";

const TOKEN_KEY = "cisid.apiToken";

// signInDeclined stops asking a reader who said no to signing in again.
let signInDeclined = false;

function savedToken(): string | null {
  try {
    return sessionStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

function saveToken(token: string | null) {
  try {
    if (token) sessionStorage.setItem(TOKEN_KEY, token);
    else sessionStorage.removeItem(TOKEN_KEY);
  } catch {
    // Not kept; the next change asks again.
  }
}

export class APIError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
  }
}

interface RequestOptions {
  body?: unknown;
  // token marks a change: it needs a maintainer (or more).
  token?: boolean;
}

// request calls the API and returns its JSON ({} for 204), or null when the
// user declined to sign in for a change.
export async function request<T = unknown>(method: string, url: string, { body, token = false }: RequestOptions = {}): Promise<T | null> {
  for (let attempt = 0; attempt < 3; attempt++) {
    // Changes a cookie signs in must say they come from this page.
    const headers: Record<string, string> = { Accept: "application/json", "X-Cisid-Request": "1" };
    if (token && !signedIn()) {
      const t = await credential(attempt > 0);
      if (t === null) return null;
      if (t) headers.Authorization = "Bearer " + t;
    }
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const resp = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    if (resp.status === 401) {
      if (token) {
        saveToken(null);
        await loadMe();
        continue;
      }
      if (await signIn()) continue;
      throw new APIError("Sign in to see this.", 401);
    }
    if (resp.status === 204) return {} as T;
    const data = (await resp.json().catch(() => ({}))) as { reason?: string };
    if (!resp.ok) throw new APIError(data.reason || resp.statusText, resp.status);
    return data as T;
  }
  throw new APIError(token ? "The sign-in or token was not accepted." : "The token was not accepted.", 401);
}

// getJSON reads from the API; it throws instead of returning null.
export async function getJSON<T>(url: string): Promise<T> {
  const data = await request<T>("GET", url);
  if (data === null) throw new APIError("Sign in to see this.", 401);
  return data;
}

// credential returns the token a change goes with, "" when the user signed
// in instead, or null when they declined.
async function credential(retry: boolean): Promise<string | null> {
  if (retry) saveToken(null);
  const saved = savedToken();
  if (saved) return saved;
  if (!(await signIn({ change: true, retry }))) return null;
  if (signedIn()) return "";
  return savedToken();
}

// signIn asks for a username and password, a forge, or a token. A token
// for changes is kept in this tab only; a personal token signs the browser
// in like a password.
export async function signIn({ change = false, retry = false } = {}): Promise<boolean> {
  if (!change && signInDeclined) return false;
  const me = getMe();
  const fields: Field[] = [];
  if (me.accounts) {
    fields.push({ name: "username", label: "Username", autocomplete: "username" },
      { name: "password", label: "Password", type: "password", autocomplete: "current-password" });
  }
  fields.push({
    name: "token", label: me.accounts ? "Or a token" : "Token", type: "password", autocomplete: "off",
    hint: change ? "A personal token, or CISID_API_TOKEN, kept in this tab only." : "A personal token, CISID_READ_TOKEN or CISID_API_TOKEN.",
  });
  const providers = (me.providers || []).map((p) =>
    createElement(ProviderLink, { key: p, provider: p, verb: "Sign in with", next: location.pathname }));
  const done = await openDialog({
    title: retry ? "That did not work" : "Sign in",
    text: change ? "Running builds, cancelling, rerunning and managing schedules, notifications and secrets need a maintainer."
      : "This server shows jobs and logs to signed-in users.",
    extra: providers.length ? createElement("div", { className: "providers" }, providers) : null,
    fields,
    submit: "Sign in",
    onSubmit: async (v) => {
      const t = (v.token || "").trim();
      if (v.username && v.password) {
        await login({ username: v.username.trim(), password: v.password });
      } else if (t.startsWith("cisid_")) {
        await login({ token: t });
      } else if (t) {
        if (change) saveToken(t);
        else await login({ token: t });
      } else {
        throw new Error(me.accounts ? "Write your username and password, or a token." : "Write a token.");
      }
      await loadMe();
    },
  });
  if (!done && !change) signInDeclined = true;
  return Boolean(done);
}

function sentence(reason: string): string {
  return reason.charAt(0).toUpperCase() + reason.slice(1) + (reason.endsWith(".") ? "" : ".");
}

// login signs the browser in with a password or a token.
export async function login(body: { username: string; password: string } | { token: string }): Promise<void> {
  const resp = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Cisid-Request": "1" },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    const data = (await resp.json().catch(() => ({}))) as { reason?: string };
    throw new Error(data.reason ? sentence(data.reason) : "That was not accepted.");
  }
}

// register opens an account and signs the browser in.
export async function register(body: { username: string; password: string }): Promise<void> {
  const resp = await fetch("/api/register", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Cisid-Request": "1" },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    const data = (await resp.json().catch(() => ({}))) as { reason?: string };
    throw new Error(data.reason ? sentence(data.reason) : "That did not work.");
  }
}

export async function logout(): Promise<void> {
  await fetch("/api/logout", { method: "POST", headers: { "X-Cisid-Request": "1" } });
  saveToken(null);
  signInDeclined = false;
  await loadMe();
}
