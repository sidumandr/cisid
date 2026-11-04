// Repo shows one repository: its builds, schedules, polled branches,
// notifications and secrets, in tabs, and its build badge.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate } from "react-router";

import { runBuild } from "../lib/actions";
import { getJSON, request } from "../lib/api";
import { openDialog } from "../lib/dialog";
import { ago, finding, inWords, repoPath, repoURL, splitList, stampUTC } from "../lib/format";
import { useMe } from "../lib/session";
import type { Job, Notification, Schedule, SecretInfo, Watch } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { Dash, ErrorNote, Header, Note, Skeleton, TabBar, type Tab } from "../ui/parts";
import { JobRow, JobsHeaderRow, LIST_LIMIT } from "./Jobs";
import { NotFound } from "./NotFound";

type RepoTab = "builds" | "schedules" | "polling" | "notifications" | "secrets";
const TABS: readonly Tab<RepoTab>[] = [
  { id: "builds", label: "Builds" },
  { id: "schedules", label: "Schedules" },
  { id: "polling", label: "Polling" },
  { id: "notifications", label: "Notifications" },
  { id: "secrets", label: "Secrets" },
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
  const [lede, setLede] = useState("Loading builds");
  const count = useCallback((id: RepoTab, n: number) => setCounts((c) => ({ ...c, [id]: n })), []);
  const base = `/api/repos/${repoURL(repo)}`;

  let body: ReactNode;
  switch (tab) {
    case "builds": body = <Builds repo={repo} count={count} setLede={setLede} />; break;
    case "schedules": body = <Schedules repo={repo} base={base} count={count} />; break;
    case "polling": body = <Polling repo={repo} base={base} count={count} />; break;
    case "notifications": body = <Notifications repo={repo} base={base} count={count} />; break;
    case "secrets": body = <Secrets repo={repo} base={base} count={count} />; break;
  }

  return (
    <>
      <Header
        crumbs={<><Link to="/repos">Repositories</Link><span aria-hidden="true">/</span><span>{repo}</span></>}
        title={repo}
        lede={lede}
        actions={<>
          <button className="btn" type="button" onClick={() => showBadge(repo)}>Badge</button>
          <button className="btn primary" type="button" onClick={async () => {
            const job = await runBuild(repo);
            if (job) navigate("/jobs/" + job.id);
          }}>Run build</button>
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
    const t = setInterval(load, every);
    return () => clearInterval(t);
  }, [load, every]);
}

const message = (err: unknown) => (err instanceof Error ? err.message : String(err));

function Builds({ repo, count, setLede }: { repo: string; count: (id: RepoTab, n: number) => void; setLede: (s: string) => void }) {
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const list = (await getJSON<Job[]>(`/api/jobs?limit=${LIST_LIMIT}`)).filter((j) => j.repo === repo);
      setJobs(list);
      setFailure(null);
      count("builds", list.length);
      setLede(list[0] ? `Last build #${list[0].id}: ${finding(list[0])}` : "No builds in the latest jobs.");
    } catch (err) {
      setFailure(message(err));
    }
  }, [repo, count, setLede]);
  useLoad(load, 5000);

  let rows;
  if (failure) rows = <ErrorNote text={`Couldn't load builds: ${failure}`} onRetry={load} />;
  else if (!jobs) rows = <><JobsHeaderRow /><Skeleton grid="jobs-grid" rows={3} /></>;
  else if (jobs.length === 0) rows = <Note>No builds of this repository in the latest jobs.</Note>;
  else rows = <><JobsHeaderRow />{jobs.map((j) => <JobRow key={j.id} job={j} />)}</>;
  return <div className="table" role="table" aria-label="Builds">{rows}</div>;
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
      title: "New schedule",
      text: `Builds a branch of ${repo} at the times the cron matches.`,
      fields: [
        { name: "name", label: "Name", placeholder: "nightly", hint: "Lowercase letters, digits, dots, dashes and underscores." },
        { name: "cron", label: "Cron", placeholder: "0 3 * * *", hint: "Five fields, or @daily, @hourly. 0 3 * * * builds at 03:00 UTC every day." },
        { name: "branch", label: "Branch", hint: "Empty builds the default branch." },
      ],
      submit: "Save schedule",
      onSubmit: async (v) => {
        const saved = await request("PUT", `${base}/schedules/${encodeURIComponent((v.name ?? "").trim())}`,
          { body: { cron: (v.cron ?? "").trim(), branch: (v.branch ?? "").trim() }, token: true });
        if (!saved) throw new Error("Saving a schedule needs a maintainer.");
      },
    });
    if (done) void load();
  }

  async function remove(s: Schedule) {
    const ok = await openDialog({
      title: `Delete ${s.name}?`,
      text: `${repo} stops building ${s.branch || "its default branch"} at ${s.cron}. Builds it already started stay.`,
      submit: "Delete schedule",
      danger: true,
      onSubmit: async () => {
        if (!(await request("DELETE", `${base}/schedules/${encodeURIComponent(s.name)}`, { token: true }))) throw new Error("Deleting needs a maintainer.");
      },
    });
    if (ok) void load();
  }

  let rows;
  if (failure) rows = <ErrorNote text={`Couldn't load schedules: ${failure}`} onRetry={load} />;
  else if (!list) rows = null;
  else if (list.length === 0) rows = <Note>No schedules. A schedule builds a branch on a timetable, such as every night.</Note>;
  else {
    rows = (
      <>
        <div className="tr th sched-grid" role="row">
          <span role="columnheader">Name</span><span role="columnheader">Cron</span><span role="columnheader">Branch</span>
          <span role="columnheader">Next run</span><span role="columnheader">Last build</span><span role="columnheader" />
        </div>
        {list.map((s) => (
          <div key={s.name} className="tr sched-grid" role="row">
            <div role="cell" className="line1"><span className="name">{s.name}</span></div>
            <div role="cell" className="num">{s.cron}</div>
            <div role="cell">{s.branch || <span className="muted">default branch</span>}</div>
            <div role="cell"><div className="num">{stampUTC(s.next_run_at)}</div><div className="line2">{inWords(s.next_run_at)}</div></div>
            <div role="cell">{s.last_job_id ? <Link className="num" to={"/jobs/" + s.last_job_id}>#{s.last_job_id}</Link> : <Dash />}</div>
            <div role="cell" className="right"><button className="text-action danger" type="button" onClick={() => remove(s)}>Delete</button></div>
          </div>
        ))}
      </>
    );
  }
  return (
    <>
      <div className="toolbar">
        <span className="muted small">Times are UTC unless the cron starts with CRON_TZ=.</span>
        <button className="btn small" type="button" onClick={add}>New schedule</button>
      </div>
      <div className="table" role="table" aria-label="Schedules">{rows}</div>
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
      title: "Poll a branch",
      text: `Checks a branch of ${repo} for new commits and builds each one. The first poll builds the branch's current head.`,
      fields: [{ name: "branch", label: "Branch", hint: "Empty polls the default branch." }],
      submit: "Poll branch",
      onSubmit: async (v) => {
        if (!(await request("POST", `${base}/watches`, { body: { branch: (v.branch ?? "").trim() }, token: true }))) {
          throw new Error("Polling a branch needs a maintainer.");
        }
      },
    });
    if (done) void load();
  }

  async function remove(w: Watch) {
    const ok = await openDialog({
      title: `Stop polling ${w.branch}?`,
      text: `New commits on ${w.branch} are no longer built without a webhook. Builds already started stay.`,
      submit: "Stop polling",
      danger: true,
      onSubmit: async () => {
        const path = w.branch.split("/").map(encodeURIComponent).join("/");
        if (!(await request("DELETE", `${base}/watches/${path}`, { token: true }))) throw new Error("Stopping needs a maintainer.");
      },
    });
    if (ok) void load();
  }

  let rows;
  if (failure) rows = <ErrorNote text={`Couldn't load polled branches: ${failure}`} onRetry={load} />;
  else if (!list) rows = null;
  else if (list.length === 0) rows = <Note>No polled branches. Poll a branch when the forge's webhooks cannot reach this server.</Note>;
  else {
    rows = (
      <>
        <div className="tr th watch-grid" role="row">
          <span role="columnheader">Branch</span><span role="columnheader">Last commit</span>
          <span role="columnheader">Last poll</span><span role="columnheader">Last build</span><span role="columnheader" />
        </div>
        {list.map((w) => (
          <div key={w.branch} className="tr watch-grid" role="row">
            <div role="cell" className="line1"><span className="name">{w.branch}</span></div>
            <div role="cell" className="num">{w.last_sha ? w.last_sha.slice(0, 7) : <span className="muted">not polled yet</span>}</div>
            <div role="cell">
              <div className="num">{w.last_poll_at ? ago(w.last_poll_at) : "-"}</div>
              {w.last_error ? <div className="line2 failing">{w.last_error}</div> : null}
            </div>
            <div role="cell">{w.last_job_id ? <Link className="num" to={"/jobs/" + w.last_job_id}>#{w.last_job_id}</Link> : <Dash />}</div>
            <div role="cell" className="right"><button className="text-action danger" type="button" onClick={() => remove(w)}>Stop</button></div>
          </div>
        ))}
      </>
    );
  }
  return (
    <>
      <div className="toolbar">
        <span className="muted small">Each new commit on a polled branch is built as a push, without a webhook.</span>
        <button className="btn small" type="button" onClick={add}>Poll a branch</button>
      </div>
      <div className="table" role="table" aria-label="Polled branches">{rows}</div>
    </>
  );
}

const NOTIFY_KINDS = [
  { value: "slack", label: "Slack, or a chat that takes its webhooks" },
  { value: "webhook", label: "Webhook (JSON)" },
  { value: "email", label: "Email" },
];
const KIND_WORD: Record<string, string> = { slack: "Chat", webhook: "Webhook", email: "Email" };
const NOTIFY_WHEN = [
  { value: "failed,fixed", label: "Every failure, and when fixed" },
  { value: "broken,fixed", label: "Only when it breaks or is fixed" },
  { value: "failed", label: "Every failure" },
  { value: "failed,passed", label: "Every result" },
];
const RESULT_WORD: Record<string, string> = { failed: "failures", broken: "breaks", fixed: "fixes", passed: "passes" };

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
      title: "Add a notification",
      text: `Announces build results of ${repo}. Saving a name again replaces it.`,
      fields: [
        { name: "name", label: "Name", placeholder: "team-chat", hint: "Lowercase letters, digits, dots, dashes and underscores." },
        { name: "kind", label: "Sends to", options: NOTIFY_KINDS },
        { name: "target", label: "URL or addresses", placeholder: "https://hooks.slack.com/services/...",
          hint: "A chat's incoming webhook URL, a webhook URL, or email addresses separated by commas. URLs are kept encrypted and never shown again." },
        { name: "when", label: "When", options: NOTIFY_WHEN },
        { name: "branches", label: "Branches", placeholder: "main, release/*", hint: "Optional. Empty means every branch." },
        { name: "prs", label: "Pull requests", options: [
          { value: "no", label: "Not announced" },
          { value: "yes", label: "Announced, by the branch they merge into" },
        ] },
        { name: "secret", label: "Signing secret", type: "password", hint: "Webhooks only, optional: signs each body in X-Cisid-Signature." },
      ],
      submit: "Save notification",
      onSubmit: async (v) => {
        const payload: Record<string, unknown> = {
          kind: v.kind, on: (v.when ?? "").split(","), branches: splitList(v.branches ?? ""), pull_requests: v.prs === "yes",
        };
        if (v.kind === "email") payload.to = splitList(v.target ?? "");
        else payload.url = (v.target ?? "").trim();
        if (v.secret) payload.secret = v.secret;
        try {
          const saved = await request("PUT", `${base}/notifications/${encodeURIComponent((v.name ?? "").trim())}`, { body: payload, token: true });
          if (!saved) throw new Error("Saving a notification needs a maintainer.");
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
      title: `Delete ${n.name}?`,
      text: `Build results of ${repo} stop going to ${n.target}.`,
      submit: "Delete notification",
      danger: true,
      onSubmit: async () => {
        if (!(await request("DELETE", `${base}/notifications/${encodeURIComponent(n.name)}`, { token: true }))) throw new Error("Deleting needs a maintainer.");
      },
    });
    if (ok) void load();
  }

  const scope = (n: Notification) => {
    const branches = n.branches && n.branches.length ? n.branches.join(", ") : "every branch";
    return n.pull_requests ? `${branches}, with pull requests` : branches;
  };

  let rows;
  if (denied) rows = <Note action={<button className="text-action" type="button" onClick={load}>Sign in</button>}>Notifications are for maintainers.</Note>;
  else if (failure) rows = <ErrorNote text={`Couldn't load notifications: ${failure}`} onRetry={load} />;
  else if (!list) rows = null;
  else if (list.length === 0) rows = <Note>No notifications. Add one to hear when a branch breaks or is fixed.</Note>;
  else {
    rows = (
      <>
        <div className="tr th notify-grid" role="row">
          <span role="columnheader">Name</span><span role="columnheader">Sends to</span>
          <span role="columnheader">For</span><span role="columnheader">Last sent</span><span role="columnheader" />
        </div>
        {list.map((n) => (
          <div key={n.name} className="tr notify-grid" role="row">
            <div role="cell"><div className="line1"><span className="name">{n.name}</span></div><div className="line2">{scope(n)}</div></div>
            <div role="cell"><div className="line1">{KIND_WORD[n.kind] || n.kind}</div><div className="line2">{n.target}</div></div>
            <div role="cell">{n.on.map((r) => RESULT_WORD[r] || r).join(", ")}</div>
            <div role="cell">
              <div className="num">{n.last_sent_at ? ago(n.last_sent_at) : "never"}</div>
              {n.last_error ? <div className="line2 failing">{n.last_error}</div> : null}
            </div>
            <div role="cell" className="right row-actions">
              <button className="text-action" type="button" disabled={sending === n.name} onClick={() => test(n)}>{sending === n.name ? "Sending" : "Test"}</button>
              <button className="text-action danger" type="button" onClick={() => remove(n)}>Delete</button>
            </div>
          </div>
        ))}
      </>
    );
  }
  return (
    <>
      <div className="toolbar">
        <span className="muted small">Build results of this repository, sent to a chat, a webhook or email.</span>
        <button className="btn small" type="button" onClick={add}>Add notification</button>
      </div>
      <div className="table" role="table" aria-label="Notifications">{rows}</div>
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
      title: "Add a secret",
      text: `Steps of ${repo} that list it under secrets get it as an environment variable. Saving a name again replaces it.`,
      fields: [
        { name: "name", label: "Name", placeholder: "DEPLOY_TOKEN" },
        { name: "value", label: "Value", multiline: true },
        { name: "branches", label: "Branches", placeholder: "main, release/*", hint: "Optional. Limits it to pushes to these branches; pull requests then never get it." },
      ],
      submit: "Save secret",
      onSubmit: async (v) => {
        const saved = await request("PUT", `${base}/secrets/${encodeURIComponent((v.name ?? "").trim())}`,
          { body: { value: v.value ?? "", branches: splitList(v.branches ?? "") }, token: true });
        if (!saved) throw new Error("Saving a secret needs a maintainer.");
      },
    });
    if (done) void load();
  }

  async function remove(s: SecretInfo) {
    const ok = await openDialog({
      title: `Delete ${s.name}?`,
      text: `Steps of ${repo} that ask for ${s.name} run without it from the next build on.`,
      submit: "Delete secret",
      danger: true,
      onSubmit: async () => {
        if (!(await request("DELETE", `${base}/secrets/${encodeURIComponent(s.name)}`, { token: true }))) throw new Error("Deleting needs a maintainer.");
      },
    });
    if (ok) void load();
  }

  let rows;
  if (denied) rows = <Note action={<button className="text-action" type="button" onClick={load}>Sign in</button>}>Secrets are for maintainers.</Note>;
  else if (failure) rows = <ErrorNote text={`Couldn't load secrets: ${failure}`} onRetry={load} />;
  else if (!list) rows = null;
  else if (list.length === 0) rows = <Note>No secrets. Add one for a deploy key or a registry token.</Note>;
  else {
    rows = (
      <>
        <div className="tr th secret-grid" role="row">
          <span role="columnheader">Name</span><span role="columnheader">Given to</span>
          <span role="columnheader">Updated</span><span role="columnheader" />
        </div>
        {list.map((s) => (
          <div key={s.name} className="tr secret-grid" role="row">
            <div role="cell" className="num">{s.name}</div>
            <div role="cell">{s.branches && s.branches.length
              ? <span>pushes to {s.branches.join(", ")}</span>
              : <span className="muted">every build except pull requests from forks</span>}</div>
            <div role="cell" className="muted">{ago(s.updated_at)}</div>
            <div role="cell" className="right"><button className="text-action danger" type="button" onClick={() => remove(s)}>Delete</button></div>
          </div>
        ))}
      </>
    );
  }
  return (
    <>
      <div className="toolbar">
        <span className="muted small">Values are never shown again. Steps get a secret only when they ask for it.</span>
        <button className="btn small" type="button" onClick={add}>Add secret</button>
      </div>
      <div className="table" role="table" aria-label="Secrets">{rows}</div>
    </>
  );
}

// BadgePreview shows the badge and the Markdown to copy.
function BadgePreview({ path, markdown }: { path: string; markdown: string }) {
  return (
    <div className="badge-preview">
      <img src={path} alt="Build status" height={20} />
      <pre className="snippet">{markdown}</pre>
    </div>
  );
}

// showBadge offers the repository's build badge for a README.
function showBadge(repo: string) {
  const path = `/api/repos/${repoURL(repo)}/badge.svg`;
  const markdown = `[![cisid](${location.origin}${path})](${location.origin}${repoPath(repo)})`;
  const notes = ["Shows how the latest build of the default branch went. Add ?branch=name to the image URL for another branch."];
  return openDialog({
    title: "Build badge",
    text: notes.join(" "),
    extra: <BadgeNote path={path} markdown={markdown} />,
    submit: "Copy Markdown",
    cancel: "Close",
    onSubmit: async () => {
      await navigator.clipboard.writeText(markdown).catch(() => {
        throw new Error("Couldn't copy. Select the text above and copy it.");
      });
    },
  });
}

function BadgeNote({ path, markdown }: { path: string; markdown: string }) {
  const me = useMe();
  return (
    <>
      {me.public_read ? null : <p>Reading needs a sign-in on this server, so the badge shows elsewhere only with CISID_PUBLIC_BADGES=true.</p>}
      <BadgePreview path={path} markdown={markdown} />
    </>
  );
}
