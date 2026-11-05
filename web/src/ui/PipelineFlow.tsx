// PipelineFlow draws builds flowing through a pipeline, lane by lane: each
// run lights the stages it passes in the logo's orange and turns green
// once it is through. It sits behind the sign-in form, under frosted glass.

import { useEffect, useRef } from "react";

// Where the stages sit across the width: clone, test, build, deploy.
const STAGES = [0.14, 0.4, 0.66, 0.9];
const LANE = 34; // px between lanes
const DOT = 9; // px between the dots of a lane
const FLASH = 0.9; // seconds a stage stays lit after a run passes it

interface Run {
  lane: number;
  x: number; // the head
  speed: number; // px a second
  len: number; // px of trail
}

interface Colors {
  run: string;
  done: string;
  ink: string;
}

function readColors(): Colors {
  const s = getComputedStyle(document.documentElement);
  const v = (name: string, fallback: string) => s.getPropertyValue(name).trim() || fallback;
  return { run: v("--accent", "#f26b1d"), done: v("--positive", "#3fc07a"), ink: v("--text", "#111111") };
}

export function PipelineFlow() {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = ref.current;
    const g = canvas?.getContext("2d");
    if (!canvas || !g) return;
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;

    let w = 0;
    let h = 0;
    let top = 0;
    let lanes = 0;
    let colors = readColors();
    let runs: Run[] = [];
    // When each lane's stages were last passed, and by a run that finished.
    let lit: { at: number; done: boolean }[][] = [];
    let frame = 0;
    let last = 0;
    let clock = 0;
    let recolor = 0;

    const spawn = (x?: number): Run | null => {
      const busy = new Set(runs.map((r) => r.lane));
      const free = [...Array(lanes).keys()].filter((l) => !busy.has(l));
      if (free.length === 0) return null;
      const len = 70 + Math.random() * 90;
      return {
        lane: free[Math.floor(Math.random() * free.length)]!,
        x: x ?? -Math.random() * 200,
        speed: 55 + Math.random() * 85,
        len,
      };
    };

    const size = () => {
      const dpr = window.devicePixelRatio || 1;
      w = canvas.clientWidth;
      h = canvas.clientHeight;
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      g.setTransform(dpr, 0, 0, dpr, 0, 0);
      lanes = Math.max(1, Math.floor(h / LANE));
      top = (h - (lanes - 1) * LANE) / 2;
      lit = Array.from({ length: lanes }, () => STAGES.map(() => ({ at: -99, done: false })));
      runs = [];
      // Start mid-flow, so the page never opens on an empty pipeline.
      for (let i = 0; i < Math.ceil(lanes * 0.45); i++) {
        const r = spawn(Math.random() * w);
        if (r) runs.push(r);
      }
    };

    const draw = () => {
      g.clearRect(0, 0, w, h);
      const lastStage = STAGES[STAGES.length - 1]! * w;
      for (let l = 0; l < lanes; l++) {
        const y = top + l * LANE;
        // The lane itself: faint dots.
        g.fillStyle = colors.ink;
        g.globalAlpha = 0.09;
        for (let x = DOT / 2; x < w; x += DOT) {
          g.beginPath();
          g.arc(x, y, 1.1, 0, Math.PI * 2);
          g.fill();
        }
        // Its stages: rings that light up as runs pass.
        STAGES.forEach((f, k) => {
          const x = f * w;
          const age = clock - lit[l]![k]!.at;
          const glow = Math.max(0, 1 - age / FLASH);
          g.globalAlpha = 0.22;
          g.strokeStyle = colors.ink;
          g.lineWidth = 1;
          g.beginPath();
          g.arc(x, y, 4.5, 0, Math.PI * 2);
          g.stroke();
          if (glow > 0) {
            g.globalAlpha = glow;
            g.fillStyle = lit[l]![k]!.done ? colors.done : colors.run;
            g.beginPath();
            g.arc(x, y, 4.5, 0, Math.PI * 2);
            g.fill();
          }
        });
      }
      // The runs: a bright head and a fading trail along the lane's dots.
      for (const r of runs) {
        const y = top + r.lane * LANE;
        const done = r.x > lastStage;
        g.fillStyle = done ? colors.done : colors.run;
        const from = Math.max(DOT / 2, Math.ceil((r.x - r.len - DOT / 2) / DOT) * DOT + DOT / 2);
        for (let x = from; x <= r.x && x < w; x += DOT) {
          const f = 1 - (r.x - x) / r.len;
          g.globalAlpha = Math.max(0, f) * 0.95;
          g.beginPath();
          g.arc(x, y, 1.2 + f * 1.6, 0, Math.PI * 2);
          g.fill();
        }
      }
      g.globalAlpha = 1;
    };

    const step = (now: number) => {
      const dt = Math.min(0.05, (now - last) / 1000 || 0);
      last = now;
      clock += dt;
      if ((recolor += dt) > 0.5) {
        recolor = 0;
        colors = readColors();
      }
      for (const r of runs) {
        const before = r.x;
        r.x += r.speed * dt;
        STAGES.forEach((f, k) => {
          const sx = f * w;
          if (before < sx && r.x >= sx) lit[r.lane]![k] = { at: clock, done: k === STAGES.length - 1 };
        });
      }
      runs = runs.filter((r) => r.x - r.len < w);
      while (runs.length < Math.ceil(lanes * 0.45)) {
        const r = spawn();
        if (!r) break;
        runs.push(r);
      }
      draw();
      frame = requestAnimationFrame(step);
    };

    size();
    const resize = new ResizeObserver(() => {
      size();
      if (still) draw();
    });
    resize.observe(canvas);
    if (still) {
      draw();
    } else {
      last = performance.now();
      frame = requestAnimationFrame(step);
    }
    return () => {
      cancelAnimationFrame(frame);
      resize.disconnect();
    };
  }, []);

  return <canvas className="pipeline-flow" ref={ref} aria-hidden="true" />;
}
