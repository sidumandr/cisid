// Job shows one job: where it stands, its facts, its steps as a strip to
// pick from, the picked step's live log and, once it is done, its
// artifacts. The log follows the running step until a step is picked.

import { useCallback, useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { getJSON, request } from "../lib/api";
import { openDialog } from "../lib/dialog";
import {
  FINISHED, STATE_WORD, byteSize, finding, fmtDuration, matrixLabel, repoPath, seconds, shortSHA, stamp, stepStates,
  stepTime, stripANSI, trigger,
} from "../lib/format";
import type { Artifact, Job as JobT, JobLog } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { Icon } from "../ui/Icon";
import { CopyButton, Dash, ErrorNote, Fact, Header, Pill } from "../ui/parts";

// pickDefault chooses the step a reader most likely wants: the running
// one, the one that failed, else the last.
function pickDefault(j: JobT): number | null {
  const n = (j.steps || []).length;
  if (n === 0) return null;
  if (j.status === "running") {
    const states = stepStates(j);
    for (let i = states.length - 1; i >= 0; i--) if (states[i]!.state === "running") return i;
    return j.step_results ? Math.max(0, states.findLastIndex((s) => s.state === "passed")) : j.current_step ?? 0;
  }
  if (j.failed_step !== undefined && j.failed_step !== null) return j.failed_step;
  return n - 1;
}

// Logs keeps one <pre> per step and appends output as text nodes, so a long
// log costs no re-rendering; the selected one is shown in the pane.
class Logs {
  private pres = new Map<number, HTMLPreElement>();
  pre(step: number): HTMLPreElement {
    let p = this.pres.get(step);
    if (!p) {
      p = document.createElement("pre");
      p.tabIndex = 0;
      this.pres.set(step, p);
    }
    return p;
  }
  has(step: number) {
    const p = this.pres.get(step);
    return Boolean(p && p.childNodes.length);
  }
  append(l: JobLog) {
    const p = this.pre(l.step);
    const follow = p.scrollTop + p.clientHeight >= p.scrollHeight - 4;
    const text = stripANSI(l.data);
    if (l.stream === "stderr") {
      const span = document.createElement("span");
      span.className = "err";
      span.textContent = text;
      p.append(span);
    } else {
      p.append(document.createTextNode(text));
    }
    if (follow) p.scrollTop = p.scrollHeight;
  }
}

export function Job() {
  const id = Number(useParams().id);
  useTitle(`Job #${id}`);
  const navigate = useNavigate();
  const [job, setJob] = useState<JobT | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [picked, setPicked] = useState<number | null>(null); // null follows the default
  const [now, setNow] = useState(Date.now());
  const [busy, setBusy] = useState(false);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [logVersion, setLogVersion] = useState(0);
  const logs = useRef(new Logs());
  const logBox = useRef<HTMLDivElement>(null);

  // Load the job first: if reading needs a token, this signs the browser
  // in, and the stream, which cannot send one itself, then gets through.
  useEffect(() => {
    let es: EventSource | null = null;
    let stopped = false;
    logs.current = new Logs();
    setJob(null);
    setFailure(null);
    setPicked(null);
    setArtifacts([]);
    getJSON<JobT>(`/api/jobs/${id}`)
      .then((j) => {
        if (stopped) return;
        setJob(j);
        // The stream sends the job, its logs, and the job again once it has
        // finished, then ends. Only that last status closes it: the first
        // one of a finished job comes before its logs.
        let statuses = 0;
        es = new EventSource(`/api/jobs/${id}/logs/stream`);
        es.addEventListener("status", (e) => {
          const next = JSON.parse((e as MessageEvent).data) as JobT;
          statuses++;
          setJob((prev) => ({ ...prev, ...next }));
          if (FINISHED.has(next.status) && statuses > 1) es?.close();
        });
        es.addEventListener("log", (e) => {
          const l = JSON.parse((e as MessageEvent).data) as JobLog;
          logs.current.append(l);
          setJob((prev) => {
            if (!prev || prev.status !== "running") return prev;
            const reported = prev.step_results?.[l.step];
            if (reported && reported.state === "pending") {
              // Output means the step started, even before the stream says so.
              return { ...prev, step_results: prev.step_results!.map((r, i) => (i === l.step ? { ...r, state: "running" } : r)) };
            }
            if (!prev.step_results && (prev.current_step ?? -1) < l.step) return { ...prev, current_step: l.step };
            return prev;
          });
          setLogVersion((v) => v + 1);
        });
      })
      .catch((err) => {
        if (!stopped) setFailure(err instanceof Error ? err.message : String(err));
      });
    return () => {
      stopped = true;
      es?.close();
    };
  }, [id]);

  // The clocks of a job still going move on every second.
  useEffect(() => {
    if (!job || FINISHED.has(job.status)) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [job]);

  // Once finished, the files the build kept are listed.
  const finished = job ? FINISHED.has(job.status) : false;
  useEffect(() => {
    if (!finished) return;
    getJSON<Artifact[]>(`/api/jobs/${id}/artifacts`).then(setArtifacts).catch(() => setArtifacts([]));
  }, [id, finished]);

  const states = job ? stepStates(job) : [];
  const selected = job ? (picked !== null && picked < states.length ? picked : pickDefault(job)) : null;

  // Show the selected step's log in the pane.
  useEffect(() => {
    const box = logBox.current;
    if (!box || selected === null || !logs.current.has(selected)) return;
    const pre = logs.current.pre(selected);
    if (pre.parentElement !== box) box.replaceChildren(pre);
  }, [selected, logVersion]);

  const act = useCallback(async (url: string, openResult: boolean) => {
    setBusy(true);
    try {
      const result = await request<JobT>("POST", url, { token: true });
      if (result && openResult) navigate("/jobs/" + result.id);
      // The API leaves out fields that are false, such as needs_approval.
      else if (result) setJob((prev) => ({ ...prev, needs_approval: false, ...result }));
    } catch (err) {
      await openDialog({ title: "That did not work", text: err instanceof Error ? err.message : String(err), submit: "Close", cancel: false });
    } finally {
      setBusy(false);
    }
  }, [navigate]);

  if (failure) {
    return (
      <>
        <Header title={<span>Job <span className="num">#{id}</span></span>} />
        <ErrorNote text={`Couldn't load this job: ${failure}`} onRetry={() => navigate(0)} />
      </>
    );
  }
  if (!job) {
    return <Header title={<span>Job <span className="num">#{id}</span></span>} lede="Loading job" />;
  }

  const actions = FINISHED.has(job.status)
    ? [<button key="rerun" className="btn primary" type="button" disabled={busy} onClick={() => act(`/api/jobs/${job.id}/rerun`, true)}>Rerun</button>]
    : [<button key="cancel" className="btn" type="button" disabled={busy} onClick={() => act(`/api/jobs/${job.id}/cancel`, false)}>Cancel</button>];
  if (job.needs_approval && job.status === "pending") {
    actions.unshift(<button key="approve" className="btn primary" type="button" disabled={busy}
      onClick={() => act(`/api/jobs/${job.id}/approve`, false)}>Approve</button>);
  }
  const errorAfter = job.status === "failed" && job.error && job.failed_step !== undefined && job.failed_step !== null ? ` ${job.error}` : "";

  const facts: [string, ReactNode][] = [
    ["Trigger", trigger(job)],
    ["Commit", <><span className="num">{shortSHA(job.commit_sha)}</span><CopyButton text={job.commit_sha} label="Copy the commit SHA" /></>],
    ["Agent", job.agent || <Dash />],
    ["Started", job.started_at ? <span className="num" title={job.started_at}>{stamp(job.started_at)}</span> : <Dash />],
    ["Duration", job.started_at ? <span className="num">{fmtDuration(seconds(job, now))}</span> : <Dash />],
  ];
  if (job.matrix) facts.splice(1, 0, ["Matrix", matrixLabel(job.matrix)]);

  const state = selected !== null ? states[selected] : undefined;
  let logNote: string | null = null;
  if (selected === null) {
    logNote = job.logs_expired_at
      ? `Logs were deleted on ${stamp(job.logs_expired_at)} after the retention period.`
      : FINISHED.has(job.status) ? "No step ran, so there is no output." : "The log starts when an agent picks the job up.";
  } else if (!logs.current.has(selected)) {
    if (job.logs_expired_at) logNote = `Logs were deleted on ${stamp(job.logs_expired_at)} after the retention period.`;
    else logNote = state && (state.state === "queued" || state.state === "running") ? "No output yet." : "This step wrote no output.";
  }
  const total = artifacts.reduce((n, a) => n + a.size, 0);

  return (
    <>
      <Header
        crumbs={<><Link to="/jobs">Jobs</Link><span aria-hidden="true">/</span><Link to={repoPath(job.repo)}>{job.repo}</Link></>}
        title={<><span>{job.repo}</span><span className="num">#{job.id}</span><Pill status={job.status} /></>}
        lede={<p className="lede">{finding(job, now)}{errorAfter}</p>}
        actions={actions}
      />
      <dl className="facts" style={{ "--n": facts.length } as CSSProperties}>
        {facts.map(([name, value]) => <Fact key={name} name={name}>{value}</Fact>)}
      </dl>
      <div className="strip big" role="group" aria-label="Steps">
        {states.map((s, i) => {
          const took = stepTime(s.result);
          return (
            <button key={i} className="seg" type="button" data-state={s.state} aria-pressed={i === selected} onClick={() => setPicked(i)}>
              <span className="seg-bar" />
              <span className="seg-name">
                <span>{s.name}</span>
                <span className="seg-state">{STATE_WORD[s.state]}</span>
                {took !== null ? <span className="seg-time num">{fmtDuration(took)}</span> : null}
              </span>
            </button>
          );
        })}
      </div>
      <section className="logs" aria-label="Log">
        <div className="logs-head">
          <div className="stack">
            <h2>{selected === null ? "Log" : (job.steps || [])[selected] || `step ${selected}`}</h2>
            <span className="muted small">{state ? STATE_WORD[state.state] : ""}</span>
          </div>
        </div>
        {logNote ? <p className="logs-note">{logNote}</p> : <div ref={logBox} />}
      </section>
      {artifacts.length ? (
        <section className="artifacts" aria-label="Artifacts">
          <div className="section-head">
            <h2>Artifacts</h2>
            <span className="muted small num">{artifacts.length} {artifacts.length === 1 ? "file" : "files"}, {byteSize(total)}</span>
          </div>
          <ul className="artifact-list">
            {artifacts.map((a) => (
              <li key={a.name}>
                <a href={`/api/jobs/${id}/artifacts/${a.name.split("/").map(encodeURIComponent).join("/")}`} download={a.name.split("/").pop()}>
                  <Icon name="download" size={14} /><span className="artifact-name">{a.name}</span>
                </a>
                <span className="num muted">{byteSize(a.size)}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </>
  );
}
