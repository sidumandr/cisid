// The UI's languages: English, the language it is written in, and Turkish.
// t takes the English text and gives it in the language chosen; a text the
// dictionary does not have stays English. {name} marks a value put in.
// Every literal passed to t has a Turkish entry; i18n.test.ts checks it.

import { useSyncExternalStore } from "react";

import { createStore } from "./store";
import { TR } from "./i18n.tr";

export type Lang = "en" | "tr";
export const LANGS: { lang: Lang; label: string; name: string }[] = [
  { lang: "en", label: "EN", name: "English" },
  { lang: "tr", label: "TR", name: "Türkçe" },
];

const KEY = "cisid.lang";

// initial is the language saved in this browser, else the browser's own
// when it is Turkish, else English. Outside a browser (tests) it is English.
function initial(): Lang {
  if (typeof window === "undefined") return "en";
  try {
    const saved = localStorage.getItem(KEY);
    if (saved === "en" || saved === "tr") return saved;
  } catch {
    // Storage may be off; the browser's language still counts.
  }
  return navigator.language?.toLowerCase().startsWith("tr") ? "tr" : "en";
}

const store = createStore<Lang>(initial());

export const getLang = () => store.get();

export function setLang(lang: Lang) {
  try {
    localStorage.setItem(KEY, lang);
  } catch {
    // Not remembered, but still used for this page.
  }
  if (typeof document !== "undefined") document.documentElement.lang = lang;
  store.set(lang);
}

// useLang re-renders a component when the language changes.
export const useLang = () => useSyncExternalStore(store.subscribe, store.get);

export function t(en: string, vars?: Record<string, string | number>): string {
  const text = store.get() === "tr" ? (TR[en] ?? en) : en;
  return vars ? text.replace(/\{(\w+)\}/g, (all, k: string) => (k in vars ? String(vars[k]) : all)) : text;
}

// N_ marks English text that is translated where it is shown, as in a
// table of labels made once; it returns the text as it is.
export const N_ = (en: string) => en;

// applyLang marks the page with the language chosen, before the first render.
export function applyLang() {
  if (typeof document !== "undefined") document.documentElement.lang = store.get();
}
