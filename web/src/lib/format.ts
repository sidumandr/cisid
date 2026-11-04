// Words and numbers for the UI: times, durations, sizes, and the sentence
// that says where a job stands. Pure functions, tested in format.test.ts.

import type { Job, JobStatus, StepResult } from "./types";

export const FINISHED: ReadonlySet<JobStatus> = new Set(["success", "failed", "canceled"]);

const pad2 = (n: number) => String(n).padStart(2, "0");

export function stamp(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

// stampUTC is stamp in UTC, the zone schedules are written in.
export function stampUTC(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())} ${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())} UTC`;
}

export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const s = Math.max(0, (now - new Date(iso).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

export function inWords(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const s = (new Date(iso).getTime() - now) / 1000;
  if (s < 60) return "in under a minute";
  if (s < 3600) return `in ${Math.round(s / 60)}m`;
  if (s < 86400) return `in ${Math.round(s / 3600)}h`;
  return `in ${Math.round(s / 86400)}d`;
}

// seconds is how long a job ran, or has been running.
export function seconds(job: Pick<Job, "started_at" | "finished_at">, now = Date.now()): number | null {
  if (!job.started_at) return null;
  const end = job.finished_at ? new Date(job.finished_at).getTime() : now;
  return Math.max(0, Math.round((end - new Date(job.started_at).getTime()) / 1000));
}

export function fmtDuration(s: number | null): string {
  if (s === null) return "";
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s % 60}s`;
  return `${s % 60}s`;
}

export const shortSHA = (sha?: string) => (sha || "").slice(0, 7);

// byteSize writes a size for people: 512 B, 1.5 KB, 20.0 MB.
export function byteSize(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

export function matrixLabel(m?: Record<string, string>): string {
  return Object.keys(m || {}).sort().map((k) => `${k}=${m![k]}`).join(", ");
}

export const STATUS_WORD: Record<JobStatus, string> = {
  success: "Passed", failed: "Failed", running: "Running", pending: "Queued", canceled: "Canceled",
};

// trigger says what started a job, in the words of the trade.
export function trigger(j: Job): string {
  switch (j.event) {
    case "pull_request":
      return `PR #${j.pull_request} ${j.branch} → ${j.target_branch}${j.fork ? " from a fork" : ""}`;
    case "manual":
      return `manual build of ${j.branch}`;
    case "schedule":
      return `scheduled build of ${j.branch}`;
    default:
      return `push to ${j.branch}`;
  }
}

// repoURL is a repository key as the two path segments the server routes:
// the last segment, and the rest with the forge and any slashes escaped,
// e.g. "gitlab:group/sub/project" -> "gitlab%3Agroup%2Fsub/project".
export function repoURL(repo: string): string {
  const i = repo.lastIndexOf("/");
  return encodeURIComponent(repo.slice(0, i)) + "/" + encodeURIComponent(repo.slice(i + 1));
}

export const repoPath = (repo: string) => "/repos/" + repoURL(repo);

// listWords joins names as a sentence does: "a", "a and b", "a, b and c".
export function listWords(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}

export type StripState = "passed" | "failed" | "running" | "canceled" | "queued" | "skipped";

export const STATE_WORD: Record<StripState, string> = {
  passed: "passed", failed: "failed", running: "running", canceled: "canceled", queued: "not started", skipped: "not run",
};

// STEP_STATE maps the states agents report to the strip's.
const STEP_STATE: Record<string, StripState> = {
  success: "passed", failed: "failed", running: "running", canceled: "canceled", skipped: "skipped", pending: "queued",
};

export interface StepView {
  name: string;
  state: StripState;
  result?: StepResult;
}

// stepStates says how far each planned step of a job got: as the agent
// reported it when it did, otherwise worked out from the job.
export function stepStates(j: Job): StepView[] {
  const reported = j.step_results;
  const names = j.steps || [];
  if (reported && reported.length) {
    return names.map((name, i) => {
      const r = reported[i];
      const state = r ? STEP_STATE[r.state] || "queued" : FINISHED.has(j.status) ? "skipped" : "queued";
      return r ? { name, state, result: r } : { name, state };
    });
  }
  const stopped = j.failed_step ?? null;
  return names.map((name, i): StepView => {
    let state: StripState;
    if (j.status === "success") state = "passed";
    else if (j.status === "failed" || j.status === "canceled") {
      if (stopped === null) state = "skipped";
      else if (i < stopped) state = "passed";
      else if (i === stopped) state = j.status === "failed" ? "failed" : "canceled";
      else state = "skipped";
    } else if (j.status === "running") {
      const current = j.current_step ?? 0;
      state = i < current ? "passed" : i === current ? "running" : "queued";
    } else state = "queued";
    return { name, state };
  });
}

// stepTime is how long a finished step took, in seconds, or null.
export function stepTime(r?: StepResult): number | null {
  if (!r || !r.started_at || !r.finished_at) return null;
  return Math.max(0, Math.round((new Date(r.finished_at).getTime() - new Date(r.started_at).getTime()) / 1000));
}

// runningSteps names the steps of a running job that are running now.
export function runningSteps(j: Job): string[] {
  const now = stepStates(j).filter((s) => s.state === "running").map((s) => s.name);
  if (now.length || j.step_results) return now;
  const steps = j.steps || [];
  const at = j.current_step ?? 0;
  return [steps[at] ?? `step ${at}`];
}

// finding is the one sentence that says where a job stands.
export function finding(j: Job, now = Date.now()): string {
  const steps = j.steps || [];
  const took = fmtDuration(seconds(j, now));
  const at = (i: number) => steps[i] ?? `step ${i}`;
  switch (j.status) {
    case "success":
      return `Passed in ${took}${j.agent ? ` on ${j.agent}` : ""}.`;
    case "failed":
      if (j.failed_step === undefined || j.failed_step === null) return `Failed before any step ran: ${j.error || "no reason recorded"}.`;
      return `Failed at ${at(j.failed_step)}${(j.exit_code ?? 0) > 0 ? ` with exit code ${j.exit_code}` : ""} after ${took}.`;
    case "canceled":
      return j.failed_step === undefined || j.failed_step === null
        ? "Canceled before it started."
        : `Canceled at ${at(j.failed_step)} after ${took}.`;
    case "running": {
      const running = runningSteps(j);
      const what = running.length ? listWords(running) : "between steps";
      return `Running ${what}${j.agent ? ` on ${j.agent}` : ""} for ${took}.`;
    }
    default:
      if (j.needs_approval) return `Queued ${ago(j.created_at, now)}, waiting for a maintainer to approve this build from a fork.`;
      if (j.labels && j.labels.length) return `Waiting for an agent labelled ${listWords(j.labels)} since ${ago(j.created_at, now)}.`;
      return `Waiting for an agent since ${ago(j.created_at, now)}.`;
  }
}

// jobsFinding sums up a list of jobs in a sentence or two.
export function jobsFinding(jobs: Job[], now = Date.now()): string {
  const running = jobs.filter((j) => j.status === "running").length;
  const queued = jobs.filter((j) => j.status === "pending").length;
  const since = now - 24 * 3600 * 1000;
  const failed = jobs.filter((j) => j.status === "failed" && new Date(j.finished_at || j.created_at).getTime() > since);
  const current: string[] = [];
  if (running) current.push(`${running} running`);
  if (queued) current.push(`${queued} queued`);
  const parts = [current.length ? current.join(", ") + "." : "Nothing running."];
  const latest = failed[0];
  if (!latest) parts.push("No failures in the last 24 hours.");
  else {
    const step = latest.failed_step !== undefined && latest.failed_step !== null
      ? ` at ${(latest.steps || [])[latest.failed_step] || "step " + latest.failed_step}` : "";
    parts.push(`${failed.length} failed in the last 24 hours, the latest ${latest.repo} #${latest.id}${step}.`);
  }
  return parts.join(" ");
}

export const FORGE_NAMES: Record<string, string> = {
  github: "GitHub", gitea: "Gitea", forgejo: "Forgejo", gitlab: "GitLab", bitbucket: "Bitbucket", google: "Google",
};
export const forgeName = (f: string) => FORGE_NAMES[f] || f;

// stripANSI drops terminal colour codes from step output.
export const stripANSI = (s: string) => s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "");

// parseIdentities reads "gitea=ahmet, github=ahmet-y" into a map.
export function parseIdentities(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of text.split(",")) {
    const [forge = "", login = ""] = part.split("=").map((s) => s.trim());
    if (!forge && !login) continue;
    if (!forge || !login) throw new Error("Write logins as provider=login, separated by commas.");
    out[forge.toLowerCase()] = login;
  }
  return out;
}

export const showIdentities = (ids?: Record<string, string>) =>
  Object.entries(ids || {}).map(([f, l]) => `${f}=${l}`).join(", ");

export const splitList = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);

// repoKeyRe is a repository key: owner/name on GitHub, forge:path elsewhere.
export const repoKeyRe = /^((gitea|forgejo|gitlab|bitbucket):)?[^/\s:]+(\/[^/\s:]+)+$/;
