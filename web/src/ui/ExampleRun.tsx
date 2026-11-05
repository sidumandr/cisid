// ExampleRun plays a build from push to Passed, over and over: the strip
// fills, lint and test run side by side, the log arrives line by line. The
// landing page shows it to visitors; the sign-in pages play it behind the
// form, two of them, one ahead of the other by offset milliseconds.

import { useEffect, useState } from "react";

import { CYCLE_MS, frameAt } from "../lib/heroRun";
import { fmtDuration, trigger } from "../lib/format";
import { t } from "../lib/i18n";
import { Pill, Strip } from "./parts";

export function ExampleRun({ offset = 0 }: { offset?: number }) {
  const [clock, setClock] = useState({ ms: offset % CYCLE_MS, run: Math.floor(offset / CYCLE_MS) });
  useEffect(() => {
    let last = performance.now();
    const id = setInterval(() => {
      const now = performance.now();
      const dt = Math.min(now - last, 400);
      last = now;
      if (document.hidden) return;
      setClock((c) => (c.ms + dt >= CYCLE_MS ? { ms: 0, run: c.run + 1 } : { ms: c.ms + dt, run: c.run }));
    }, 100);
    return () => clearInterval(id);
  }, []);
  const f = frameAt(clock.ms, clock.run);
  const running = f.job.status === "running";
  return (
    <figure className="hero-screen" aria-label={t("An example build")}>
      <figcaption>
        <span className={running ? "live-dot running" : "live-dot passed"} aria-hidden="true" />
        {running ? t("An example build in flight") : t("An example build, passed")}
      </figcaption>
      <div className="hero-rows">
        <div className="hero-row">
          <div className="hero-row-top">
            <span className="name">{f.job.repo}</span>
            <Pill status={f.job.status} />
          </div>
          <Strip job={f.job} />
          <div className="line2">
            {trigger(f.job)} · <span className="num">{f.job.commit_sha}</span> · <span className="num">{fmtDuration(f.seconds)}</span>
          </div>
        </div>
      </div>
      <pre className="hero-log" aria-hidden="true">
        {f.lines.map((l) => <span key={`${clock.run}-${l.key}`} className={l.echo ? "log-line err" : "log-line"}>{l.text}</span>)}
      </pre>
    </figure>
  );
}
