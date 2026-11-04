import { describe, expect, it } from "vitest";

import { CYCLE_MS, RUN_MS, STEPS, frameAt } from "./heroRun";

const states = (ms: number) => frameAt(ms, 0).job.step_results!.map((r) => r.state);

describe("frameAt", () => {
  it("starts with clone running and the rest waiting", () => {
    expect(states(0)).toEqual(["running", "pending", "pending", "pending", "pending"]);
    expect(frameAt(0, 0).job.status).toBe("running");
    expect(frameAt(0, 0).lines).toEqual([]);
  });

  it("runs lint beside test", () => {
    expect(states(2000)).toEqual(["success", "running", "running", "pending", "pending"]);
    expect(states(4000)).toEqual(["success", "success", "running", "pending", "pending"]);
  });

  it("passes at the end of the run and rests there", () => {
    expect(states(RUN_MS)).toEqual(STEPS.map(() => "success"));
    expect(frameAt(RUN_MS, 0).job.status).toBe("success");
    expect(frameAt(CYCLE_MS - 1, 0).seconds).toBe(frameAt(RUN_MS, 0).seconds);
  });

  it("shows the last six lines of the log as they arrive", () => {
    const end = frameAt(RUN_MS, 0).lines;
    expect(end).toHaveLength(6);
    expect(end[end.length - 1]!.text).toBe("kept coverage.out (38.2 KiB)");
    expect(frameAt(1000, 0).lines.map((l) => l.echo)).toEqual([true, false]);
  });

  it("takes a new push each run", () => {
    expect(frameAt(0, 0).job.commit_sha).not.toBe(frameAt(0, 1).job.commit_sha);
    expect(frameAt(0, 3).job.commit_sha).toBe(frameAt(0, 0).job.commit_sha);
  });
});
