// Landing introduces cisid to someone who has not signed in, and sends
// them on to sign in, sign up or, where reading is public, the builds.

import { useEffect, useRef, useState } from "react";
import { Link, Navigate } from "react-router";

import { getJSON } from "../lib/api";
import { ago, fmtDuration, seconds, shortSHA, trigger } from "../lib/format";
import { useMe } from "../lib/session";
import type { Job } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { Icon } from "../ui/Icon";
import { LogoMark, Wordmark } from "../ui/Logo";
import { useTheme } from "../ui/Shell";
import { Pill, Strip } from "../ui/parts";

// The pipeline cisid builds itself with, as it is in the repository.
const SELF_PIPELINE = `cache:
  - /go/pkg/mod
  - /root/.cache/go-build

services:
  - name: postgres
    image: postgres:18-alpine
    environment:
      POSTGRES_PASSWORD: cisid

steps:
  - name: test
    image: golang:1.27
    environment:
      CISID_TEST_DATABASE_URL: postgres://postgres:cisid@postgres:5432/postgres?sslmode=disable
    commands:
      - go vet ./...
      - go test -race ./...
    timeout: 15m

  - name: chart
    image: golang:1.27
    commands:
      - cd deployments/helm/charttest
      - go vet ./...
      - go test ./...
    timeout: 10m`;

// An example of a job in flight, drawn with the real strip, for when the
// server shows no builds of its own.
const EXAMPLE: Job = {
  id: 0, repo: "your-org/api", clone_url: "", branch: "main", commit_sha: "4f2a9c1e", status: "running",
  created_at: new Date().toISOString(), event: "push", steps: ["clone", "lint", "test", "chart", "artifacts"],
  step_results: [{ state: "success" }, { state: "success" }, { state: "running" }, { state: "running" }, { state: "pending" }],
};

const FLOW = [
  { step: "push", title: "A change arrives", text: "A push or pull request on GitHub, Gitea, Forgejo, GitLab or Bitbucket, a branch the server polls, a schedule, or Run build." },
  { step: "plan", title: "The server plans it", text: "It reads .cisid.yml at that very commit, picks the secrets the steps ask for, and queues one job per matrix combination." },
  { step: "run", title: "Your agents run it", text: "Each step runs in its own container on your Docker host, side by side when depends_on allows, next to the services it needs." },
  { step: "report", title: "Everyone hears", text: "The log streams line by line; the forge gets a commit status, your chat or inbox a message, the README its badge." },
];

const FEATURES = [
  {
    head: "Pipelines",
    items: ["Steps in any image, run in order or in parallel", "Services such as PostgreSQL on a private network",
      "Matrix builds, one job per combination", "Caches kept between jobs", "Artifacts to download", "Labels that send jobs to the right agent"],
  },
  {
    head: "Forges",
    items: ["GitHub, Gitea, Forgejo, GitLab and Bitbucket", "Webhooks, or polling for servers no webhook reaches",
      "Pull requests, with forks held for a maintainer", "Commit statuses on every result", "Private repositories cloned with the forge's token"],
  },
  {
    head: "People",
    items: ["Accounts with viewer, maintainer and admin roles", "Sign-in with a password or through the forge",
      "Personal API tokens", "Secrets encrypted, masked in logs, limited to branches"],
  },
  {
    head: "Running it",
    items: ["One Go binary and PostgreSQL", "Docker images and a Helm chart with Docker-in-Docker agents",
      "Prometheus metrics and OpenTelemetry traces", "Slack, webhook and email notifications", "MIT licensed"],
  },
];

const LOG_LINES: [string, string][] = [
  ["err", "+ go test -race ./..."],
  ["out", "ok   cisid/internal/agent     1.565s"],
  ["out", "ok   cisid/internal/api       2.311s"],
  ["out", "ok   cisid/internal/db       15.368s"],
  ["out", "ok   cisid/internal/hub       6.496s"],
  ["out", "ok   cisid/internal/notify    0.549s"],
];

const SECTIONS = [
  { href: "#how", label: "How it works" },
  { href: "#pipeline", label: "Pipeline" },
  { href: "#features", label: "Features" },
  { href: "https://github.com/sidumandr/cisid", label: "Source" },
];

// NavMenu holds the nav's links on screens too narrow to show them, in a
// dropdown on the same glass as the nav.
function NavMenu({ theme, toggle }: { theme: string; toggle: () => void }) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const press = (e: PointerEvent) => { if (!(e.target instanceof Node && box.current?.contains(e.target))) setOpen(false); };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        button.current?.focus();
      }
    };
    document.addEventListener("pointerdown", press);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("pointerdown", press);
      document.removeEventListener("keydown", key);
    };
  }, [open]);
  return (
    <div className="nav-menu" ref={box}>
      <button ref={button} className="btn nav-menu-button" type="button" aria-label="Menu" aria-expanded={open}
        aria-controls="nav-menu-list" onClick={() => setOpen(!open)}>
        <Icon name={open ? "close" : "jobs"} size={18} />
      </button>
      {open ? (
        <div id="nav-menu-list" className="nav-menu-list glass">
          {SECTIONS.map((s) => (
            <a key={s.href} href={s.href} rel={s.href.startsWith("http") ? "noreferrer" : undefined} onClick={() => setOpen(false)}>{s.label}</a>
          ))}
          <Link to="/login" onClick={() => setOpen(false)}>Sign in</Link>
          <button className="text-action" type="button" onClick={toggle}>{theme === "dark" ? "Light theme" : "Dark theme"}</button>
        </div>
      ) : null}
    </div>
  );
}

function SiteNav({ signupOpen }: { signupOpen: boolean }) {
  const [theme, toggle] = useTheme();
  // Glass once the page slides under the nav; clear over the hero.
  const [stuck, setStuck] = useState(false);
  useEffect(() => {
    const on = () => setStuck(window.scrollY > 4);
    on();
    window.addEventListener("scroll", on, { passive: true });
    return () => window.removeEventListener("scroll", on);
  }, []);
  return (
    <header className="site-nav" data-stuck={stuck || undefined}>
      <Link className="site-mark" to="/"><Wordmark height={26} /></Link>
      <nav aria-label="Site">
        {SECTIONS.map((s) => <a key={s.href} href={s.href} rel={s.href.startsWith("http") ? "noreferrer" : undefined}>{s.label}</a>)}
        <button className="text-action" type="button" onClick={toggle}>{theme === "dark" ? "Light" : "Dark"}</button>
        {/* On a phone the nav keeps one button: Create an account when sign-up is open, else Sign in. */}
        <Link className={signupOpen ? "btn nav-extra" : "btn"} to="/login">Sign in</Link>
        {signupOpen ? <Link className="btn primary" to="/register">Create an account</Link> : null}
        <NavMenu theme={theme} toggle={toggle} />
      </nav>
    </header>
  );
}

// LiveBuilds shows the latest builds on this server when reading is
// public, else an example of one in flight.
function LiveBuilds({ publicRead }: { publicRead: boolean }) {
  const [jobs, setJobs] = useState<Job[] | null>(null);
  useEffect(() => {
    if (!publicRead) return;
    let stop = false;
    const load = () => getJSON<Job[]>("/api/jobs?limit=4").then((l) => { if (!stop) setJobs(l); }).catch(() => {});
    void load();
    const t = setInterval(load, 5000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [publicRead]);

  const real = jobs && jobs.length > 0;
  const shown = real ? jobs : [EXAMPLE];
  return (
    <figure className="hero-screen" aria-label={real ? "The latest builds on this server" : "An example build"}>
      <figcaption>
        <span className={real ? "live-dot" : "example-dot"} aria-hidden="true" />
        {real ? "Latest builds on this server, live" : "An example build in flight"}
      </figcaption>
      <div className="hero-rows">
        {shown.map((j) => (
          <div key={j.id} className="hero-row">
            <div className="hero-row-top">
              <span className="name">{j.repo}</span>
              {j.id ? <span className="num muted">#{j.id}</span> : null}
              <Pill status={j.status} />
            </div>
            <Strip job={j} />
            <div className="line2">
              {trigger(j)} · <span className="num">{shortSHA(j.commit_sha)}</span>
              {j.started_at ? <> · <span className="num">{fmtDuration(seconds(j))}</span></> : null}
              {j.id ? <> · {ago(j.created_at)}</> : null}
            </div>
          </div>
        ))}
      </div>
      <pre className="hero-log" aria-hidden="true">
        {LOG_LINES.map(([stream, line], i) => <span key={i} className={stream === "err" ? "err" : undefined}>{line + "\n"}</span>)}
      </pre>
    </figure>
  );
}

export function Landing() {
  useTitle("");
  const me = useMe();
  if (me.user) return <Navigate to="/jobs" replace />;
  const signupOpen = me.accounts && (me.signup === "first" || me.signup === "open");
  const primary = signupOpen
    ? <Link className="btn primary big" to="/register">{me.signup === "first" ? "Set up this server" : "Create an account"}<Icon name="arrow" /></Link>
    : <Link className="btn primary big" to="/login">Sign in<Icon name="arrow" /></Link>;

  return (
    <div className="site">
      <SiteNav signupOpen={signupOpen} />
      <main>
        <section className="hero">
          <div className="hero-copy">
            <h1>Your builds, on your machines.</h1>
            <p className="hero-sub">
              cisid is a self-hosted CI/CD server. A push runs your pipeline in Docker on agents you own, and you
              watch every step, and every line of its log, as it happens.
            </p>
            <div className="hero-actions">
              {primary}
              {me.public_read ? <Link className="btn big" to="/jobs">Browse the builds</Link> : null}
              {signupOpen ? <Link className="text-action" to="/login">I have an account</Link> : null}
            </div>
            {me.signup === "first" ? (
              <p className="hero-note">This server has no accounts yet: the first one you create is its admin.</p>
            ) : null}
          </div>
          {/* The cisid mark, large, behind the glass the builds sit on. */}
          <div className="hero-visual">
            <span className="hero-logo" aria-hidden="true"><LogoMark size={640} /></span>
            <LiveBuilds publicRead={me.public_read} />
          </div>
        </section>

        <section className="flow" id="how" aria-labelledby="how-title">
          <div className="section-intro">
            <h2 id="how-title">From a push to a green strip</h2>
            <p>Every build is the same four moves, and the strip on each job shows you where it is.</p>
          </div>
          <ol className="flow-steps">
            {FLOW.map((f, i) => (
              <li key={f.step} data-state={i < 2 ? "passed" : i === 2 ? "running" : "queued"}>
                <span className="flow-bar" aria-hidden="true" />
                <span className="flow-step">{String(i + 1).padStart(2, "0")} {f.step}</span>
                <h3>{f.title}</h3>
                <p>{f.text}</p>
              </li>
            ))}
          </ol>
        </section>

        <section className="pipeline" id="pipeline" aria-labelledby="pipeline-title">
          <div className="pipeline-copy">
            <h2 id="pipeline-title">One file at the root of the repository</h2>
            <p>
              A <code>.cisid.yml</code> names the steps, their images and their commands. cisid reads it from the commit
              it builds, so every branch carries its own pipeline.
            </p>
            <ul className="pipeline-notes">
              <li><code>depends_on</code> runs steps side by side; the first failure stops the rest.</li>
              <li><code>services</code> start next to the steps, reachable by name.</li>
              <li><code>cache</code> keeps module and build caches between jobs.</li>
              <li><code>artifacts</code> keeps files once the steps pass, for download.</li>
            </ul>
          </div>
          <figure className="yaml">
            <figcaption><span className="file">.cisid.yml</span><span>the pipeline cisid builds itself with</span></figcaption>
            <pre><code>{SELF_PIPELINE}</code></pre>
          </figure>
        </section>

        <section className="features" id="features" aria-labelledby="features-title">
          <h2 id="features-title">What comes with it</h2>
          <div className="feature-grid">
            {FEATURES.map((f) => (
              <div key={f.head} className="feature-col">
                <h3>{f.head}</h3>
                <ul>{f.items.map((it) => <li key={it}>{it}</li>)}</ul>
              </div>
            ))}
          </div>
        </section>

        <section className="cta" aria-labelledby="cta-title">
          <span className="cta-logo" aria-hidden="true"><LogoMark size={560} /></span>
          <div className="cta-card">
            <div>
              <h2 id="cta-title">{signupOpen ? "Start building here." : "Sign in to build."}</h2>
              <p>
                {me.signup === "first"
                  ? "Create the first account. It can add your team, connect forges and run the first build."
                  : signupOpen
                    ? "Create an account, or sign in with the one you have."
                    : me.providers?.length
                      ? "Accounts on this server are made by its admins. Sign in with yours, or through your forge."
                      : "Accounts on this server are made by its admins. Sign in with yours."}
              </p>
            </div>
            <div className="hero-actions">
              {primary}
              {signupOpen ? <Link className="btn big" to="/login">Sign in</Link> : null}
            </div>
          </div>
        </section>
      </main>
      <footer className="site-foot">
        <span className="site-mark"><Wordmark height={22} /></span>
        <span className="muted">Self-hosted CI/CD, written in Go. MIT licensed.</span>
        <a href="https://github.com/sidumandr/cisid" rel="noreferrer">github.com/sidumandr/cisid</a>
      </footer>
    </div>
  );
}
