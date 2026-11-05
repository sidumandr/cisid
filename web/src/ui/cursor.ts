// The pointer is an owl unless the reader asks for their system's own; the
// choice is kept in the browser, like the theme.

import { useState } from "react";

const KEY = "cisid.cursor";

// applySavedCursor puts back the system pointer if the reader chose it.
export function applySavedCursor() {
  try {
    if (localStorage.getItem(KEY) === "plain") document.documentElement.dataset.cursor = "plain";
  } catch {
    // Storage can be off; the owl then stays.
  }
}

// useCursor says whether the system pointer is in use, and switches it.
export function useCursor(): [boolean, () => void] {
  const [plain, setPlain] = useState(() => document.documentElement.dataset.cursor === "plain");
  const toggle = () => {
    const next = document.documentElement.dataset.cursor !== "plain";
    if (next) document.documentElement.dataset.cursor = "plain";
    else delete document.documentElement.dataset.cursor;
    setPlain(next);
    try {
      if (next) localStorage.setItem(KEY, "plain");
      else localStorage.removeItem(KEY);
    } catch {
      // Not remembered; it still applies now.
    }
  };
  return [plain, toggle];
}
