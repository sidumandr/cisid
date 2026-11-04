// Who is signed in, as GET /api/me says, shared by every page.

import { useSyncExternalStore } from "react";

import { createStore } from "./store";
import type { Me } from "./types";

const meStore = createStore<Me>({ public_read: true, accounts: false, providers: [] });
let loaded = false;

export const getMe = () => meStore.get();
export const isLoaded = () => loaded;

// signedIn reports a browser session of a user, which changes things
// without a token.
export const signedIn = () => meStore.get().via === "session";

export async function loadMe(): Promise<Me> {
  try {
    const resp = await fetch("/api/me", { headers: { Accept: "application/json" } });
    if (resp.ok) meStore.set((await resp.json()) as Me);
  } catch {
    // The page still works; requests ask to sign in when they need to.
  }
  loaded = true;
  meStore.set({ ...meStore.get() });
  return meStore.get();
}

export function useMe(): Me {
  return useSyncExternalStore(meStore.subscribe, meStore.get);
}
