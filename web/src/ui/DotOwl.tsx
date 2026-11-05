// DotOwl draws a grid of dots behind the introduction, with the cisid owl
// set in it: its lenses in the logo's orange, its eyes in ink, following
// the pointer and blinking now and then. Dots near the pointer swell and
// warm. The owl sits behind the element anchor points to, where the glass
// the builds play on blurs it.

import { useEffect, useRef, type RefObject } from "react";

// The mark's lenses and the line where they cross, in the logo files'
// coordinates (x 14 to 186, y 4 to 104); see Logo.tsx.
const LENSES = new Path2D(
  "M88.9 77.9 L111.1 42.1 A34 34 0 0 1 129.66 27.61 L166 16 L173.36 53.46 A34 34 0 0 1 111.1 77.9 " +
    "L88.9 42.1 A34 34 0 0 0 70.34 27.61 L34 16 L26.64 53.46 A34 34 0 0 0 88.9 77.9 Z",
);
const STROKE = 12;
// The eyes: their middles and radius, and the bright spot each one keeps
// toward the bridge, in the same coordinates.
const EYES: { x: number; y: number; hx: number }[] = [
  { x: 70.4, y: 57.6, hx: 2.3 },
  { x: 129.6, y: 57.6, hx: -2.3 },
];
const EYE_R = 10;
const SPOT_R = 4.8;
const SPOT_DY = -4.3;
// How far the eyes look, in mark units, as useGaze has it.
const REACH_X = 3.4;
const REACH_Y = 2.4;
const BLINK_EVERY = 6.5; // seconds
const BLINK_FOR = 0.16;
const NEAR = 120; // px around the pointer that warm up
const CLEAR = 18; // px kept free above and below the owl, from the nav and the anchor
const MIN_OWL = 340; // px: an owl narrower than this hides behind the anchor instead
const SPREAD = 24; // px a glass's blur spreads what lies under it

interface Dot {
  x: number;
  y: number;
  lens: boolean;
}

interface Colors {
  ink: string;
  brand: string;
  eye: string;
  accent: [number, number, number];
  base: [number, number, number];
}

function rgb(color: string, fallback: [number, number, number]): [number, number, number] {
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(color.trim());
  return m ? [parseInt(m[1]!, 16), parseInt(m[2]!, 16), parseInt(m[3]!, 16)] : fallback;
}

function readColors(): Colors {
  const s = getComputedStyle(document.documentElement);
  const v = (name: string, fallback: string) => s.getPropertyValue(name).trim() || fallback;
  const ink = v("--text", "#111111");
  const brand = v("--brand", "#f26b1d");
  return { ink, brand, eye: v("--brand-ink", "#141414"), accent: rgb(brand, [242, 107, 29]), base: rgb(ink, [17, 17, 17]) };
}

// avoid names an element on glass over the canvas, such as the nav.
export function DotOwl({ anchor, avoid }: { anchor: RefObject<HTMLElement | null>; avoid?: string }) {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = ref.current;
    const g = canvas?.getContext("2d");
    if (!canvas || !g) return;
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const fine = matchMedia("(pointer: fine)").matches;

    let w = 0;
    let h = 0;
    let gap = 14;
    let dots: Dot[] = [];
    let base: HTMLCanvasElement | null = null;
    let colors = readColors();
    // The owl's place: its scale and where the mark's (0, 0) falls.
    let s = 1;
    let ox = 0;
    let oy = 0;
    let px = -1e4;
    let py = -1e4;
    let pointer = false;
    let look = { x: 0, y: 0 };
    let visible = true;
    let frame = 0;
    const t0 = performance.now();

    const layout = () => {
      const dpr = window.devicePixelRatio || 1;
      w = canvas.clientWidth;
      h = canvas.clientHeight;
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      g.setTransform(dpr, 0, 0, dpr, 0, 0);
      // On a wide screen the whole owl sits between the nav and the anchor,
      // its eyes just over the anchor's top edge, as large as that room
      // allows. Where there is too little room, or on a narrow screen with
      // the anchor under the words, it hides behind the anchor, wider than it.
      const c = canvas.getBoundingClientRect();
      const a = anchor.current?.getBoundingClientRect();
      // The canvas reaches up under the nav by as much as its top is raised.
      const below = -parseFloat(getComputedStyle(canvas).top) || 0;
      let width = a ? a.width * 1.12 : w * 0.45;
      let eyeY = a ? a.top + a.height / 2 - c.top : h * 0.5;
      if (a && w >= 1000) {
        const top = a.top - c.top;
        // From the top of the lenses' stroke (y 16, less half the stroke) to
        // the eyes' bottoms (y 57.6 + 10) is 57.6 units.
        const fit = (top - below - 2 * CLEAR) / (57.6 - 16 + STROKE / 2 + EYE_R);
        if (fit * 172 >= MIN_OWL) {
          width = Math.min(a.width * 1.08, fit * 172);
          eyeY = top - CLEAR - EYE_R * (width / 172);
        }
      }
      s = width / 172;
      // Finer dots for a smaller owl, so its eyes keep their shape.
      gap = w < 600 ? 12 : s >= 3.2 ? 14 : s >= 2.6 ? 12 : 10;
      const cx = a ? a.left + a.width / 2 - c.left : w * 0.72;
      ox = cx - 100 * s;
      oy = eyeY - EYES[0]!.y * s;

      // Which dots fall on the lenses: stroke them into a mask and look.
      const mask = document.createElement("canvas");
      mask.width = Math.max(1, Math.ceil(w));
      mask.height = Math.max(1, Math.ceil(h));
      const m = mask.getContext("2d")!;
      m.setTransform(s, 0, 0, s, ox, oy);
      m.lineWidth = STROKE;
      m.stroke(LENSES);
      m.beginPath();
      m.moveTo(88.9, 77.9);
      m.lineTo(111.1, 42.1);
      m.stroke();
      const alpha = m.getImageData(0, 0, mask.width, mask.height).data;
      dots = [];
      for (let y = gap / 2; y < h; y += gap) {
        for (let x = gap / 2; x < w; x += gap) {
          const i = ((Math.floor(y) * mask.width + Math.floor(x)) * 4) + 3;
          dots.push({ x, y, lens: (alpha[i] ?? 0) > 80 });
        }
      }
      paintBase();
    };

    // The still part, the grid's plain dots, drawn once per size or theme.
    const paintBase = () => {
      const dpr = window.devicePixelRatio || 1;
      base = document.createElement("canvas");
      base.width = canvas.width;
      base.height = canvas.height;
      const b = base.getContext("2d")!;
      b.setTransform(dpr, 0, 0, dpr, 0, 0);
      b.fillStyle = colors.ink;
      b.globalAlpha = 0.13;
      for (const d of dots) {
        if (d.lens) continue;
        b.beginPath();
        b.arc(d.x, d.y, 1.2, 0, Math.PI * 2);
        b.fill();
      }
    };

    const eyeDots = (t: number) => {
      if (!still && t % BLINK_EVERY < BLINK_FOR) return [];
      return EYES.map((e) => ({ x: ox + (e.x + look.x) * s, y: oy + (e.y + look.y) * s, hx: ox + (e.x + look.x + e.hx) * s, hy: oy + (e.y + look.y + SPOT_DY) * s }));
    };

    const draw = (t: number) => {
      g.clearRect(0, 0, w, h);
      if (base) {
        g.save();
        g.setTransform(1, 0, 0, 1, 0, 0);
        g.drawImage(base, 0, 0);
        g.restore();
      }
      const eyes = eyeDots(t);
      // Nothing warms under the glass of the element avoid names (the nav),
      // which would spread it into a haze around its buttons, nor while the
      // pointer is on it. Its box, on the canvas, grown by the glass's blur.
      let box = [0, 0, -1, -1];
      const el = avoid ? document.querySelector(avoid) : null;
      if (el) {
        const r = el.getBoundingClientRect();
        const c = canvas.getBoundingClientRect();
        box = [r.left - c.left - SPREAD, r.top - c.top - SPREAD, r.right - c.left + SPREAD, r.bottom - c.top + SPREAD];
      }
      const inBox = (x: number, y: number) => x >= box[0]! && x <= box[2]! && y >= box[1]! && y <= box[3]!;
      const warm = pointer && !still && !inBox(px, py);
      for (const d of dots) {
        const dist = warm && !inBox(d.x, d.y) ? Math.hypot(d.x - px, d.y - py) : Infinity;
        const k = dist < NEAR ? 1 - dist / NEAR : 0;
        let eye = false;
        for (const e of eyes) {
          if (Math.hypot(d.x - e.x, d.y - e.y) < EYE_R * s && Math.hypot(d.x - e.hx, d.y - e.hy) > SPOT_R * s) eye = true;
        }
        if (!d.lens && !eye && k === 0) continue;
        let r = 1.2;
        let a = 0.13;
        let color = colors.ink;
        if (d.lens) {
          r = gap * 0.17 + (still ? 0 : 0.35 * Math.sin(t * 2 + d.x * 0.03 + d.y * 0.02));
          a = 0.95;
          color = colors.brand;
        }
        if (eye) {
          r = gap * 0.2;
          a = 1;
          color = colors.eye;
        }
        if (k > 0) {
          r += k * 1.8;
          a = Math.min(1, a + k * 0.6);
          if (!d.lens && !eye) {
            const c = colors.base.map((v, i) => Math.round(v + (colors.accent[i]! - v) * k * 0.75));
            color = `rgb(${c[0]}, ${c[1]}, ${c[2]})`;
          }
        }
        g.globalAlpha = a;
        g.fillStyle = color;
        g.beginPath();
        g.arc(d.x, d.y, r, 0, Math.PI * 2);
        g.fill();
      }
      g.globalAlpha = 1;
    };

    const step = (now: number) => {
      const t = (now - t0) / 1000;
      // The eyes ease toward the pointer, or wander when there is none.
      let tx = Math.cos(t * 0.5) * 0.6;
      let ty = Math.sin(t * 0.7) * 0.4;
      if (pointer) {
        const cx = ox + 100 * s;
        const cy = oy + 57.6 * s;
        const dx = px - cx;
        const dy = py - cy;
        const d = Math.hypot(dx, dy) || 1;
        const k = Math.min(1, d / 420);
        tx = (dx / d) * k;
        ty = (dy / d) * k;
      }
      look = { x: look.x + (tx * REACH_X - look.x) * 0.15, y: look.y + (ty * REACH_Y - look.y) * 0.15 };
      draw(t);
      frame = visible ? requestAnimationFrame(step) : 0;
    };
    const start = () => {
      if (!still && !frame && visible) frame = requestAnimationFrame(step);
    };

    const onMove = (e: PointerEvent) => {
      const c = canvas.getBoundingClientRect();
      px = e.clientX - c.left;
      py = e.clientY - c.top;
      pointer = px >= -NEAR && py >= -NEAR && px <= w + NEAR && py <= h + NEAR;
    };
    const onLeave = () => {
      pointer = false;
    };
    const restyle = () => {
      colors = readColors();
      paintBase();
      if (still) draw(0);
    };

    layout();
    if (still) draw(0);
    start();
    // The page's fonts can move the anchor once they arrive.
    void document.fonts?.ready.then(() => {
      layout();
      if (still) draw(0);
    });
    const resize = new ResizeObserver(() => {
      layout();
      if (still) draw(0);
    });
    resize.observe(canvas);
    if (anchor.current) resize.observe(anchor.current);
    const seen = new IntersectionObserver(([entry]) => {
      visible = entry?.isIntersecting ?? true;
      if (visible) start();
    });
    seen.observe(canvas);
    const theme = new MutationObserver(restyle);
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    const scheme = matchMedia("(prefers-color-scheme: dark)");
    scheme.addEventListener("change", restyle);
    if (fine && !still) {
      window.addEventListener("pointermove", onMove, { passive: true });
      document.documentElement.addEventListener("pointerleave", onLeave);
    }
    return () => {
      cancelAnimationFrame(frame);
      resize.disconnect();
      seen.disconnect();
      theme.disconnect();
      scheme.removeEventListener("change", restyle);
      window.removeEventListener("pointermove", onMove);
      document.documentElement.removeEventListener("pointerleave", onLeave);
    };
  }, [anchor, avoid]);

  return <canvas className="dot-owl" ref={ref} aria-hidden="true" />;
}
