import { afterEach, describe, expect, it } from "vitest";

import { getLang, setLang, t } from "./i18n";
import { TR } from "./i18n.tr";
import { STATE_WORD, STATUS_WORD } from "./format";

// The UI's code as text, leaving out tests and the dictionary.
const SOURCES = Object.entries(
  import.meta.glob<string>(["../**/*.ts", "../**/*.tsx", "!../**/*.test.ts", "!./i18n.tr.ts"], { query: "?raw", import: "default", eager: true }),
);

// texts are the English literals passed to t or N_.
function texts(): Map<string, string> {
  const found = new Map<string, string>();
  const call = /\b(?:t|N_)\(\s*"((?:[^"\\]|\\.)*)"/g;
  for (const [file, code] of SOURCES) {
    for (const m of code.matchAll(call)) found.set(JSON.parse(`"${m[1]}"`), file);
  }
  return found;
}

const holes = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();

describe("the Turkish dictionary", () => {
  it("has every text the UI translates", () => {
    expect(SOURCES.length).toBeGreaterThan(20);
    const missing = [...texts()].filter(([en]) => !(en in TR)).map(([en, file]) => `${en}  (${file})`);
    expect(missing).toEqual([]);
  });

  it("has the words tables hold, translated where they are shown", () => {
    for (const word of [...Object.values(STATUS_WORD), ...Object.values(STATE_WORD)]) expect(TR).toHaveProperty([word]);
  });

  it("keeps the values each text puts in", () => {
    for (const [en, tr] of Object.entries(TR)) expect(holes(tr), en).toEqual(holes(en));
  });

  it("never uses the long dash", () => {
    for (const tr of Object.values(TR)) expect(tr).not.toContain("\u2014");
  });
});

describe("t", () => {
  afterEach(() => setLang("en"));

  it("is English outside a browser, and fills in values", () => {
    expect(getLang()).toBe("en");
    expect(t("Jobs")).toBe("Jobs");
    expect(t("Job #{id}", { id: 7 })).toBe("Job #7");
  });

  it("speaks Turkish once chosen, and leaves unknown text English", () => {
    setLang("tr");
    expect(t("Jobs")).toBe("İşler");
    expect(t("Job #{id}", { id: 7 })).toBe("İş #7");
    expect(t("a text nobody translated")).toBe("a text nobody translated");
  });
});
