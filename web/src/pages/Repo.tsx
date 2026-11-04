// Repo shows one repository: its builds, schedules, polled branches,
// notifications and secrets, in tabs, and its build badge.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate } from "react-router";

import { runBuild } from "../lib/actions";
import { getJSON, request } from "../lib/api";
import { openDialog } from "../lib/dialog";
import { ago, finding, inWords, repoPath, repoURL, splitList, stampUTC } from "../lib/format";
import { N_, t } from "../lib/i18n";
import { useMe } from "../lib/session";
import type { Job, Notification, RepositoryInfo, Schedule, SecretInfo, Watch } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { Dash, ErrorNote, Header, Note, Skeleton, TabBar, type Tab } from "../ui/parts";
import { JobRow, JobsHeaderRow, LIST_LIMIT } from "./Jobs";
import { NotFound } from "./NotFound";

type RepoTab = "builds" | "schedules" | "polling" | "notifications" | "secrets";
const TABS: readonly Tab<RepoTab>[] = [
  { id: "builds", label: N_("Builds") },
  { id: "schedules", label: N_("Schedules") },
  { id: "polling", label: N_("Polling") },
  { id: "notifications", label: N_("Notifications") },
  { id: "secrets", label: N_("Secrets") },
];

type Counts = Partial<Record<RepoTab, number | null>>;

// repoFromPath reads the repository key from /repos/{owner}/{repo}, where
// the owner may hold an escaped forge prefix and slashes.
function repoFromPath(pathname: string): string | null {
  const m = pathname.match(/^\/repos\/([^/]+)\/([^/]+)\/?$/);
  if (!m) return null;
  try {
    return `${decodeURIComponent(m[1]!)}/${decodeURIComponent(m[2]!)}`;
  } catch {
    return null;
  }
}

export function Repo() {
  const repo = repoFromPath(useLocation().pathname);
  if (!repo) return <NotFound />;
  return <RepoPage key={repo} repo={repo} />;
}

function RepoPage({ repo }: { repo: string }) {
  useTitle(repo);
  const navigate = useNavigate();
  const [tab, setTab] = useState<RepoTab>("builds");
  const [counts, setCounts] = useState<Counts>({});
  const [lede, setLede] = useState(() => t("Loading builds"));
  const count = useCallback((id: RepoTab, n: number) => setCounts((c) => ({ ...c, [id]: n })), []);
  const base = `/api/repos/${repoURL(repo)}`;
  // Whether it is connected, and how cisid hears of its commits.
  const [connected, setConnected] = useState<RepositoryInfo | null>(null);
  useEffect(() => {
    getJSON<RepositoryInfo[]>("/api/repos").then((l) => setConnected(l.find((r) => r.repo === repo) ?? null)).catch(() => {});
  }, [repo]);

  async function disconnect() {
    const ok = await openDialog({
      title: t("Disconnect {repo}?", { repo }),
      text: t("cisid stops polling it and running its schedules. Its builds, secrets and notifications stay, and a webhook on its forge still starts builds until you remove it."),
      submit: t("Disconnect"),
      danger: true,
      onSubmit: async () => {
        if (!(await request("DELETE", base, { token: true }))) throw new Error(t("Disconnecting needs a maintainer."));
      },
    });
    if (ok) navigate("/repos");
  }

  let body: ReactNode;
  switch (tab) {
    case "builds": body = <Builds repo={repo} connected={connected} count={count} setLede={setLede} />; break;
    case "schedules": body = <Schedules repo={repo} base={base} count={count} />; break;
    case "polling": body = <Polling repo={repo} base={base} count={count} />; break;
    case "notifications": body = <Notifications repo={repo} base={base} count={count} />; break;
    case "secrets": body = <Secrets repo={repo} base={base} count={count} />; break;
  }

  return (
    <>
      <Header
        crumbs={<><Link to="/repos">{t("Repositories")}</Link><span aria-hidden="true">/</span><span>{repo}</span></>}
        title={repo}
        lede={lede}
        actions={<>
          {connected ? <button className="btn" type="button" onClick={disconnect}>{t("Disconnect")}</button> : null}
          <button className="btn" type="button" onClick={() => showBadge(repo)}>{t("Badge")}</button>
          <button className="btn primary" type="button" onClick={async () => {
            const job = await runBuild(repo);
            if (job) navigate("/jobs/" + job.id);
          }}>{t("Run build")}</button>
        </>}
      />
      <TabBar tabs={TABS} selected={tab} onSelect={setTab} counts={counts} />
      <div>{body}</div>
    </>
  );
}

// useLoad runs load now and every `every` ms, and again on demand.
function useLoad(load: () => Promise<void>, every = 0) {
  useEffect(() => {
    void load();
    if (!every) return;
    const timer = setInterval(load, every);
    return () => clearInterval(timer);
  }, [load, every]);
}

const message = (err: unknown) => (err instanceof Error ? err.message : String(err));

function Builds({ repo, connected, count, setLede }: {
  repo: string;
  connected: RepositoryInfo | null;
  count: (id: RepoTab, n: number) => void;
  setLede: (s: string) => void;
}) {
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const list = (await getJSON<Job[]>(`/api/jobs?limit=${LIST_LIMIT}`)).filter((j) => j.repo === repo);
      setJobs(list);
      setFailure(null);
      count("builds", list.length);
      if (list[0]) setLede(t("Last build #{id}: {finding}", { id: list[0].id, finding: finding(list[0]) }));
      else if (connected?.polled) setLede(t("Connected, polling {branch}. Its first build starts at the next poll, or now with Run build.", { branch: connected.default_branch }));
      else if (connected) setLede(t("Connected. Its first build starts with the forge's next webhook, or now with Run build."));
      else setLede(t("No builds in the latest jobs."));
    } catch (err) {
      setFailure(message(err));
    }
  }, [repo, connected, count, setLede]);
  useLoad(load, 5000);

  let rows;
  if (failure) rows = <ErrorNote text={t("Couldn't load builds: {reason}", { reason: failure })} onRetry={load} />;
  else if (!jobs) rows = <><JobsHeaderRow /><Skeleton grid="jobs-grid" rows={3} /></>;
  else if (jobs.length === 0) rows = <Note>{t("No builds of this repository in the latest jobs.")}</Note>;
  else rows = <><JobsHeaderRow />{jobs.map((j) => <JobRow key={j.id} job={j} />)}</>;
  return <div className="table" role="table" aria-label={t("Builds")}>{rows}</div>;
}

function Schedules({ repo, base, count }: { repo: string; base: string; count: (id: RepoTab, n: number) => void }) {
  const [list, setList] = useState<Schedule[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const l = await getJSON<Schedule[]>(`${base}/schedules`);
      setList(l);
      setFailure(null);
      count("schedules", l.length);
    } catch (err) {
      setFailure(message(err));
    }
  }, [base, count]);
  useLoad(load);

  async function add() {
    const done = await openDialog({
      title: t("New schedule"),
      text: t("Builds a branch of {repo} at the times the cron matches.", { repo }),
      fields: [
        { name: "name", label: t("Name"), placeholder: "nightly", hint: t("Lowercase letters, digits, dots, dashes and underscores.") },
        { name: "cron", label: t("Cron"), placeholder: "0 3 * * *", hint: t("Five fields, or @daily, @hourly. 0 3 * * * builds at 03:00 UTC every day.") },
        { name: "branch", label: t("Branch"), hint: t("Empty builds the default branch.") },
      ],
      submit: t("Save schedule"),
      onSubmit: async (v) => {
        const saved = await request("PUT", `${base}/schedules/${encodeURIComponent((v.name ?? "").trim())}`,
          { body: { cron: (v.cron ?? "").trim(), branch: (v.branch ?? "").trim() }, token: true });
        if (!saved) throw new Error(t("Saving a schedule needs a maintainer."));
      },
    });
    if (done) void load();
  }

  async function remove(s: Schedule) {
    const ok = await openDialog({
      title: t("Delete {name}?", { name: s.name }),
      text: t("{repo} stops building {branch} at {cron}. Builds it already started stay.", { repo, branch: s.branch || t("its default branch"), cron: s.cron }),
      submit: t("Delete schedule"),
      danger: true,
      onSubmit: async () => {
        if (!(await request("DELETE", `${base}/schedules/${encodeURIComponent(s.name)}`, { token: true }))) throw new Error(t("Deleting needs a maintainer."));
      },
    });
    if (ok) void load();
  }

  let rows;
  if (failure) rows = <ErrorNote text={t("Couldn't load schedules: {reason}", { reason: failure })} onRetry={load} />;
  else if (!list) rows = null;
  else if (list.length === 0) rows = <Note>{t("No schedules. A schedule builds a branch on a timetable, such as every night.")}</Note>;
  else {
    rows = (
      <>
        <div className="tr th sched-grid" role="row">
          <span role="columnheader">{t("Name")}</span><span role="columnheader">{t("Cron")}</span><span role="columnheader">{t("Branch")}</span>
          <span role="columnheader">{t("Next run")}</span><span role="columnheader">{t("Last build")}</span><span role="columnheader" />
        </div>
        {list.map((s) => (
          <div key={s.name} className="tr sched-grid" role="row">
            <div role="cell" className="line1"><span className="name">{s.name}</span></div>
            <div role="cell" className="num">{s.cron}</div>
            <div role="cell">{s.branch || <span className="muted">{t("default branch")}</span>}</div>
            <div role="cell"><div className="num">{stampUTC(s.next_run_at)}</div><div className="line2">{inWords(s.next_run_at)}</div></div>
            <div role="cell">{s.last_job_id ? <Link className="num" to={"/jobs/" + s.last_job_id}>#{s.last_job_id}</Link> : <Dash />}</div>
            <div role="cell" className="right"><button className="text-action danger" type="button" onClick={() => remove(s)}>{t("Delete")}</button></div>
          </div>
        ))}
      </>
    );
  }
  return (
    <>
      <div className="toolbar">
        <span className="muted small">{t("Times are UTC unless the cron starts with CRON_TZ=.")}</span>
        <button className="btn small" type="button" onClick={add}>{t("New schedule")}</button>
      </div>
      <div className="table" role="table" aria-label={t("Schedules")}>{rows}</div>
    </>
  );
}

// Polling lists the branches the server polls for new commits, for a
// server the forge's webhooks cannot reach.
function Polling({ repo, base, count }: { repo: string; base: string; count: (id: RepoTab, n: number) => void }) {
  const [list, setList] = useState<Watch[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const l = await getJSON<Watch[]>(`${base}/watches`);
      setList(l);
      setFailure(null);
      count("polling", l.length);
    } catch (err) {
      setFailure(message(err));
    }
  }, [base, count]);
  useLoad(load, 10000);

  async function add() {
    const done = await openDialog({
      title: t("Poll a branch"),
      text: t("Checks a branch of {repo} for new commits and builds each one. The first poll builds the branch's current head.", { repo }),
      fields: [{ name: "branch", label: t("Branch"), hint: t("Empty polls the default branch.") }],
      submit: t("Poll branch"),
      onSubmit: async (v) => {
        if (!(await request("POST", `${base}/watches`, { body: { branch: (v.branch ?? "").trim() }, token: true }))) {
          throw new Error(t("Polling a branch needs a maintainer."));
        }
      },
    });
    if (done) void load();
  }

  async function remove(w: Watch) {
    const ok = await openDialog({
      title: t("Stop polling {branch}?", { branch: w.branch }),
      text: t("New commits on {branch} are no longer built without a webhook. Builds already started stay.", { branch: w.branch }),
      submit: t("Stop polling"),
      danger: true,
      onSubmit: async () => {
        const path = w.branch.split("/").map(encodeURIComponent).join("/");
        if (!(await request("DELETE", `${base}/watches/${path}`, { token: true }))) throw new Error(t("Stopping needs a maintainer."));
      },
    });
    if (ok) void load();
  }

  let rows;
  if (failure) rows = <ErrorNote text={t("Couldn't load polled branches: {reason}", { reason: failure })} onRetry={load} />;
  else if (!list) rows = null;
  else if (list.length === 0) rows = <Note>{t("No polled branches. Poll a branch when the forge's webhooks cannot reach this server.")}</Note>;
  else {
    rows = (
      <>
        <div className="tr th watch-grid" role="row">
          <span role="columnheader">{t("Branch")}</span><span role="columnheader">{t("Last commit")}</span>
          <span role="columnheader">{t("Last poll")}</span><span role="columnheader">{t("Last build")}</span><span role="columnheader" />
        </div>
        {list.map((w) => (
          <div key={w.branch} className="tr watch-grid" role="row">
            <div role="cell" className="line1"><span className="name">{w.branch}</span></div>
            <div role="cell" className="num">{w.last_sha ? w.last_sha.slice(0, 7) : <span className="muted">{t("not polled yet")}</span>}</div>
            <div role="cell">
              <div className="num">{w.last_poll_at ? ago(w.last_poll_at) : "-"}</div>
              {w.last_error ? <div className="line2 failing">{w.last_error}</div> : null}
            </div>
            <div role="cell">{w.last_job_id ? <Link className="num" to={"/jobs/" + w.last_job_id}>#{w.last_job_id}</Link> : <Dash />}</div>
            <div role="cell" className="right"><button className="text-action danger" type="button" onClick={() => remove(w)}>{t("Stop")}</button></div>
          </div>
        ))}
      </>
    );
  }
  return (
    <>
      <div className="toolbar">
        <span className="muted small">{t("Each new commit on a polled branch is built as a push, without a webhook.")}</span>
        <button className="btn small" type="button" onClick={add}>{t("Poll a branch")}</button>
      </div>
      <div className="table" role="table" aria-label={t("Polled branches")}>{rows}</div>
    </>
  );
}

const NOTIFY_KINDS = [
  { value: "slack", label: N_("Slack, or a chat that takes its webhooks") },
  { value: "webhook", label: N_("Webhook (JSON)") },
  { value: "email", label: N_("Email") },
];
const KIND_WORD: Record<string, string> = { slack: N_("Chat"), webhook: N_("Webhook"), email: N_("Email") };
const NOTIFY_WHEN = [
  { value: "failed,fixed", label: N_("Every failure, and when fixed") },
  { value: "broken,fixed", label: N_("Only when it breaks or is fixed") },
  { value: "failed", label: N_("Every failure") },
  { value: "failed,passed", label: N_("Every result") },
];
const RESULT_WORD: Record<string, string> = { failed: N_("failures"), broken: N_("breaks"), fixed: N_("fixes"), passed: N_("passes") };
// translated gives a table of choices in the language chosen.
const translated = (choices: { value: string; label: string }[]) => choices.map((c) => ({ value: c.value, label: t(c.label) }));

// Notifications says where build results are announced: a chat, a webhook
// or email.
function Notifications({ repo, base, count }: { repo: string; base: string; count: (id: RepoTab, n: number) => void }) {
  const [list, setList] = useState<Notification[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [denied, setDenied] = useState(false);
  const [sending, setSending] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const l = await request<Notification[]>("GET", `${base}/notifications`, { token: true });
      if (!l) {
        setDenied(true);
        return;
      }
      setDenied(false);
      setList(l);
      setFailure(null);
      count("notifications", l.length);
    } catch (err) {
      setFailure(message(err));
    }
  }, [base, count]);
  useLoad(load);

  async function add() {
    const done = await openDialog({
      title: t("Add a notification"),
      text: t("Announces build results of {repo}. Saving a name again replaces it.", { repo }),
      fields: [
        { name: "name", label: t("Name"), placeholder: "team-chat", hint: t("Lowercase letters, digits, dots, dashes and underscores.") },
        { name: "kind", label: t("Sends to"), options: translated(NOTIFY_KINDS) },
        { name: "target", label: t("URL or addresses"), placeholder: "https://hooks.slack.com/services/...",
          hint: t("A chat's incoming webhook URL, a webhook URL, or email addresses separated by commas. URLs are kept encrypted and never shown again.") },
        { name: "when", label: t("When"), options: translated(NOTIFY_WHEN) },
        { name: "branches", label: t("Branches"), placeholder: "main, release/*", hint: t("Optional. Empty means every branch.") },
        { name: "prs", label: t("Pull requests"), options: [
          { value: "no", label: t("Not announced") },
          { value: "yes", label: t("Announced, by the branch they merge into") },
        ] },
        { name: "secret", label: t("Signing secret"), type: "password", hint: t("Webhooks only, optional: signs each body in X-Cisid-Signature.") },
      ],
      submit: t("Save notification"),
      onSubmit: async (v) => {
        const payload: Record<string, unknown> = {
          kind: v.kind, on: (v.when ?? "").split(","), branches: splitList(v.branches ?? ""), pull_requests: v.prs === "yes",
        };
        if (v.kind === "email") payload.to = splitList(v.target ?? "");
        else payload.url = (v.target ?? "").trim();
        if (v.secret) payload.secret = v.secret;
        try {
          const saved = await request("PUT", `${base}/notifications/${encodeURIComponent((v.name ?? "").trim())}`, { body: payload, token: true });
          if (!saved) throw new Error(t("Saving a notification needs a maintainer."));
        } catch (err) {
          // "invalid notification: url must be ..." reads as "Url must be ...".
          const why = message(err).replace(/^invalid notification: /, "");
          throw new Error(why.charAt(0).toUpperCase() + why.slice(1) + (why.endsWith(".") ? "" : "."));
        }
      },
    });
    if (done) void load();
  }

  async function test(n: Notification) {
    setSending(n.name);
    try {
      await request("POST", `${base}/notifications/${encodeURIComponent(n.name)}/test`, { token: true });
    } catch {
      // The row shows why, from the notification's last error.
    }
    setSending(null);
    void load();
  }

  async function remove(n: Notification) {
    const ok = await openDialog({
      title: t("Delete {name}?", { name: n.name }),
      text: t("Build results of {repo} stop going to {target}.", { repo, target: n.target }),
      submit: t("Delete notification"),
      danger: true,
      onSubmit: async () => {
        if (!(await request("DELETE", `${base}/notifications/${encodeURIComponent(n.name)}`, { token: true }))) throw new Error(t("Deleting needs a maintainer."));
      },
    });
    if (ok) void load();
  }

  const scope = (n: Notification) => {
    const branches = n.branches && n.branches.length ? n.branches.join(", ") : t("every branch");
    return n.pull_requests ? t("{branches}, with pull requests", { branches }) : branches;
  };

  let rows;
  if (denied) rows = <Note action={<button className="text-action" type="button" onClick={load}>{t("Sign in")}</button>}>{t("Notifications are for maintainers.")}</Note>;
  else if (failure) rows = <ErrorNote text={t("Couldn't load notifications: {reason}", { reason: failure })} onRetry={load} />;
  else if (!list) rows = null;
  else if (list.length === 0) rows = <Note>{t("No notifications. Add one to hear when a branch breaks or is fixed.")}</Note>;
  else {
    rows = (
      <>
        <div className="tr th notify-grid" role="row">
          <span role="columnheader">{t("Name")}</span><span role="columnheader">{t("Sends to")}</span>
          <span role="columnheader">{t("For")}</span><span role="columnheader">{t("Last sent")}</span><span role="columnheader" />
        </div>
        {list.map((n) => (
          <div key={n.name} className="tr notify-grid" role="row">
            <div role="cell"><div className="line1"><span className="name">{n.name}</span></div><div className="line2">{scope(n)}</div></div>
            <div role="cell"><div className="line1">{KIND_WORD[n.kind] ? t(KIND_WORD[n.kind]!) : n.kind}</div><div className="line2">{n.target}</div></div>
            <div role="cell">{n.on.map((r) => (RESULT_WORD[r] ? t(RESULT_WORD[r]!) : r)).join(", ")}</div>
            <div role="cell">
              <div className="num">{n.last_sent_at ? ago(n.last_sent_at) : t("never")}</div>
              {n.last_error ? <div className="line2 failing">{n.last_error}</div> : null}
            </div>
            <div role="cell" className="right row-actions">
              <button className="text-action" type="button" disabled={sending === n.name} onClick={() => test(n)}>{sending === n.name ? t("Sending") : t("Test")}</button>
              <button className="text-action danger" type="button" onClick={() => remove(n)}>{t("Delete")}</button>
            </div>
          </div>
        ))}
      </>
    );
  }
  return (
    <>
      <div className="toolbar">
        <span className="muted small">{t("Build results of this repository, sent to a chat, a webhook or email.")}</span>
        <button className="btn small" type="button" onClick={add}>{t("Add notification")}</button>
      </div>
      <div className="table" role="table" aria-label={t("Notifications")}>{rows}</div>
    </>
  );
}

function Secrets({ repo, base, count }: { repo: string; base: string; count: (id: RepoTab, n: number) => void }) {
  const [list, setList] = useState<SecretInfo[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [denied, setDenied] = useState(false);
  const load = useCallback(async () => {
    try {
      const l = await request<SecretInfo[]>("GET", `${base}/secrets`, { token: true });
      if (!l) {
        setDenied(true);
        return;
      }
      setDenied(false);
      setList(l);
      setFailure(null);
      count("secrets", l.length);
    } catch (err) {
      setFailure(message(err));
    }
  }, [base, count]);
  useLoad(load);

  async function add() {
    const done = await openDialog({
      title: t("Add a secret"),
      text: t("Steps of {repo} that list it under secrets get it as an environment variable. Saving a name again replaces it.", { repo }),
      fields: [
        { name: "name", label: t("Name"), placeholder: "DEPLOY_TOKEN" },
        { name: "value", label: t("Value"), multiline: true },
        { name: "branches", label: t("Branches"), placeholder: "main, release/*", hint: t("Optional. Limits it to pushes to these branches; pull requests then never get it.") },
      ],
      submit: t("Save secret"),
      onSubmit: async (v) => {
        const saved = await request("PUT", `${base}/secrets/${encodeURIComponent((v.name ?? "").trim())}`,
          { body: { value: v.value ?? "", branches: splitList(v.branches ?? "") }, token: true });
        if (!saved) throw new Error(t("Saving a secret needs a maintainer."));
      },
    });
    if (done) void load();
  }

  async function remove(s: SecretInfo) {
    const ok = await openDialog({
      title: t("Delete {name}?", { name: s.name }),
      text: t("Steps of {repo} that ask for {name} run without it from the next build on.", { repo, name: s.name }),
      submit: t("Delete secret"),
      danger: true,
      onSubmit: async () => {
        if (!(await request("DELETE", `${base}/secrets/${encodeURIComponent(s.name)}`, { token: true }))) throw new Error(t("Deleting needs a maintainer."));
      },
    });
    if (ok) void load();
  }

  let rows;
  if (denied) rows = <Note action={<button className="text-action" type="button" onClick={load}>{t("Sign in")}</button>}>{t("Secrets are for maintainers.")}</Note>;
  else if (failure) rows = <ErrorNote text={t("Couldn't load secrets: {reason}", { reason: failure })} onRetry={load} />;
  else if (!list) rows = null;
  else if (list.length === 0) rows = <Note>{t("No secrets. Add one for a deploy key or a registry token.")}</Note>;
  else {
    rows = (
      <>
        <div className="tr th secret-grid" role="row">
          <span role="columnheader">{t("Name")}</span><span role="columnheader">{t("Given to")}</span>
          <span role="columnheader">{t("Updated")}</span><span role="columnheader" />
        </div>
        {list.map((s) => (
          <div key={s.name} className="tr secret-grid" role="row">
            <div role="cell" className="num">{s.name}</div>
            <div role="cell">{s.branches && s.branches.length
              ? <span>{t("pushes to {branches}", { branches: s.branches.join(", ") })}</span>
              : <span className="muted">{t("every build except pull requests from forks")}</span>}</div>
            <div role="cell" className="muted">{ago(s.updated_at)}</div>
            <div role="cell" className="right"><button className="text-action danger" type="button" onClick={() => remove(s)}>{t("Delete")}</button></div>
          </div>
        ))}
      </>
    );
  }
  return (
    <>
      <div className="toolbar">
        <span className="muted small">{t("Values are never shown again. Steps get a secret only when they ask for it.")}</span>
        <button className="btn small" type="button" onClick={add}>{t("Add secret")}</button>
      </div>
      <div className="table" role="table" aria-label={t("Secrets")}>{rows}</div>
    </>
  );
}

// BadgePreview shows the badge and the Markdown to copy.
function BadgePreview({ path, markdown }: { path: string; markdown: string }) {
  return (
    <div className="badge-preview">
      <img src={path} alt={t("Build status")} height={20} />
      <pre className="snippet">{markdown}</pre>
    </div>
  );
}

// showBadge offers the repository's build badge for a README.
function showBadge(repo: string) {
  const path = `/api/repos/${repoURL(repo)}/badge.svg`;
  const markdown = `[![cisid](${location.origin}${path})](${location.origin}${repoPath(repo)})`;
  return openDialog({
    title: t("Build badge"),
    text: t("Shows how the latest build of the default branch went. Add ?branch=name to the image URL for another branch."),
    extra: <BadgeNote path={path} markdown={markdown} />,
    submit: t("Copy Markdown"),
    cancel: t("Close"),
    onSubmit: async () => {
      await navigator.clipboard.writeText(markdown).catch(() => {
        throw new Error(t("Couldn't copy. Select the text above and copy it."));
      });
    },
  });
}

function BadgeNote({ path, markdown }: { path: string; markdown: string }) {
  const me = useMe();
  return (
    <>
      {me.public_read ? null : <p>{t("Reading needs a sign-in on this server, so the badge shows elsewhere only with CISID_PUBLIC_BADGES=true.")}</p>}
      <BadgePreview path={path} markdown={markdown} />
    </>
  );
}
