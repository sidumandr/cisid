// Repos lists the repositories seen in the latest jobs.

import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router";

import { getJSON } from "../lib/api";
import { ago, repoPath, trigger } from "../lib/format";
import { t } from "../lib/i18n";
import type { Job } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { ErrorNote, Header, Note, Pill, RepoTile, Skeleton, Strip } from "../ui/parts";
import { LIST_LIMIT } from "./Jobs";

interface RepoSummary {
  repo: string;
  last: Job;
  count: number;
}

function summarize(jobs: Job[]): RepoSummary[] {
  const repos = new Map<string, RepoSummary>();
  for (const j of jobs) { // newest first
    const r = repos.get(j.repo);
    if (r) r.count++;
    else repos.set(j.repo, { repo: j.repo, last: j, count: 1 });
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

export function Repos() {
  useTitle(t("Repositories"));
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setJobs(await getJSON<Job[]>(`/api/jobs?limit=${LIST_LIMIT}`));
      setFailure(null);
    } catch (err) {
      setFailure(err instanceof Error ? err.message : String(err));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const repos = jobs ? summarize(jobs) : [];
  const failing = repos.filter((r) => r.last.status === "failed");
  let lede = t("Loading repositories");
  if (jobs) {
    lede = repos.length === 0
      ? t("A repository shows up here after its first build.")
      : (repos.length === 1
        ? t("{n} repository in the latest {total} jobs.", { n: repos.length, total: jobs.length })
        : t("{n} repositories in the latest {total} jobs.", { n: repos.length, total: jobs.length })) + " " +
        (failing.length ? t("{n} failing now: {names}.", { n: failing.length, names: failing.map((r) => r.repo).join(", ") }) : t("None failing now."));
  }

  let rows;
  if (failure) rows = <ErrorNote text={t("Couldn't load repositories: {reason}", { reason: failure })} onRetry={load} />;
  else if (!jobs) rows = <><HeaderRow /><Skeleton grid="repos-grid" rows={3} /></>;
  else if (repos.length === 0) rows = <Note>{t("No repositories yet. Point a webhook at /webhooks/github (or gitea, forgejo, gitlab, bitbucket), or poll a branch, to build the first one.")}</Note>;
  else {
    rows = (
      <>
        <HeaderRow />
        {repos.map((r) => (
          <Link key={r.repo} className="tr repos-grid" to={repoPath(r.repo)} role="row">
            <div className="ident" role="cell">
              <RepoTile repo={r.repo} />
              <div className="stack">
                <div className="line1"><span className="name">{r.repo}</span></div>
                <div className="line2">#{r.last.id} · {trigger(r.last)}</div>
              </div>
            </div>
            <div role="cell"><Strip job={r.last} /></div>
            <div role="cell"><Pill status={r.last.status} /></div>
            <div role="cell" className="right num col-count">{r.count}</div>
            <div role="cell" className="right col-time muted">{ago(r.last.created_at)}</div>
          </Link>
        ))}
      </>
    );
  }

  return (
    <>
      <Header title={t("Repositories")} lede={lede} />
      <div className="table" role="table" aria-label={t("Repositories")}>{rows}</div>
    </>
  );
}
