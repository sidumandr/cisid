// LangSwitch picks the UI's language: a two-way switch, EN and TR, the
// one in use pressed.

import { LANGS, setLang, t, useLang } from "../lib/i18n";

export function LangSwitch({ className = "" }: { className?: string }) {
  const lang = useLang();
  return (
    <div className={"lang-switch " + className} role="group" aria-label={t("Language")}>
      {LANGS.map((l) => (
        <button key={l.lang} type="button" lang={l.lang} title={l.name} aria-pressed={l.lang === lang} onClick={() => setLang(l.lang)}>
          {l.label}
        </button>
      ))}
    </div>
  );
}
