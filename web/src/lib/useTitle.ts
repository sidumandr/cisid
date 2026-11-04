import { useEffect } from "react";

// useTitle names the browser tab after the page; callers pass the title
// already in the language chosen, so a change of language renames the tab.
export function useTitle(title: string) {
  useEffect(() => {
    document.title = title ? `${title} · cisid` : "cisid";
  }, [title]);
}
