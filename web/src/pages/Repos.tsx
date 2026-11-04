// Repos lists the repositories connected to cisid and those seen in the
// latest jobs, and connects new ones.

import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router";

import { connectRepo } from "../lib/actions";
import { getJSON } from "../lib/api";
import { ago, repoPath, trigger } from "../lib/format";
import { t } from "../lib/i18n";
import type { Job, RepositoryInfo } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { Dash, ErrorNote, Header, Note, Pill, RepoTile, Skeleton, Strip } from "../ui/parts";
import { LIST_LIMIT } from "./Jobs";

interface RepoSummary {
  repo: string;
  last?: Job;
  count: number;
  connected?: RepositoryInfo;
}

// summarize puts the connected repositories and the repositories of the
// latest jobs (newest first) together, the ones with recent builds first.
function summarize(jobs: Job[], connected: RepositoryInfo[]): RepoSummary[] {
  const repos = new Map<string, RepoSummary>();
  for (const j of jobs) {
    const r = repos.get(j.repo);
    if (r) r.count++;
    else repos.set(j.repo, { repo: j.repo, last: j, count: 1 });
  }
  for (const c of connected) {
    const r = repos.get(c.repo);
    if (r) r.connected = c;
    else repos.set(c.repo, { repo: c.repo, count: 0, connected: c });
  }
  return [...repos.values()];
}

function HeaderRow() {
  return (
    <div className="tr th repos-grid" role="row">
      <span role="columnheader">{t("Repository")}</span>
      <span role="columnheader">{t("Last build")}</span>
      <span role="columnheader">{t("Status")}</span>
      <span role="columnheader" className="right col-count">{t("Builds")}</span>
      <span role="columnheader" className="right col-time">{t("Last run")}</span>
    </div>
  );
}

// how says how cisid hears of a connected repository's commits.
const how = (c: RepositoryInfo) =>
  c.polled ? t("polling {branch}", { branch: c.default_branch }) : t("by webhook");

export function Repos() {
  useTitle(t("Repositories"));
  const navigate = useNavigate();
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [connected, setConnected] = useState<RepositoryInfo[]>([]);
  const [failure, setFailure] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [list, repos] = await Promise.all([
        getJSON<Job[]>(`/api/jobs?limit=${LIST_LIMIT}`),
        getJSON<RepositoryInfo[]>("/api/repos").catch(() => [] as RepositoryInfo[]),
      ]);
      setJobs(list);
      setConnected(repos);
      setFailure(null);
    } catch (err) {
      setFailure(err instanceof Error ? err.message : String(err));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const repos = jobs ? summarize(jobs, connected) : [];
  const failing = repos.filter((r) => r.last?.status === "failed");
  let lede = t("Loading repositories");
  if (jobs) {
    lede = repos.length === 0
      ? t("Connect a repository, and cisid builds it as new commits arrive.")
      : (repos.length === 1
        ? t("{n} repository.", { n: 1 })
        : t("{n} repositories.", { n: repos.length })) + " " +
        (failing.length ? t("{n} failing now: {names}.", { n: failing.length, names: failing.map((r) => r.repo).join(", ") }) : t("None failing now."));
  }

  async function connect() {
    const made = await connectRepo();
    if (made) navigate(repoPath(made.repo));
  }

  let rows;
  if (failure) rows = <ErrorNote text={t("Couldn't load repositories: {reason}", { reason: failure })} onRetry={load} />;
  else if (!jobs) rows = <><HeaderRow /><Skeleton grid="repos-grid" rows={3} /></>;
  else if (repos.length === 0) {
    rows = (
      <Note action={<button className="text-action" type="button" onClick={connect}>{t("Connect a repository")}</button>}>
        {t("No repositories yet. Connect one, or point a forge's webhook at /webhooks/github (or gitea, forgejo, gitlab, bitbucket).")}
      </Note>
    );
  } else {
    rows = (
      <>
        <HeaderRow />
        {repos.map((r) => (
          <Link key={r.repo} className="tr repos-grid" to={repoPath(r.repo)} role="row">
            <div className="ident" role="cell">
              <RepoTile repo={r.repo} />
              <div className="stack">
                <div className="line1"><span className="name">{r.repo}</span></div>
                <div className="line2">
                  {r.last ? <>#{r.last.id} · {trigger(r.last)}</> : r.connected ? how(r.connected) : null}
                </div>
              </div>
            </div>
            <div role="cell">{r.last ? <Strip job={r.last} /> : <span className="strip-wait">{t("No builds yet")}</span>}</div>
            <div role="cell">{r.last ? <Pill status={r.last.status} /> : <Dash />}</div>
            <div role="cell" className="right num col-count">{r.count}</div>
            <div role="cell" className="right col-time muted">{r.last ? ago(r.last.created_at) : <Dash />}</div>
          </Link>
        ))}
      </>
    );
  }

  return (
    <>
      <Header title={t("Repositories")} lede={lede}
        actions={<button className="btn primary" type="button" onClick={connect}>{t("Connect a repository")}</button>} />
      <div className="table" role="table" aria-label={t("Repositories")}>{rows}</div>
    </>
  );
}
