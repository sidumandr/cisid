// Jobs lists the latest jobs, with tabs, a filter and a sentence that sums
// them up. It refreshes every 5 seconds.

import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";

import { getJSON } from "../lib/api";
import { knownRepos, runBuild } from "../lib/actions";
import { ago, fmtDuration, jobsFinding, matrixLabel, seconds, shortSHA, trigger } from "../lib/format";
import { N_, t } from "../lib/i18n";
import type { Job } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { ErrorNote, Header, Note, Pill, RepoTile, SearchBox, Skeleton, Strip, TabBar, type Tab } from "../ui/parts";

export const LIST_LIMIT = 200;

type JobTab = "all" | "active" | "failed" | "pr";
const TABS: readonly Tab<JobTab>[] = [
  { id: "all", label: N_("All") },
  { id: "active", label: N_("Running") },
  { id: "failed", label: N_("Failed") },
  { id: "pr", label: N_("Pull requests") },
];
const MATCH: Record<JobTab, (j: Job) => boolean> = {
  all: () => true,
  active: (j) => j.status === "running" || j.status === "pending",
  failed: (j) => j.status === "failed",
  pr: (j) => j.event === "pull_request",
};

function matchesSearch(j: Job, q: string): boolean {
  if (!q) return true;
  return [j.repo, j.branch, j.target_branch, j.commit_sha, "#" + j.id, j.agent, trigger(j)]
    .some((v) => v && v.toLowerCase().includes(q));
}

export function JobsHeaderRow() {
  return (
    <div className="tr th jobs-grid" role="row">
      <span role="columnheader">{t("Job")}</span>
      <span role="columnheader">{t("Steps")}</span>
      <span role="columnheader">{t("Status")}</span>
      <span role="columnheader" className="col-agent">{t("Agent")}</span>
      <span role="columnheader" className="right col-time">{t("Duration")}</span>
    </div>
  );
}

export function JobRow({ job: j }: { job: Job }) {
  const sub = [trigger(j)];
  if (j.matrix) sub.push(matrixLabel(j.matrix));
  return (
    <Link className="tr jobs-grid" to={"/jobs/" + j.id} role="row">
      <div className="ident" role="cell">
        <RepoTile repo={j.repo} />
        <div className="stack">
          <div className="line1"><span className="name">{j.repo}</span><span className="num">#{j.id}</span></div>
          <div className="line2">{sub.join(" · ")} · <span className="num">{shortSHA(j.commit_sha)}</span></div>
        </div>
      </div>
      <div role="cell"><Strip job={j} /></div>
      <div role="cell"><Pill status={j.status} /></div>
      <div role="cell" className="col-agent muted">{j.agent || <span className="dash">-</span>}</div>
      <div role="cell" className="right col-time">
        <div className="num">{j.started_at ? fmtDuration(seconds(j)) : <span className="dash wide-only">-</span>}</div>
        <div className="line2">{ago(j.created_at)}</div>
      </div>
    </Link>
  );
}

export function Jobs() {
  useTitle(t("Jobs"));
  const navigate = useNavigate();
  const [tab, setTab] = useState<JobTab>("all");
  const [q, setQ] = useState("");
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  // Once the list has loaded, a failed refresh keeps showing it.
  const loaded = useRef(false);
  const refresh = useCallback(async () => {
    try {
      const list = await getJSON<Job[]>(`/api/jobs?limit=${LIST_LIMIT}`);
      loaded.current = true;
      setJobs(list);
      setFailure(null);
    } catch (err) {
      if (!loaded.current) setFailure(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = setInterval(refresh, 5000);
    return () => clearInterval(timer);
  }, [refresh]);

  const searched = (jobs ?? []).filter((j) => matchesSearch(j, q));
  const counts = Object.fromEntries(TABS.map((tab) => [tab.id, jobs ? searched.filter(MATCH[tab.id]).length : null]));
  const shown = searched.filter(MATCH[tab]);

  let rows;
  if (failure) rows = <ErrorNote text={t("Couldn't load jobs: {reason}", { reason: failure })} onRetry={refresh} />;
  else if (!jobs) rows = <><JobsHeaderRow /><Skeleton grid="jobs-grid" /></>;
  else if (jobs.length === 0) rows = <Note>{t("No jobs yet. Point a webhook at /webhooks/github (or gitea, forgejo, gitlab, bitbucket), poll a branch, or run a build by hand.")}</Note>;
  else if (shown.length === 0) rows = <><JobsHeaderRow /><Note>{t("No jobs match this view.")}</Note></>;
  else rows = <><JobsHeaderRow />{shown.map((j) => <JobRow key={j.id} job={j} />)}</>;

  return (
    <>
      <Header title={t("Jobs")} lede={jobs ? jobsFinding(jobs) : t("Loading jobs")} actions={
        <button className="btn primary" type="button" onClick={async () => {
          const job = await runBuild("", knownRepos(jobs ?? []));
          if (job) navigate("/jobs/" + job.id);
        }}>{t("Run build")}</button>
      } />
      <TabBar tabs={TABS} selected={tab} onSelect={setTab} counts={counts} />
      <div className="toolbar">
        <SearchBox placeholder={t("Filter by repository, branch or commit")} onChange={setQ} />
        <span className="muted small num">{jobs ? t("{n} of the latest {total} jobs", { n: shown.length, total: jobs.length }) : ""}</span>
      </div>
      <div className="table" role="table" aria-label={t("Jobs")}>{rows}</div>
    </>
  );
}
