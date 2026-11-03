"use strict";

// cisid web UI: jobs, a job's steps and logs, and repositories with their
// schedules and secrets. Everything from the API is inserted as text, never
// as HTML.

const FINISHED = new Set(["success", "failed", "canceled"]);
const TOKEN_KEY = "cisid.apiToken";
const THEME_KEY = "cisid.theme";
const LIST_LIMIT = 200;
const app = document.getElementById("app");
const dialog = document.getElementById("dialog");
let stopPage = () => {};

// --- elements ----------------------------------------------------------------

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c !== null && c !== undefined && c !== false) node.append(c);
  }
  return node;
}

const SVG_NS = "http://www.w3.org/2000/svg";
const ICONS = {
  search: ["M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13zM20 20l-4.6-4.6"],
  copy: ["M9 9h10v11H9zM5 15V4h10"],
  check: ["M5 12.5l4.5 4.5L19 7.5"],
};

function icon(name, size = 16) {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", size);
  svg.setAttribute("height", size);
  svg.setAttribute("aria-hidden", "true");
  for (const d of ICONS[name]) {
    const path = document.createElementNS(SVG_NS, "path");
    path.setAttribute("d", d);
    svg.append(path);
  }
  return svg;
}

// --- formatting ---------------------------------------------------------------

const pad2 = (n) => String(n).padStart(2, "0");

function stamp(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

// stampUTC is stamp in UTC, the zone schedules are written in.
function stampUTC(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())} ${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())} UTC`;
}

function ago(iso) {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

function inWords(iso) {
  if (!iso) return "";
  const s = (new Date(iso).getTime() - Date.now()) / 1000;
  if (s < 60) return "in under a minute";
  if (s < 3600) return `in ${Math.round(s / 60)}m`;
  if (s < 86400) return `in ${Math.round(s / 3600)}h`;
  return `in ${Math.round(s / 86400)}d`;
}

function seconds(job) {
  if (!job.started_at) return null;
  const end = job.finished_at ? new Date(job.finished_at) : new Date();
  return Math.max(0, Math.round((end - new Date(job.started_at)) / 1000));
}

function fmtDuration(s) {
  if (s === null) return "";
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s % 60}s`;
  return `${s % 60}s`;
}

const shortSHA = (sha) => (sha || "").slice(0, 7);

function matrixLabel(m) {
  return Object.keys(m || {}).sort().map((k) => `${k}=${m[k]}`).join(", ");
}

const STATUS_WORD = { success: "Passed", failed: "Failed", running: "Running", pending: "Queued", canceled: "Canceled" };

function pill(status) {
  return el("span", { class: "pill", "data-status": status }, STATUS_WORD[status] || status);
}

// trigger says what started a job, in the words of the trade.
function trigger(j) {
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

function repoTile(repo) {
  const name = (repo || "").split("/").pop() || "?";
  return el("span", { class: "tile", "aria-hidden": "true" }, name.slice(0, 1).toLowerCase());
}

// repoURL is a repository key as the two path segments the server routes:
// the last segment, and the rest with the forge and any slashes escaped,
// e.g. "gitlab:group/sub/project" -> "gitlab%3Agroup%2Fsub/project".
function repoURL(repo) {
  const i = repo.lastIndexOf("/");
  return encodeURIComponent(repo.slice(0, i)) + "/" + encodeURIComponent(repo.slice(i + 1));
}

function repoPath(repo) {
  return "/repos/" + repoURL(repo);
}

const dash = () => el("span", { class: "dash" }, "-");

// --- steps --------------------------------------------------------------------

const STATE_WORD = { passed: "passed", failed: "failed", running: "running", canceled: "canceled", queued: "not started", skipped: "not run" };

// stepStates says how far each planned step of a job got.
function stepStates(j) {
  const stopped = j.failed_step ?? null;
  return (j.steps || []).map((name, i) => {
    let state;
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

// strip draws a job's pipeline as one segment per step.
function strip(j) {
  const steps = stepStates(j);
  if (steps.length === 0) {
    const text = j.status === "pending" ? "Waiting for an agent" : FINISHED.has(j.status) ? "No steps ran" : "Planning";
    return el("span", { class: "strip-wait" }, text);
  }
  return el("div", { class: "strip", role: "img", "aria-label": steps.map((s) => `${s.name} ${STATE_WORD[s.state]}`).join(", ") },
    steps.map((s) => el("span", { class: "seg", "data-state": s.state },
      el("span", { class: "seg-bar" }),
      el("span", { class: "seg-label", "aria-hidden": "true" }, s.name))));
}

// finding is the one sentence that says where a job stands.
function finding(j) {
  const steps = j.steps || [];
  const took = fmtDuration(seconds(j));
  const at = (i) => steps[i] ?? `step ${i}`;
  switch (j.status) {
    case "success":
      return `Passed in ${took}${j.agent ? ` on ${j.agent}` : ""}.`;
    case "failed":
      if (j.failed_step === undefined || j.failed_step === null) return `Failed before any step ran: ${j.error || "no reason recorded"}.`;
      return `Failed at ${at(j.failed_step)}${j.exit_code > 0 ? ` with exit code ${j.exit_code}` : ""} after ${took}.`;
    case "canceled":
      return j.failed_step === undefined || j.failed_step === null
        ? "Canceled before it started."
        : `Canceled at ${at(j.failed_step)} after ${took}.`;
    case "running":
      return `Running ${at(j.current_step ?? 0)}${j.agent ? ` on ${j.agent}` : ""} for ${took}.`;
    default:
      return `Waiting for an agent since ${ago(j.created_at)}.`;
  }
}

// --- API ----------------------------------------------------------------------

let signInDeclined = false;

// request calls the API. With token, it sends the API token, asking for it
// when it is missing or wrong. A read the server refuses asks to sign in.
async function request(method, url, { body, token = false } = {}) {
  for (let attempt = 0; attempt < 3; attempt++) {
    const headers = { Accept: "application/json" };
    if (token) {
      const t = await apiToken(attempt > 0);
      if (!t) return null;
      headers.Authorization = "Bearer " + t;
    }
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const resp = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    if (resp.status === 401) {
      if (token) continue;
      if (await signIn()) continue;
      throw new Error("Sign in to see this.");
    }
    if (resp.status === 204) return {};
    const data = await resp.json().catch(() => ({}));
    if (!resp.ok) throw new Error(data.reason || resp.statusText);
    return data;
  }
  throw new Error(token ? "The API token was not accepted." : "The token was not accepted.");
}

const getJSON = (url) => request("GET", url);

async function apiToken(forget) {
  if (forget) sessionStorage.removeItem(TOKEN_KEY);
  const saved = sessionStorage.getItem(TOKEN_KEY);
  if (saved) return saved;
  const values = await openDialog({
    title: forget ? "That token did not work" : "API token",
    text: "Cancelling, rerunning, running builds and managing secrets need the API token (CISID_API_TOKEN). It is kept in this tab only.",
    fields: [{ name: "token", label: "Token", type: "password", autocomplete: "off" }],
    submit: "Use token",
  });
  if (!values || !values.token) return null;
  sessionStorage.setItem(TOKEN_KEY, values.token.trim());
  return values.token.trim();
}

async function signIn() {
  if (signInDeclined) return false;
  const values = await openDialog({
    title: "Sign in",
    text: "This server asks for a token to show jobs and logs (CISID_READ_TOKEN or CISID_API_TOKEN).",
    fields: [{ name: "token", label: "Token", type: "password", autocomplete: "current-password" }],
    submit: "Sign in",
    onSubmit: async (v) => {
      const resp = await fetch("/api/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: v.token }),
      });
      if (!resp.ok) throw new Error("That token was not accepted.");
    },
  });
  if (!values) signInDeclined = true;
  return Boolean(values);
}

// --- dialogs ------------------------------------------------------------------

// openDialog shows a form and resolves with its values, or null when it is
// dismissed. onSubmit may throw to keep the dialog open with the message.
function openDialog({ title, text, fields = [], submit, danger = false, onSubmit }) {
  return new Promise((resolve) => {
    const error = el("p", { class: "form-error", role: "alert" });
    const inputs = fields.map((f) => {
      const id = "f-" + f.name;
      const input = f.multiline
        ? el("textarea", { id, name: f.name, rows: 4, autocomplete: "off", spellcheck: "false" })
        : el("input", { id, name: f.name, type: f.type || "text", autocomplete: f.autocomplete || "off", spellcheck: "false", list: f.list });
      if (f.value) input.value = f.value;
      if (f.placeholder) input.placeholder = f.placeholder;
      return { f, input, node: el("div", { class: "field" }, el("label", { for: id }, f.label), input, f.hint ? el("span", { class: "hint" }, f.hint) : null) };
    });
    const cancel = el("button", { class: "btn", type: "button" }, "Cancel");
    const ok = el("button", { class: danger ? "btn danger" : "btn primary", type: "submit" }, submit);
    const form = el("form", { novalidate: true },
      el("h2", { id: "dialog-title" }, title),
      text ? el("p", {}, text) : null,
      inputs.map((i) => i.node),
      error,
      el("div", { class: "dialog-actions" }, cancel, ok));

    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      dialog.close();
      dialog.replaceChildren();
      resolve(value);
    };
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const values = Object.fromEntries(inputs.map(({ f, input }) => [f.name, input.value]));
      ok.disabled = true;
      error.textContent = "";
      try {
        if (onSubmit) await onSubmit(values);
        finish(values);
      } catch (err) {
        error.textContent = err.message;
        ok.disabled = false;
      }
    });
    cancel.addEventListener("click", () => finish(null));
    dialog.oncancel = (e) => {
      e.preventDefault();
      finish(null);
    };
    dialog.replaceChildren(form);
    dialog.showModal();
    (inputs[0] ? inputs[0].input : ok).focus();
  });
}

// runBuild builds the head of a branch now and opens the new job.
async function runBuild(repo, repos = []) {
  const options = el("datalist", { id: "known-repos" }, repos.map((r) => el("option", { value: r })));
  document.body.append(options);
  const job = await openDialog({
    title: "Run a build",
    text: "Builds the latest commit of a branch, as a push would.",
    fields: [
      { name: "repo", label: "Repository", value: repo, placeholder: "owner/name", list: "known-repos",
        hint: "On GitHub owner/name; elsewhere prefix the forge, as in gitea:owner/name or gitlab:group/project." },
      { name: "branch", label: "Branch", hint: "Empty builds the repository's default branch." },
    ],
    submit: "Run build",
    onSubmit: async (v) => {
      const name = v.repo.trim();
      if (!/^((gitea|forgejo|gitlab|bitbucket):)?[^/\s:]+(\/[^/\s:]+)+$/.test(name)) {
        throw new Error("Write the repository as owner/name, or forge:owner/name.");
      }
      const queued = await request("POST", `/api/repos/${repoURL(name)}/builds`,
        { body: { branch: v.branch.trim() }, token: true });
      if (!queued) throw new Error("A build needs the API token.");
      v.job = queued;
    },
  });
  options.remove();
  if (job && job.job) navigate("/jobs/" + job.job.id);
}

// --- shared pieces ------------------------------------------------------------

function header({ crumbs, title, lede, actions = [] }) {
  return el("div", { class: "head" },
    el("div", { class: "head-main" },
      crumbs ? el("nav", { class: "crumbs", "aria-label": "Breadcrumb" }, crumbs) : null,
      el("h1", {}, title),
      lede),
    actions.length ? el("div", { class: "actions" }, actions) : null);
}

// tabBar draws underline tabs; the selected one carries the orange line.
function tabBar(tabs, selected, onSelect) {
  const buttons = new Map();
  const bar = el("div", { class: "tabs", role: "tablist" });
  for (const t of tabs) {
    const count = el("span", { class: "num" });
    const b = el("button", { class: "tab", type: "button", role: "tab", "aria-selected": String(t.id === selected) },
      t.label, count);
    b.addEventListener("click", () => {
      for (const [id, other] of buttons) other.button.setAttribute("aria-selected", String(id === t.id));
      onSelect(t.id);
    });
    buttons.set(t.id, { button: b, count });
    bar.append(b);
  }
  return {
    el: bar,
    count(id, n) {
      const entry = buttons.get(id);
      if (entry) entry.count.textContent = n === null ? "" : String(n);
    },
  };
}

function searchBox(placeholder, onInput) {
  const input = el("input", { type: "search", placeholder, "aria-label": placeholder, autocomplete: "off", spellcheck: "false" });
  input.addEventListener("input", () => onInput(input.value.trim().toLowerCase()));
  return el("label", { class: "search" }, icon("search"), input);
}

function skeleton(grid, rows = 5) {
  return Array.from({ length: rows }, () => el("div", { class: "tr " + grid, "aria-hidden": "true" },
    el("div", { class: "ident" }, el("span", { class: "tile" }),
      el("div", { class: "stack" }, el("span", { class: "sk", "data-w": "1" }), el("span", { class: "sk", "data-w": "2" }))),
    el("span", { class: "sk" }), el("span", { class: "sk" }), el("span", { class: "sk col-agent" }), el("span", { class: "sk col-time" })));
}

function note(text, action) {
  return el("div", { class: "note" }, el("span", {}, text), action || null);
}

function errorNote(text, retry) {
  return el("div", { class: "note error", role: "alert" },
    el("span", {}, el("span", { class: "sign" }, "Error: "), text),
    retry ? el("button", { class: "text-action", type: "button", onclick: retry }, "Retry") : null);
}

function jobsHeaderRow() {
  return el("div", { class: "tr th jobs-grid", role: "row" },
    el("span", { role: "columnheader" }, "Job"),
    el("span", { role: "columnheader" }, "Steps"),
    el("span", { role: "columnheader" }, "Status"),
    el("span", { role: "columnheader", class: "col-agent" }, "Agent"),
    el("span", { role: "columnheader", class: "right col-time" }, "Duration"));
}

function jobRow(j) {
  const sub = [trigger(j)];
  if (j.matrix) sub.push(matrixLabel(j.matrix));
  return el("a", { class: "tr jobs-grid", href: "/jobs/" + j.id, role: "row" },
    el("div", { class: "ident", role: "cell" },
      repoTile(j.repo),
      el("div", { class: "stack" },
        el("div", { class: "line1" }, el("span", { class: "name" }, j.repo), el("span", { class: "num" }, "#" + j.id)),
        el("div", { class: "line2" }, sub.join(" · "), " · ", el("span", { class: "num" }, shortSHA(j.commit_sha))))),
    el("div", { role: "cell" }, strip(j)),
    el("div", { role: "cell" }, pill(j.status)),
    el("div", { role: "cell", class: "col-agent muted" }, j.agent || dash()),
    el("div", { role: "cell", class: "right col-time" },
      el("div", { class: "num" }, j.started_at ? fmtDuration(seconds(j)) : el("span", { class: "dash wide-only" }, "-")),
      el("div", { class: "line2" }, ago(j.created_at))));
}

// --- jobs ---------------------------------------------------------------------

const JOB_TABS = [
  { id: "all", label: "All", match: () => true },
  { id: "active", label: "Running", match: (j) => j.status === "running" || j.status === "pending" },
  { id: "failed", label: "Failed", match: (j) => j.status === "failed" },
  { id: "pr", label: "Pull requests", match: (j) => j.event === "pull_request" },
];

function matchesSearch(j, q) {
  if (!q) return true;
  return [j.repo, j.branch, j.target_branch, j.commit_sha, "#" + j.id, j.agent, trigger(j)]
    .some((v) => v && v.toLowerCase().includes(q));
}

function jobsFinding(jobs) {
  const running = jobs.filter((j) => j.status === "running").length;
  const queued = jobs.filter((j) => j.status === "pending").length;
  const since = Date.now() - 24 * 3600 * 1000;
  const failed = jobs.filter((j) => j.status === "failed" && new Date(j.finished_at || j.created_at).getTime() > since);
  const now = [];
  if (running) now.push(`${running} running`);
  if (queued) now.push(`${queued} queued`);
  const parts = [now.length ? now.join(", ") + "." : "Nothing running."];
  if (failed.length === 0) parts.push("No failures in the last 24 hours.");
  else {
    const latest = failed[0];
    const step = latest.failed_step !== undefined && latest.failed_step !== null ? ` at ${(latest.steps || [])[latest.failed_step] || "step " + latest.failed_step}` : "";
    parts.push(`${failed.length} failed in the last 24 hours, the latest ${latest.repo} #${latest.id}${step}.`);
  }
  return parts.join(" ");
}

function renderJobs() {
  setNav("jobs");
  document.title = "Jobs · cisid";
  const state = { tab: "all", q: "" };
  let jobs = null;
  let failure = null;

  const lede = el("p", { class: "lede" }, "Loading jobs");
  const run = el("button", { class: "btn primary", type: "button" }, "Run build");
  run.addEventListener("click", () => runBuild("", knownRepos(jobs || [])));
  const tabs = tabBar(JOB_TABS, state.tab, (t) => { state.tab = t; draw(); });
  const count = el("span", { class: "muted small num" });
  const rows = el("div", { class: "table", role: "table", "aria-label": "Jobs" });
  app.replaceChildren(
    header({ title: "Jobs", lede, actions: [run] }),
    tabs.el,
    el("div", { class: "toolbar" }, searchBox("Filter by repository, branch or commit", (q) => { state.q = q; draw(); }), count),
    rows);
  rows.replaceChildren(jobsHeaderRow(), ...skeleton("jobs-grid"));

  function draw() {
    if (failure) {
      rows.replaceChildren(errorNote(`Couldn't load jobs: ${failure}`, refresh));
      return;
    }
    if (!jobs) return;
    const searched = jobs.filter((j) => matchesSearch(j, state.q));
    for (const t of JOB_TABS) tabs.count(t.id, searched.filter(t.match).length);
    const shown = searched.filter(JOB_TABS.find((t) => t.id === state.tab).match);
    count.textContent = `${shown.length} of the latest ${jobs.length} jobs`;
    lede.textContent = jobsFinding(jobs);
    if (jobs.length === 0) {
      rows.replaceChildren(note("No jobs yet. Point a webhook at /webhooks/github (or gitea, forgejo, gitlab, bitbucket), or run a build by hand."));
    } else if (shown.length === 0) {
      rows.replaceChildren(jobsHeaderRow(), note("No jobs match this view."));
    } else {
      rows.replaceChildren(jobsHeaderRow(), ...shown.map(jobRow));
    }
  }

  async function refresh() {
    try {
      jobs = await getJSON(`/api/jobs?limit=${LIST_LIMIT}`);
      failure = null;
    } catch (err) {
      if (!jobs) failure = err.message;
    }
    draw();
  }

  refresh();
  const timer = setInterval(refresh, 5000);
  return () => clearInterval(timer);
}

function knownRepos(jobs) {
  return [...new Set(jobs.map((j) => j.repo))].sort();
}

// --- job ----------------------------------------------------------------------

function renderJob(id) {
  setNav("jobs");
  document.title = `Job #${id} · cisid`;
  const stripANSI = (s) => s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "");

  let job = null;
  let selected = null;   // the step whose log is shown
  let following = true;  // follow the running step until a step is picked
  const logs = new Map(); // step -> <pre>

  const top = el("div", {}, header({ title: el("span", {}, "Job ", el("span", { class: "num" }, "#" + id)),
    lede: el("p", { class: "lede" }, "Loading job") }));
  const facts = el("dl", { class: "facts" });
  const steps = el("div", { class: "strip big", role: "group", "aria-label": "Steps" });
  const logTitle = el("h2", {}, "Log");
  const logMeta = el("span", { class: "muted small" });
  const logBody = el("div");
  const pane = el("section", { class: "logs", "aria-label": "Log" },
    el("div", { class: "logs-head" }, el("div", { class: "stack" }, logTitle, logMeta)), logBody);
  app.replaceChildren(top, facts, steps, pane);

  function pre(step) {
    let p = logs.get(step);
    if (!p) {
      p = el("pre", { tabindex: "0" });
      logs.set(step, p);
    }
    return p;
  }

  function showHeader(j) {
    // A job still going can be stopped; a finished one can run again.
    const actions = FINISHED.has(j.status)
      ? [el("button", { class: "btn primary", type: "button", onclick: (e) => act(e.currentTarget, `/api/jobs/${j.id}/rerun`, true) }, "Rerun")]
      : [el("button", { class: "btn", type: "button", onclick: (e) => act(e.currentTarget, `/api/jobs/${j.id}/cancel`) }, "Cancel")];
    top.replaceChildren(header({
      crumbs: [el("a", { href: "/" }, "Jobs"), el("span", { "aria-hidden": "true" }, "/"), el("a", { href: repoPath(j.repo) }, j.repo)],
      title: [el("span", {}, j.repo), el("span", { class: "num" }, "#" + j.id), pill(j.status)],
      lede: el("p", { class: "lede" }, finding(j),
        j.status === "failed" && j.error && j.failed_step !== undefined && j.failed_step !== null ? ` ${j.error}` : ""),
      actions,
    }));
  }

  function showFacts(j) {
    const copy = el("button", { class: "icon-btn", type: "button", title: "Copy the commit SHA", "aria-label": "Copy the commit SHA" }, icon("copy", 14));
    copy.addEventListener("click", async () => {
      await navigator.clipboard.writeText(j.commit_sha).catch(() => {});
      copy.replaceChildren(icon("check", 14));
      setTimeout(() => copy.replaceChildren(icon("copy", 14)), 1200);
    });
    const items = [
      ["Trigger", el("span", {}, trigger(j))],
      ["Commit", [el("span", { class: "num" }, shortSHA(j.commit_sha)), copy]],
      ["Agent", j.agent ? el("span", {}, j.agent) : dash()],
      ["Started", j.started_at ? el("span", { class: "num", title: j.started_at }, stamp(j.started_at)) : dash()],
      ["Duration", j.started_at ? el("span", { class: "num" }, fmtDuration(seconds(j))) : dash()],
    ];
    if (j.matrix) items.splice(1, 0, ["Matrix", el("span", {}, matrixLabel(j.matrix))]);
    facts.style.setProperty("--n", items.length);
    facts.replaceChildren(...items.map(([label, value]) => el("div", { class: "fact" }, el("dt", {}, label), el("dd", {}, value))));
  }

  // pickDefault chooses the step a reader most likely wants: the running
  // one, the one that failed, else the last.
  function pickDefault(j) {
    const n = (j.steps || []).length;
    if (n === 0) return null;
    if (j.status === "running") return j.current_step ?? 0;
    if (j.failed_step !== undefined && j.failed_step !== null) return j.failed_step;
    return n - 1;
  }

  function showSteps(j) {
    const states = stepStates(j);
    if (following || selected === null || selected >= states.length) {
      selected = pickDefault(j);
    }
    steps.replaceChildren(...states.map((s, i) => {
      const b = el("button", { class: "seg", type: "button", "data-state": s.state, "aria-pressed": String(i === selected) },
        el("span", { class: "seg-bar" }),
        el("span", { class: "seg-name" }, el("span", {}, s.name), el("span", { class: "seg-state" }, STATE_WORD[s.state])));
      b.addEventListener("click", () => {
        following = false;
        selected = i;
        showSteps(job);
      });
      return b;
    }));
    showLog();
  }

  function showLog() {
    if (!job) return;
    const names = job.steps || [];
    if (selected === null) {
      logTitle.textContent = "Log";
      logMeta.textContent = "";
      const text = job.logs_expired_at
        ? `Logs were deleted on ${stamp(job.logs_expired_at)} after the retention period.`
        : FINISHED.has(job.status) ? "No step ran, so there is no output." : "The log starts when an agent picks the job up.";
      logBody.replaceChildren(el("p", { class: "logs-note" }, text));
      return;
    }
    const state = stepStates(job)[selected];
    logTitle.textContent = names[selected] || `step ${selected}`;
    logMeta.textContent = state ? STATE_WORD[state.state] : "";
    const p = logs.get(selected);
    if (p && p.childNodes.length) {
      logBody.replaceChildren(p);
    } else if (job.logs_expired_at) {
      logBody.replaceChildren(el("p", { class: "logs-note" }, `Logs were deleted on ${stamp(job.logs_expired_at)} after the retention period.`));
    } else {
      const waiting = state && (state.state === "queued" || state.state === "running");
      logBody.replaceChildren(el("p", { class: "logs-note" }, waiting ? "No output yet." : "This step wrote no output."));
    }
  }

  function show(j) {
    job = j;
    showHeader(j);
    showFacts(j);
    showSteps(j);
  }

  async function act(button, url, openResult) {
    button.disabled = true;
    try {
      const result = await request("POST", url, { token: true });
      if (result && openResult) navigate("/jobs/" + result.id);
      else if (result) show({ ...job, ...result });
    } catch (err) {
      await openDialog({ title: "That did not work", text: err.message, submit: "Close" });
    } finally {
      button.disabled = false;
    }
  }

  let es = null;
  let stopped = false;

  // The stream sends the job, its logs, and the job again once it has
  // finished, then ends. Only that last status closes it: the first one of a
  // finished job comes before its logs.
  function openStream() {
    let statuses = 0;
    es = new EventSource(`/api/jobs/${id}/logs/stream`);
    es.addEventListener("status", (e) => {
      const j = JSON.parse(e.data);
      statuses++;
      show({ ...job, ...j });
      if (FINISHED.has(j.status) && statuses > 1) es.close();
    });
    es.addEventListener("log", (e) => {
      const l = JSON.parse(e.data);
      const p = pre(l.step);
      const follow = p.scrollTop + p.clientHeight >= p.scrollHeight - 4;
      const text = stripANSI(l.data);
      p.append(l.stream === "stderr" ? el("span", { class: "err" }, text) : document.createTextNode(text));
      if (job && job.status === "running" && (job.current_step ?? -1) < l.step) {
        job = { ...job, current_step: l.step };
        showSteps(job);
      } else if (l.step === selected && !logBody.contains(p)) {
        showLog();
      }
      if (follow) p.scrollTop = p.scrollHeight;
    });
  }

  // Load the job first: if reading needs a token, this signs the browser
  // in, and the stream, which cannot send one itself, then gets through.
  getJSON(`/api/jobs/${id}`)
    .then((j) => {
      if (stopped) return;
      show(j);
      openStream();
    })
    .catch((err) => {
      top.replaceChildren(header({ title: el("span", {}, "Job ", el("span", { class: "num" }, "#" + id)), lede: null }));
      facts.remove();
      steps.remove();
      pane.replaceWith(errorNote(`Couldn't load this job: ${err.message}`, () => navigate(location.pathname)));
    });

  const ticker = setInterval(() => {
    if (job && !FINISHED.has(job.status)) {
      showHeader(job);
      showFacts(job);
    }
  }, 1000);
  return () => {
    stopped = true;
    if (es) es.close();
    clearInterval(ticker);
  };
}

// --- repositories -------------------------------------------------------------

function reposHeaderRow() {
  return el("div", { class: "tr th repos-grid", role: "row" },
    el("span", { role: "columnheader" }, "Repository"),
    el("span", { role: "columnheader" }, "Last build"),
    el("span", { role: "columnheader" }, "Status"),
    el("span", { role: "columnheader", class: "right col-count" }, "Builds"),
    el("span", { role: "columnheader", class: "right col-time" }, "Last run"));
}

function summarize(jobs) {
  const repos = new Map();
  for (const j of jobs) { // newest first
    const r = repos.get(j.repo);
    if (r) r.count++;
    else repos.set(j.repo, { repo: j.repo, last: j, count: 1 });
  }
  return [...repos.values()];
}

function renderRepos() {
  setNav("repos");
  document.title = "Repositories · cisid";
  const lede = el("p", { class: "lede" }, "Loading repositories");
  const rows = el("div", { class: "table", role: "table", "aria-label": "Repositories" });
  app.replaceChildren(header({ title: "Repositories", lede }), rows);
  rows.replaceChildren(reposHeaderRow(), ...skeleton("repos-grid", 3));

  async function load() {
    let jobs;
    try {
      jobs = await getJSON(`/api/jobs?limit=${LIST_LIMIT}`);
    } catch (err) {
      rows.replaceChildren(errorNote(`Couldn't load repositories: ${err.message}`, load));
      return;
    }
    const repos = summarize(jobs);
    const failing = repos.filter((r) => r.last.status === "failed");
    lede.textContent = repos.length === 0
      ? "A repository shows up here after its first build."
      : `${repos.length} ${repos.length === 1 ? "repository" : "repositories"} in the latest ${jobs.length} jobs. ` +
        (failing.length ? `${failing.length} failing now: ${failing.map((r) => r.repo).join(", ")}.` : "None failing now.");
    if (repos.length === 0) {
      rows.replaceChildren(note("No repositories yet. Point a webhook at /webhooks/github (or gitea, forgejo, gitlab, bitbucket) to build the first one."));
      return;
    }
    rows.replaceChildren(reposHeaderRow(), ...repos.map((r) => el("a", { class: "tr repos-grid", href: repoPath(r.repo), role: "row" },
      el("div", { class: "ident", role: "cell" }, repoTile(r.repo),
        el("div", { class: "stack" },
          el("div", { class: "line1" }, el("span", { class: "name" }, r.repo)),
          el("div", { class: "line2" }, `#${r.last.id} · ${trigger(r.last)}`))),
      el("div", { role: "cell" }, strip(r.last)),
      el("div", { role: "cell" }, pill(r.last.status)),
      el("div", { role: "cell", class: "right num col-count" }, String(r.count)),
      el("div", { role: "cell", class: "right col-time muted" }, ago(r.last.created_at)))));
  }
  load();
  return () => {};
}

const REPO_TABS = [
  { id: "builds", label: "Builds" },
  { id: "schedules", label: "Schedules" },
  { id: "polling", label: "Polling" },
  { id: "secrets", label: "Secrets" },
];

function renderRepo(owner, name) {
  setNav("repos");
  const repo = `${owner}/${name}`;
  const base = `/api/repos/${repoURL(repo)}`;
  document.title = `${repo} · cisid`;
  let tab = "builds";
  let timer = null;

  const lede = el("p", { class: "lede" }, "Loading builds");
  const run = el("button", { class: "btn primary", type: "button", onclick: () => runBuild(repo) }, "Run build");
  const tabs = tabBar(REPO_TABS, tab, (t) => { tab = t; showTab(); });
  const body = el("div");
  app.replaceChildren(
    header({
      crumbs: [el("a", { href: "/repos" }, "Repositories"), el("span", { "aria-hidden": "true" }, "/"), el("span", {}, repo)],
      title: repo,
      lede,
      actions: [run],
    }),
    tabs.el,
    body);

  function showTab() {
    clearInterval(timer);
    timer = null;
    if (tab === "builds") builds();
    else if (tab === "schedules") schedules();
    else if (tab === "polling") polling();
    else secrets();
  }

  function builds() {
    const rows = el("div", { class: "table", role: "table", "aria-label": "Builds" });
    body.replaceChildren(rows);
    rows.replaceChildren(jobsHeaderRow(), ...skeleton("jobs-grid", 3));
    async function load() {
      try {
        const jobs = (await getJSON(`/api/jobs?limit=${LIST_LIMIT}`)).filter((j) => j.repo === repo);
        tabs.count("builds", jobs.length);
        lede.textContent = jobs.length ? `Last build #${jobs[0].id}: ${finding(jobs[0])}` : "No builds in the latest jobs.";
        rows.replaceChildren(...(jobs.length ? [jobsHeaderRow(), ...jobs.map(jobRow)] : [note("No builds of this repository in the latest jobs.")]));
      } catch (err) {
        rows.replaceChildren(errorNote(`Couldn't load builds: ${err.message}`, load));
      }
    }
    load();
    timer = setInterval(load, 5000);
  }

  function schedules() {
    const add = el("button", { class: "btn small", type: "button" }, "New schedule");
    const rows = el("div", { class: "table", role: "table", "aria-label": "Schedules" });
    body.replaceChildren(el("div", { class: "toolbar" },
      el("span", { class: "muted small" }, "Times are UTC unless the cron starts with CRON_TZ=."), add), rows);
    const head = () => el("div", { class: "tr th sched-grid", role: "row" },
      el("span", { role: "columnheader" }, "Name"), el("span", { role: "columnheader" }, "Cron"),
      el("span", { role: "columnheader" }, "Branch"), el("span", { role: "columnheader" }, "Next run"),
      el("span", { role: "columnheader" }, "Last build"), el("span", { role: "columnheader" }, ""));

    add.addEventListener("click", async () => {
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
          const saved = await request("PUT", `${base}/schedules/${encodeURIComponent(v.name.trim())}`,
            { body: { cron: v.cron.trim(), branch: v.branch.trim() }, token: true });
          if (!saved) throw new Error("Saving a schedule needs the API token.");
        },
      });
      if (done) load();
    });

    async function remove(s) {
      const ok = await openDialog({
        title: `Delete ${s.name}?`,
        text: `${repo} stops building ${s.branch || "its default branch"} at ${s.cron}. Builds it already started stay.`,
        submit: "Delete schedule",
        danger: true,
        onSubmit: async () => {
          const done = await request("DELETE", `${base}/schedules/${encodeURIComponent(s.name)}`, { token: true });
          if (!done) throw new Error("Deleting needs the API token.");
        },
      });
      if (ok) load();
    }

    async function load() {
      try {
        const list = await getJSON(`${base}/schedules`);
        tabs.count("schedules", list.length);
        if (list.length === 0) {
          rows.replaceChildren(note("No schedules. A schedule builds a branch on a timetable, such as every night."));
          return;
        }
        rows.replaceChildren(head(), ...list.map((s) => el("div", { class: "tr sched-grid", role: "row" },
          el("div", { role: "cell", class: "line1" }, el("span", { class: "name" }, s.name)),
          el("div", { role: "cell", class: "num" }, s.cron),
          el("div", { role: "cell" }, s.branch || el("span", { class: "muted" }, "default branch")),
          el("div", { role: "cell" }, el("div", { class: "num" }, stampUTC(s.next_run_at)), el("div", { class: "line2" }, inWords(s.next_run_at))),
          el("div", { role: "cell" }, s.last_job_id ? el("a", { class: "num", href: "/jobs/" + s.last_job_id }, "#" + s.last_job_id) : dash()),
          el("div", { role: "cell", class: "right" }, el("button", { class: "text-action danger", type: "button", onclick: () => remove(s) }, "Delete")))));
      } catch (err) {
        rows.replaceChildren(errorNote(`Couldn't load schedules: ${err.message}`, load));
      }
    }
    load();
  }

  // polling lists the branches the server polls for new commits, for a
  // server the forge's webhooks cannot reach.
  function polling() {
    const add = el("button", { class: "btn small", type: "button" }, "Poll a branch");
    const rows = el("div", { class: "table", role: "table", "aria-label": "Polled branches" });
    body.replaceChildren(el("div", { class: "toolbar" },
      el("span", { class: "muted small" }, "Each new commit on a polled branch is built as a push, without a webhook."), add), rows);
    const head = () => el("div", { class: "tr th watch-grid", role: "row" },
      el("span", { role: "columnheader" }, "Branch"), el("span", { role: "columnheader" }, "Last commit"),
      el("span", { role: "columnheader" }, "Last poll"), el("span", { role: "columnheader" }, "Last build"),
      el("span", { role: "columnheader" }, ""));

    add.addEventListener("click", async () => {
      const done = await openDialog({
        title: "Poll a branch",
        text: `Checks a branch of ${repo} for new commits and builds each one. The first poll builds the branch's current head.`,
        fields: [{ name: "branch", label: "Branch", hint: "Empty polls the default branch." }],
        submit: "Poll branch",
        onSubmit: async (v) => {
          const saved = await request("POST", `${base}/watches`, { body: { branch: v.branch.trim() }, token: true });
          if (!saved) throw new Error("Polling a branch needs the API token.");
        },
      });
      if (done) load();
    });

    async function remove(w) {
      const ok = await openDialog({
        title: `Stop polling ${w.branch}?`,
        text: `New commits on ${w.branch} are no longer built without a webhook. Builds already started stay.`,
        submit: "Stop polling",
        danger: true,
        onSubmit: async () => {
          const done = await request("DELETE", `${base}/watches/${w.branch.split("/").map(encodeURIComponent).join("/")}`, { token: true });
          if (!done) throw new Error("Stopping needs the API token.");
        },
      });
      if (ok) load();
    }

    async function load() {
      try {
        const list = await getJSON(`${base}/watches`);
        tabs.count("polling", list.length);
        if (list.length === 0) {
          rows.replaceChildren(note("No polled branches. Poll a branch when the forge's webhooks cannot reach this server."));
          return;
        }
        rows.replaceChildren(head(), ...list.map((w) => el("div", { class: "tr watch-grid", role: "row" },
          el("div", { role: "cell", class: "line1" }, el("span", { class: "name" }, w.branch)),
          el("div", { role: "cell", class: "num" }, w.last_sha ? w.last_sha.slice(0, 7) : el("span", { class: "muted" }, "not polled yet")),
          el("div", { role: "cell" }, el("div", { class: "num" }, w.last_poll_at ? ago(w.last_poll_at) : "-"),
            w.last_error ? el("div", { class: "line2 failing" }, w.last_error) : null),
          el("div", { role: "cell" }, w.last_job_id ? el("a", { class: "num", href: "/jobs/" + w.last_job_id }, "#" + w.last_job_id) : dash()),
          el("div", { role: "cell", class: "right" }, el("button", { class: "text-action danger", type: "button", onclick: () => remove(w) }, "Stop")))));
      } catch (err) {
        rows.replaceChildren(errorNote(`Couldn't load polled branches: ${err.message}`, load));
      }
    }
    load();
    timer = setInterval(load, 10000);
  }

  function secrets() {
    const add = el("button", { class: "btn small", type: "button" }, "Add secret");
    const rows = el("div", { class: "table", role: "table", "aria-label": "Secrets" });
    body.replaceChildren(el("div", { class: "toolbar" },
      el("span", { class: "muted small" }, "Values are never shown again. Steps get a secret only when they ask for it."), add), rows);
    const head = () => el("div", { class: "tr th secret-grid", role: "row" },
      el("span", { role: "columnheader" }, "Name"), el("span", { role: "columnheader" }, "Given to"),
      el("span", { role: "columnheader" }, "Updated"), el("span", { role: "columnheader" }, ""));

    add.addEventListener("click", async () => {
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
          const branches = v.branches.split(",").map((b) => b.trim()).filter(Boolean);
          const saved = await request("PUT", `${base}/secrets/${encodeURIComponent(v.name.trim())}`,
            { body: { value: v.value, branches }, token: true });
          if (!saved) throw new Error("Saving a secret needs the API token.");
        },
      });
      if (done) load();
    });

    async function remove(s) {
      const ok = await openDialog({
        title: `Delete ${s.name}?`,
        text: `Steps of ${repo} that ask for ${s.name} run without it from the next build on.`,
        submit: "Delete secret",
        danger: true,
        onSubmit: async () => {
          const done = await request("DELETE", `${base}/secrets/${encodeURIComponent(s.name)}`, { token: true });
          if (!done) throw new Error("Deleting needs the API token.");
        },
      });
      if (ok) load();
    }

    async function load() {
      try {
        const list = await request("GET", `${base}/secrets`, { token: true });
        if (!list) {
          rows.replaceChildren(note("Secrets need the API token.",
            el("button", { class: "text-action", type: "button", onclick: load }, "Enter token")));
          return;
        }
        tabs.count("secrets", list.length);
        if (list.length === 0) {
          rows.replaceChildren(note("No secrets. Add one for a deploy key or a registry token."));
          return;
        }
        rows.replaceChildren(head(), ...list.map((s) => el("div", { class: "tr secret-grid", role: "row" },
          el("div", { role: "cell", class: "num" }, s.name),
          el("div", { role: "cell" }, s.branches && s.branches.length
            ? el("span", {}, "pushes to ", s.branches.join(", "))
            : el("span", { class: "muted" }, "every build except pull requests from forks")),
          el("div", { role: "cell", class: "muted" }, ago(s.updated_at)),
          el("div", { role: "cell", class: "right" }, el("button", { class: "text-action danger", type: "button", onclick: () => remove(s) }, "Delete")))));
      } catch (err) {
        rows.replaceChildren(errorNote(`Couldn't load secrets: ${err.message}`, load));
      }
    }
    load();
  }

  showTab();
  return () => clearInterval(timer);
}

// --- theme --------------------------------------------------------------------

function currentTheme() {
  const set = document.documentElement.dataset.theme;
  if (set) return set;
  return matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  document.getElementById("theme").setAttribute("aria-label", theme === "dark" ? "Switch to light" : "Switch to dark");
}

try {
  const saved = localStorage.getItem(THEME_KEY);
  if (saved === "light" || saved === "dark") applyTheme(saved);
} catch {
  // Storage can be off; the system theme then applies.
}
document.getElementById("theme").setAttribute("aria-label", currentTheme() === "dark" ? "Switch to light" : "Switch to dark");
document.getElementById("theme").addEventListener("click", () => {
  const next = currentTheme() === "dark" ? "light" : "dark";
  applyTheme(next);
  try {
    localStorage.setItem(THEME_KEY, next);
  } catch {
    // Not remembered; it still applies now.
  }
});

// --- routing ------------------------------------------------------------------

function setNav(name) {
  for (const a of document.querySelectorAll("[data-nav]")) {
    if (a.dataset.nav === name) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
}

function route() {
  stopPage();
  const path = location.pathname;
  let m;
  if ((m = path.match(/^\/jobs\/(\d+)$/))) stopPage = renderJob(Number(m[1]));
  else if (path === "/repos") stopPage = renderRepos();
  else if ((m = path.match(/^\/repos\/([^/]+)\/([^/]+)$/))) stopPage = renderRepo(decodeURIComponent(m[1]), decodeURIComponent(m[2]));
  else stopPage = renderJobs();
}

function navigate(path) {
  history.pushState(null, "", path);
  route();
  window.scrollTo(0, 0);
}

document.addEventListener("click", (e) => {
  const a = e.target.closest("a");
  if (a && a.origin === location.origin && !a.target && !e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) {
    e.preventDefault();
    navigate(a.pathname);
  }
});
window.addEventListener("popstate", route);
route();
