import { describe, expect, it } from "vitest";

import {
  ago, byteSize, finding, fmtDuration, jobsFinding, listWords, matrixLabel, parseIdentities, repoKeyRe, repoURL,
  runningSteps, stepStates, stepTime, trigger,
} from "./format";
import type { Job } from "./types";

const base: Job = {
  id: 42, repo: "sidumandr/cisid", clone_url: "", branch: "main", commit_sha: "6113728f27ae82c7b1a177c8d03f9e96e0adf246",
  status: "success", created_at: "2026-11-04T09:00:00Z", event: "push", steps: ["clone", "test", "chart"],
  started_at: "2026-11-04T09:00:00Z", finished_at: "2026-11-04T09:01:48Z", agent: "worker-1",
};
const now = new Date("2026-11-04T09:05:00Z").getTime();

describe("durations and sizes", () => {
  it("writes durations as people read them", () => {
    expect(fmtDuration(null)).toBe("");
    expect(fmtDuration(5)).toBe("5s");
    expect(fmtDuration(108)).toBe("1m 48s");
    expect(fmtDuration(3720)).toBe("1h 2m");
  });
  it("writes sizes in binary units", () => {
    expect(byteSize(512)).toBe("512 B");
    expect(byteSize(1536)).toBe("1.5 KB");
    expect(byteSize(20 << 20)).toBe("20.0 MB");
  });
  it("says how long ago", () => {
    expect(ago("2026-11-04T09:04:30Z", now)).toBe("30s ago");
    expect(ago("2026-11-04T07:05:00Z", now)).toBe("2h ago");
  });
});

describe("jobs in words", () => {
  it("names what started a job", () => {
    expect(trigger(base)).toBe("push to main");
    expect(trigger({ ...base, event: "pull_request", pull_request: 7, branch: "fix", target_branch: "main", fork: true }))
      .toBe("PR #7 fix → main from a fork");
  });
  it("says where a job stands", () => {
    expect(finding(base, now)).toBe("Passed in 1m 48s on worker-1.");
    expect(finding({ ...base, status: "failed", failed_step: 1, exit_code: 2 }, now)).toBe("Failed at test with exit code 2 after 1m 48s.");
    expect(finding({ ...base, status: "pending", needs_approval: true, started_at: undefined, finished_at: undefined }, now))
      .toBe("Queued 5m ago, waiting for a maintainer to approve this build from a fork.");
    expect(finding({ ...base, status: "pending", labels: ["arm64", "gpu"] }, now))
      .toBe("Waiting for an agent labelled arm64 and gpu since 5m ago.");
  });
  it("sums up a list", () => {
    expect(jobsFinding([], now)).toBe("Nothing running. No failures in the last 24 hours.");
    expect(jobsFinding([{ ...base, status: "running" }, { ...base, id: 41, status: "failed", failed_step: 1 }], now))
      .toBe("1 running. 1 failed in the last 24 hours, the latest sidumandr/cisid #41 at test.");
  });
});

describe("steps", () => {
  it("uses the states agents report", () => {
    const job: Job = { ...base, status: "running", finished_at: undefined, step_results: [
      { state: "success", started_at: "2026-11-04T09:00:00Z", finished_at: "2026-11-04T09:00:04Z" },
      { state: "running" },
      { state: "running" },
    ] };
    expect(stepStates(job).map((s) => s.state)).toEqual(["passed", "running", "running"]);
    expect(runningSteps(job)).toEqual(["test", "chart"]);
    expect(stepTime(job.step_results![0])).toBe(4);
    expect(finding(job, now)).toBe("Running test and chart on worker-1 for 5m 0s.");
  });
  it("works them out for older agents", () => {
    expect(stepStates({ ...base, status: "failed", failed_step: 1 }).map((s) => s.state)).toEqual(["passed", "failed", "skipped"]);
    expect(stepStates({ ...base, status: "running", current_step: 1 }).map((s) => s.state)).toEqual(["passed", "running", "queued"]);
  });
});

describe("small helpers", () => {
  it("escapes repository keys for URLs", () => {
    expect(repoURL("sidumandr/cisid")).toBe("sidumandr/cisid");
    expect(repoURL("gitlab:group/sub/project")).toBe("gitlab%3Agroup%2Fsub/project");
    expect(repoKeyRe.test("gitea:office/app")).toBe(true);
    expect(repoKeyRe.test("svn:office/app")).toBe(false);
  });
  it("joins and parses lists", () => {
    expect(listWords(["a", "b", "c"])).toBe("a, b and c");
    expect(matrixLabel({ os: "linux", go: "1.27" })).toBe("go=1.27, os=linux");
    expect(parseIdentities("gitea=ahmet, GitHub=ahmet-y")).toEqual({ gitea: "ahmet", github: "ahmet-y" });
    expect(() => parseIdentities("gitea")).toThrow();
  });
});
