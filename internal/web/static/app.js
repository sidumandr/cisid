"use strict";

// cisid web UI: a job list and a job page with live logs. Everything from
// the API is inserted as text, never as HTML.

const FINISHED = new Set(["success", "failed", "canceled"]);
const TOKEN_KEY = "cisid.apiToken";
const app = document.getElementById("app");
let stopPage = () => {};

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of children) {
    if (c !== null && c !== undefined) node.append(c);
  }
  return node;
}

function badge(status) {
  return el("span", { class: "badge " + status }, status);
}

function matrixLabel(m) {
  return Object.keys(m || {}).sort().map((k) => `${k}=${m[k]}`).join(", ");
}

function shortSHA(sha) {
  return (sha || "").slice(0, 8);
}

function ago(iso) {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

function duration(job) {
  if (!job.started_at) return "";
  const end = job.finished_at ? new Date(job.finished_at) : new Date();
  let s = Math.max(0, Math.round((end - new Date(job.started_at)) / 1000));
  const m = Math.floor(s / 60);
  s %= 60;
  return m ? `${m}m ${s}s` : `${s}s`;
}

async function getJSON(url) {
  const resp = await fetch(url, { headers: { Accept: "application/json" } });
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw new Error(body.reason || resp.statusText);
  }
  return resp.json();
}

// --- job list ---------------------------------------------------------------

function renderList() {
  document.title = "cisid";
  const body = el("tbody");
  const table = el("table", {},
    el("thead", {}, el("tr", {},
      el("th", {}, "Job"), el("th", {}, "Status"), el("th", {}, "Repository"),
      el("th", {}, "Branch"), el("th", {}, "Commit"), el("th", {}, "Agent"),
      el("th", {}, "Duration"), el("th", {}, "Created"))),
    body);
  const card = el("div", { class: "card" }, table);
  app.replaceChildren(el("h1", {}, "Jobs"), card);

  async function refresh() {
    try {
      const jobs = await getJSON("/api/jobs?limit=50");
      if (jobs.length === 0) {
        card.replaceChildren(el("div", { class: "empty" }, "No jobs yet. Push to a repository with the webhook set up."));
        return;
      }
      if (!card.contains(table)) card.replaceChildren(table);
      body.replaceChildren(...jobs.map((j) => el("tr", {},
        el("td", {}, el("a", { href: "/jobs/" + j.id }, "#" + j.id)),
        el("td", {}, badge(j.status)),
        el("td", { class: "wrap" }, j.repo,
          j.matrix ? el("div", { class: "muted small" }, matrixLabel(j.matrix)) : null),
        el("td", { class: "wrap" }, j.branch),
        el("td", { class: "mono" }, shortSHA(j.commit_sha)),
        el("td", { class: "muted" }, j.agent || ""),
        el("td", { class: "muted" }, duration(j)),
        el("td", { class: "muted" }, ago(j.created_at)))));
    } catch (err) {
      card.replaceChildren(el("div", { class: "empty" }, "Could not load jobs: " + err.message));
    }
  }

  refresh();
  const timer = setInterval(refresh, 5000);
  return () => clearInterval(timer);
}

// --- job page ---------------------------------------------------------------

function apiToken(forget) {
  if (forget) sessionStorage.removeItem(TOKEN_KEY);
  let token = sessionStorage.getItem(TOKEN_KEY);
  if (!token) {
    token = prompt("API token (CISID_API_TOKEN):");
    if (token) sessionStorage.setItem(TOKEN_KEY, token);
  }
  return token;
}

async function post(url) {
  for (let attempt = 0; attempt < 2; attempt++) {
    const token = apiToken(attempt > 0);
    if (!token) return null;
    const resp = await fetch(url, { method: "POST", headers: { Authorization: "Bearer " + token } });
    if (resp.status === 401) continue;
    const body = await resp.json().catch(() => ({}));
    if (!resp.ok) throw new Error(body.reason || resp.statusText);
    return body;
  }
  throw new Error("wrong API token");
}

function renderJob(id) {
  document.title = `cisid · job #${id}`;
  const summary = el("div", { class: "card summary" }, el("div", { class: "notice" }, "Loading…"));
  const steps = el("div");
  app.replaceChildren(summary, steps);

  const sections = new Map(); // step index -> { pre, label }
  let job = null;

  function section(index) {
    let s = sections.get(index);
    if (!s) {
      const label = el("span");
      const pre = el("pre");
      const details = el("details", { class: "card step", open: "" }, el("summary", {}, label), pre);
      s = { pre, label };
      sections.set(index, s);
      // Keep sections in step order.
      const after = [...sections.keys()].filter((k) => k > index).sort((a, b) => a - b)[0];
      steps.insertBefore(details, after === undefined ? null : sections.get(after).pre.parentElement);
      labelSection(index);
    }
    return s;
  }

  function labelSection(index) {
    const s = sections.get(index);
    if (!s) return;
    const name = (job && job.steps && job.steps[index]) || `step ${index}`;
    const parts = [name];
    if (job && job.failed_step === index && FINISHED.has(job.status)) {
      parts.push(" ", badge(job.status === "canceled" ? "canceled" : "failed"));
    }
    s.label.replaceChildren(...parts);
  }

  function showJob(j) {
    job = j;
    const actions = el("div", { class: "actions" });
    if (!FINISHED.has(j.status)) {
      actions.append(el("button", { onclick: (e) => act(e.target, `/api/jobs/${j.id}/cancel`) }, "Cancel"));
    }
    actions.append(el("button", { onclick: (e) => act(e.target, `/api/jobs/${j.id}/rerun`, true) }, "Rerun"));

    const meta = el("div", { class: "meta" },
      j.matrix ? el("span", {}, matrixLabel(j.matrix)) : null,
      el("span", { class: "mono" }, j.commit_sha),
      j.agent ? el("span", {}, "agent " + j.agent) : null,
      j.started_at ? el("span", {}, "duration " + duration(j)) : null,
      el("span", {}, "created " + ago(j.created_at)));

    // replaceChildren would print a null child as "null", so drop it.
    summary.replaceChildren(...[
      el("div", { class: "title" }, `#${j.id}`, badge(j.status), `${j.repo} @ ${j.branch}`, actions),
      meta,
      j.error ? el("div", { class: "error" }, j.error) : null,
    ].filter(Boolean));
    for (const index of sections.keys()) labelSection(index);
  }

  async function act(button, url, openResult) {
    button.disabled = true;
    try {
      const result = await post(url);
      if (result && openResult) navigate("/jobs/" + result.id);
      else if (result) showJob(result);
    } catch (err) {
      alert(err.message);
    } finally {
      button.disabled = false;
    }
  }

  const stripANSI = (s) => s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "");
  const es = new EventSource(`/api/jobs/${id}/logs/stream`);
  es.addEventListener("status", (e) => {
    const j = JSON.parse(e.data);
    showJob(j);
    if (FINISHED.has(j.status)) {
      es.close();
      if (sections.size === 0) {
        const note = j.logs_expired_at
          ? `Logs were deleted on ${new Date(j.logs_expired_at).toLocaleDateString()} after the retention period.`
          : "No output.";
        steps.replaceChildren(el("div", { class: "card notice" }, note));
      }
    }
  });
  es.addEventListener("log", (e) => {
    const l = JSON.parse(e.data);
    const { pre } = section(l.step);
    const follow = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 4;
    const text = stripANSI(l.data);
    pre.append(l.stream === "stderr" ? el("span", { class: "err" }, text) : document.createTextNode(text));
    if (follow) pre.scrollTop = pre.scrollHeight;
  });
  es.onerror = () => {
    if (!job) {
      es.close();
      getJSON(`/api/jobs/${id}`).catch((err) =>
        summary.replaceChildren(el("div", { class: "notice" }, "Could not load job: " + err.message)));
    }
  };

  const ticker = setInterval(() => { if (job && !FINISHED.has(job.status)) showJob(job); }, 1000);
  return () => { es.close(); clearInterval(ticker); };
}

// --- routing ----------------------------------------------------------------

function route() {
  stopPage();
  const m = location.pathname.match(/^\/jobs\/(\d+)$/);
  stopPage = m ? renderJob(Number(m[1])) : renderList();
}

function navigate(path) {
  history.pushState(null, "", path);
  route();
}

document.addEventListener("click", (e) => {
  const a = e.target.closest("a");
  if (a && a.origin === location.origin && !e.metaKey && !e.ctrlKey && !e.shiftKey) {
    e.preventDefault();
    navigate(a.pathname);
  }
});
window.addEventListener("popstate", route);
route();
