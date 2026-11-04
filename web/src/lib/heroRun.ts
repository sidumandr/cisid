// The example build the introduction plays for visitors: a push that runs
// clone, then lint beside test, then chart and artifacts, its log arriving
// as it goes, and then rests on Passed before the next push. Everything is
// a function of the time into the run, so the page only keeps a clock.

import type { Job, StepState } from "./types";

export const STEPS = ["clone", "lint", "test", "chart", "artifacts"];

// When each step starts and ends, in milliseconds from the push. lint and
// test run side by side, as depends_on allows.
const PLAN: [number, number][] = [
  [0, 1400],
  [1400, 3000],
  [1400, 5200],
  [5200, 6800],
  [6800, 7800],
];

export const RUN_MS = 7800;
// A run, then a rest on its result.
export const CYCLE_MS = 11500;

// The pushes the example takes turns with: commit and subject.
const PUSHES: [string, string][] = [
  ["4f2a9c1", "Retry webhooks on 502"],
  ["9b03e7d", "Cache the module downloads"],
  ["c71a2f4", "Name the agents in the logs"],
];

// LOG is the build's output: when it arrives, and whether it is the shell
// echoing a command (stderr, set dimmer) or the command's own output.
function log(sha: string, subject: string): [number, string, boolean][] {
  return [
    [100, "+ git clone --depth 1 https://github.com/your-org/api", true],
    [900, `HEAD is now at ${sha} ${subject}`, false],
    [1500, "+ go vet ./...", true],
    [1650, "+ go test -race ./...", true],
    [2700, "ok   api/internal/auth      1.204s", false],
    [3700, "ok   api/internal/store     3.871s", false],
    [4700, "ok   api/internal/webhook   0.912s", false],
    [5300, "+ helm lint deploy/chart", true],
    [6300, "1 chart(s) linted, 0 chart(s) failed", false],
    [6900, "+ tar -c coverage.out", true],
    [7600, "kept coverage.out (38.2 KiB)", false],
  ];
}

export interface LogLine {
  key: number;
  text: string;
  echo: boolean;
}

export interface Frame {
  job: Job;
  lines: LogLine[];
  // seconds is how long the build has taken, as the page shows it: the
  // example runs faster than a real build would, so its clock is scaled.
  seconds: number;
}

const SHOWN_LINES = 6;

// frameAt is the example at ms into run number run.
export function frameAt(ms: number, run: number): Frame {
  const t = Math.max(0, Math.min(ms, CYCLE_MS));
  const [sha, subject] = PUSHES[run % PUSHES.length]!;
  const states: StepState[] = PLAN.map(([start, end]) => (t >= end ? "success" : t >= start ? "running" : "pending"));
  const done = t >= RUN_MS;
  const lines = log(sha, subject)
    .map(([at, text, echo], key) => ({ at, key, text, echo }))
    .filter((l) => l.at <= t)
    .slice(-SHOWN_LINES)
    .map(({ key, text, echo }) => ({ key, text, echo }));
  return {
    job: {
      id: 0, repo: "your-org/api", clone_url: "", branch: "main", commit_sha: sha, status: done ? "success" : "running",
      created_at: "", event: "push", steps: STEPS, step_results: states.map((state) => ({ state })),
    },
    lines,
    seconds: Math.round((Math.min(t, RUN_MS) / 1000) * 12),
  };
}
