// useGaze turns an owl's eyes toward the pointer: it sets --look-x and
// --look-y on the element, which the mark's eyes translate by. Nothing
// moves for someone who asked for reduced motion, or on a touch screen.

import { useEffect, type RefObject } from "react";

// How far the eyes go, in the mark's units: a little more sideways than up.
const REACH_X = 3.4;
const REACH_Y = 2.4;
// How far away, in pixels, the pointer must be for the eyes to go all the way.
const FULL_AT = 420;

export function useGaze(ref: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const el = ref.current;
    if (!el || matchMedia("(prefers-reduced-motion: reduce)").matches || !matchMedia("(pointer: fine)").matches) return;
    let frame = 0;
    let x = 0;
    let y = 0;
    const look = () => {
      frame = 0;
      const r = el.getBoundingClientRect();
      if (r.bottom < 0 || r.top > window.innerHeight) return;
      // The eyes sit a little below the middle of the mark.
      const dx = x - (r.left + r.width / 2);
      const dy = y - (r.top + r.height * 0.52);
      const d = Math.hypot(dx, dy) || 1;
      const k = Math.min(1, d / FULL_AT);
      el.style.setProperty("--look-x", `${((dx / d) * REACH_X * k).toFixed(2)}px`);
      el.style.setProperty("--look-y", `${((dy / d) * REACH_Y * k).toFixed(2)}px`);
    };
    const move = (e: PointerEvent) => {
      x = e.clientX;
      y = e.clientY;
      if (!frame) frame = requestAnimationFrame(look);
    };
    window.addEventListener("pointermove", move, { passive: true });
    return () => {
      window.removeEventListener("pointermove", move);
      cancelAnimationFrame(frame);
    };
  }, [ref]);
}
