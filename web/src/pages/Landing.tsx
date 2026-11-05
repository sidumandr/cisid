// Landing introduces cisid to someone who has not signed in, and sends
// them on to sign in, sign up or, where reading is public, the builds.

import { useEffect, useRef, useState } from "react";
import { Link, Navigate } from "react-router";

import { getJSON } from "../lib/api";
import { ago, fmtDuration, seconds, shortSHA, trigger } from "../lib/format";
import { N_, t } from "../lib/i18n";
import { useMe } from "../lib/session";
import type { Job } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { Icon } from "../ui/Icon";
import { LangSwitch } from "../ui/LangSwitch";
import { LogoMark, Wordmark } from "../ui/Logo";
import { useGaze } from "../ui/useGaze";
import { useTheme } from "../ui/Shell";
import { ExampleRun } from "../ui/ExampleRun";
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

const FLOW = [
  { step: N_("push"), title: N_("A change arrives"), text: N_("A push or pull request on GitHub, Gitea, Forgejo, GitLab or Bitbucket, a branch the server polls, a schedule, or Run build.") },
  { step: N_("plan"), title: N_("The server plans it"), text: N_("It reads .cisid.yml at that very commit, picks the secrets the steps ask for, and queues one job per matrix combination.") },
  { step: N_("run"), title: N_("Your agents run it"), text: N_("Each step runs in its own container on your Docker host, side by side when depends_on allows, next to the services it needs.") },
  { step: N_("report"), title: N_("Everyone hears"), text: N_("The log streams line by line; the forge gets a commit status, your chat or inbox a message, the README its badge.") },
];

const FEATURES = [
  {
    head: N_("Pipelines"),
    items: [N_("Steps in any image, run in order or in parallel"), N_("Services such as PostgreSQL on a private network"),
      N_("Matrix builds, one job per combination"), N_("Caches kept between jobs"), N_("Artifacts to download"), N_("Labels that send jobs to the right agent")],
  },
  {
    head: N_("Forges"),
    items: [N_("GitHub, Gitea, Forgejo, GitLab and Bitbucket"), N_("Webhooks, or polling for servers no webhook reaches"),
      N_("Pull requests, with forks held for a maintainer"), N_("Commit statuses on every result"), N_("Private repositories cloned with the forge's token")],
  },
  {
    head: N_("People"),
    items: [N_("Accounts with viewer, maintainer and admin roles"), N_("Sign-in with a password or through the forge"),
      N_("Personal API tokens"), N_("Secrets encrypted, masked in logs, limited to branches")],
  },
  {
    head: N_("Running it"),
    items: [N_("One Go binary and PostgreSQL"), N_("Docker images and a Helm chart with Docker-in-Docker agents"),
      N_("Prometheus metrics and OpenTelemetry traces"), N_("Slack, webhook and email notifications"), N_("MIT licensed")],
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
  { href: "#how", label: N_("How it works") },
  { href: "#pipeline", label: N_("Pipeline") },
  { href: "#features", label: N_("Features") },
];
const SOURCE = "https://github.com/sidumandr/cisid";

// useSection names the section in view, so the nav can light its link.
function useSection(ids: string[]): string | null {
  const [active, setActive] = useState<string | null>(null);
  useEffect(() => {
    const seen = new Map<string, boolean>();
    const io = new IntersectionObserver((entries) => {
      for (const e of entries) seen.set(e.target.id, e.isIntersecting);
      setActive(ids.find((id) => seen.get(id)) ?? null);
    }, { rootMargin: "-40% 0px -55% 0px" });
    for (const id of ids) {
      const el = document.getElementById(id);
      if (el) io.observe(el);
    }
    return () => io.disconnect();
  }, [ids]);
  return active;
}

function ThemeButton({ theme, toggle, className = "" }: { theme: string; toggle: () => void; className?: string }) {
  const label = theme === "dark" ? t("Switch to light") : t("Switch to dark");
  return (
    <button className={"icon-toggle " + className} type="button" onClick={toggle} title={label} aria-label={label}>
      <Icon name={theme === "dark" ? "sun" : "moon"} size={18} />
    </button>
  );
}

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
      <button ref={button} className="icon-toggle nav-menu-button" type="button" aria-label={t("Menu")} aria-expanded={open}
        aria-controls="nav-menu-list" onClick={() => setOpen(!open)}>
        <Icon name={open ? "close" : "jobs"} size={18} />
      </button>
      {open ? (
        <div id="nav-menu-list" className="nav-menu-list glass">
          {SECTIONS.map((s) => <a key={s.href} href={s.href} onClick={() => setOpen(false)}>{t(s.label)}</a>)}
          <a href={SOURCE} rel="noreferrer" onClick={() => setOpen(false)}>{t("Source")}</a>
          <Link to="/login" onClick={() => setOpen(false)}>{t("Sign in")}</Link>
          <div className="nav-menu-tools">
            <LangSwitch />
            <ThemeButton theme={theme} toggle={toggle} />
          </div>
        </div>
      ) : null}
    </div>
  );
}

const SECTION_IDS = ["how", "pipeline", "features"];

// SiteNav floats over the page as a capsule of glass: the mark, the
// sections in a track with the one in view lit, and the ways in.
function SiteNav({ signupOpen }: { signupOpen: boolean }) {
  const [theme, toggle] = useTheme();
  const active = useSection(SECTION_IDS);
  const [stuck, setStuck] = useState(false);
  useEffect(() => {
    const on = () => setStuck(window.scrollY > 4);
    on();
    window.addEventListener("scroll", on, { passive: true });
    return () => window.removeEventListener("scroll", on);
  }, []);
  return (
    <header className="site-nav" data-stuck={stuck || undefined}>
      <div className="site-nav-bar">
        <Link className="site-mark" to="/"><Wordmark height={24} /></Link>
        <nav className="site-links" aria-label={t("Site")}>
          {SECTIONS.map((s) => (
            <a key={s.href} href={s.href} aria-current={active === s.href.slice(1) ? "true" : undefined}>{t(s.label)}</a>
          ))}
          <a href={SOURCE} rel="noreferrer">{t("Source")}</a>
        </nav>
        <div className="site-tools">
          <LangSwitch className="wide-tool" />
          <ThemeButton theme={theme} toggle={toggle} className="wide-tool" />
          {/* On a phone the nav keeps one button: Create an account when sign-up is open, else Sign in. */}
          <Link className={signupOpen ? "btn round nav-extra" : "btn round"} to="/login">{t("Sign in")}</Link>
          {signupOpen ? <Link className="btn round primary" to="/register">{t("Create an account")}</Link> : null}
          <NavMenu theme={theme} toggle={toggle} />
        </div>
      </div>
    </header>
  );
}

// LiveBuilds shows the latest builds on this server when reading is
// public and there are some, else the example playing.
function LiveBuilds({ publicRead }: { publicRead: boolean }) {
  const [jobs, setJobs] = useState<Job[] | null>(null);
  useEffect(() => {
    if (!publicRead) return;
    let stop = false;
    const load = () => getJSON<Job[]>("/api/jobs?limit=4").then((l) => { if (!stop) setJobs(l); }).catch(() => {});
    void load();
    const timer = setInterval(load, 5000);
    return () => {
      stop = true;
      clearInterval(timer);
    };
  }, [publicRead]);

  if (!jobs || jobs.length === 0) return <ExampleRun />;
  return (
    <figure className="hero-screen" aria-label={t("The latest builds on this server")}>
      <figcaption>
        <span className="live-dot" aria-hidden="true" />
        {t("Latest builds on this server, live")}
      </figcaption>
      <div className="hero-rows">
        {jobs.map((j) => (
          <div key={j.id} className="hero-row">
            <div className="hero-row-top">
              <span className="name">{j.repo}</span>
              <span className="num muted">#{j.id}</span>
              <Pill status={j.status} />
            </div>
            <Strip job={j} />
            <div className="line2">
              {trigger(j)} · <span className="num">{shortSHA(j.commit_sha)}</span>
              {j.started_at ? <> · <span className="num">{fmtDuration(seconds(j))}</span></> : null}
              {" · "}{ago(j.created_at)}
            </div>
          </div>
        ))}
      </div>
      <pre className="hero-log" aria-hidden="true">
        {LOG_LINES.map(([stream, line], i) => <span key={i} className={stream === "err" ? "log-line err" : "log-line"}>{line}</span>)}
      </pre>
    </figure>
  );
}

export function Landing() {
  useTitle("");
  const me = useMe();
  const heroOwl = useRef<HTMLSpanElement>(null);
  const ctaOwl = useRef<HTMLSpanElement>(null);
  useGaze(heroOwl);
  useGaze(ctaOwl);
  if (me.user) return <Navigate to="/jobs" replace />;
  const signupOpen = me.accounts && (me.signup === "first" || me.signup === "open");
  const primary = signupOpen
    ? <Link className="btn primary big" to="/register">{me.signup === "first" ? t("Set up this server") : t("Create an account")}<Icon name="arrow" /></Link>
    : <Link className="btn primary big" to="/login">{t("Sign in")}<Icon name="arrow" /></Link>;

  return (
    <div className="site">
      <SiteNav signupOpen={signupOpen} />
      <main>
        <section className="hero">
          <div className="hero-copy">
            <h1>{t("Your builds, on your machines.")}</h1>
            <p className="hero-sub">
              {t("cisid is a self-hosted CI/CD server. A push runs your pipeline in Docker on agents you own, and you watch every step, and every line of its log, as it happens.")}
            </p>
            <div className="hero-actions">
              {primary}
              {me.public_read ? <Link className="btn big" to="/jobs">{t("Browse the builds")}</Link> : null}
              {signupOpen ? <Link className="text-action" to="/login">{t("I have an account")}</Link> : null}
            </div>
            {me.signup === "first" ? (
              <p className="hero-note">{t("This server has no accounts yet: the first one you create is its admin.")}</p>
            ) : null}
          </div>
          {/* The cisid mark, large, behind the glass the builds sit on. */}
          <div className="hero-visual">
            <span className="hero-logo owl" ref={heroOwl} aria-hidden="true"><LogoMark size={640} /></span>
            <LiveBuilds publicRead={me.public_read} />
          </div>
        </section>

        <section className="flow" id="how" aria-labelledby="how-title">
          <div className="section-intro">
            <h2 id="how-title">{t("From a push to a green strip")}</h2>
            <p>{t("Every build is the same four moves, and the strip on each job shows you where it is.")}</p>
          </div>
          <ol className="flow-steps">
            {FLOW.map((f, i) => (
              <li key={f.step} data-state={i < 2 ? "passed" : i === 2 ? "running" : "queued"}>
                <span className="flow-bar" aria-hidden="true" />
                <span className="flow-step">{String(i + 1).padStart(2, "0")} {t(f.step)}</span>
                <h3>{t(f.title)}</h3>
                <p>{t(f.text)}</p>
              </li>
            ))}
          </ol>
        </section>

        <section className="pipeline" id="pipeline" aria-labelledby="pipeline-title">
          <div className="pipeline-copy">
            <h2 id="pipeline-title">{t("One file at the root of the repository")}</h2>
            <p>
              <code>.cisid.yml</code> {t("names the steps, their images and their commands. cisid reads it from the commit it builds, so every branch carries its own pipeline.")}
            </p>
            <ul className="pipeline-notes">
              <li><code>depends_on</code> {t("runs steps side by side; the first failure stops the rest.")}</li>
              <li><code>services</code> {t("start next to the steps, reachable by name.")}</li>
              <li><code>cache</code> {t("keeps module and build caches between jobs.")}</li>
              <li><code>artifacts</code> {t("keeps files once the steps pass, for download.")}</li>
            </ul>
          </div>
          <figure className="yaml">
            <figcaption><span className="file">.cisid.yml</span><span>{t("the pipeline cisid builds itself with")}</span></figcaption>
            <pre><code>{SELF_PIPELINE}</code></pre>
          </figure>
        </section>

        <section className="features" id="features" aria-labelledby="features-title">
          <h2 id="features-title">{t("What comes with it")}</h2>
          <div className="feature-grid">
            {FEATURES.map((f) => (
              <div key={f.head} className="feature-col">
                <h3>{t(f.head)}</h3>
                <ul>{f.items.map((it) => <li key={it}>{t(it)}</li>)}</ul>
              </div>
            ))}
          </div>
        </section>

        <section className="cta" aria-labelledby="cta-title">
          <span className="cta-logo owl" ref={ctaOwl} aria-hidden="true"><LogoMark size={560} /></span>
          <div className="cta-card">
            <div>
              <h2 id="cta-title">{signupOpen ? t("Start building here.") : t("Sign in to build.")}</h2>
              <p>
                {me.signup === "first"
                  ? t("Create the first account. It can add your team, connect forges and run the first build.")
                  : signupOpen
                    ? t("Create an account, or sign in with the one you have.")
                    : me.providers?.length
                      ? t("Accounts on this server are made by its admins. Sign in with yours, or through your forge.")
                      : t("Accounts on this server are made by its admins. Sign in with yours.")}
              </p>
            </div>
            <div className="hero-actions">
              {primary}
              {signupOpen ? <Link className="btn big" to="/login">{t("Sign in")}</Link> : null}
            </div>
          </div>
        </section>
      </main>
      <footer className="site-foot">
        <span className="site-mark"><Wordmark height={22} /></span>
        <span className="muted">{t("Self-hosted CI/CD, written in Go. MIT licensed.")}</span>
        <a href="https://github.com/sidumandr/cisid" rel="noreferrer">github.com/sidumandr/cisid</a>
      </footer>
    </div>
  );
}
