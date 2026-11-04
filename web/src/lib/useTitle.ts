import { useEffect } from "react";

// useTitle names the browser tab after the page.
export function useTitle(title: string) {
  useEffect(() => {
    document.title = title ? `${title} · cisid` : "cisid";
  }, [title]);
}
