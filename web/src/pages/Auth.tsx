// Login and Register: every way into cisid, on a page of its own.

import { useState, type FormEvent, type ReactNode } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";

import { login, register } from "../lib/api";
import { forgeName } from "../lib/format";
import { loadMe, useMe } from "../lib/session";
import { useTitle } from "../lib/useTitle";
import { Mark } from "../ui/Icon";
import { ProviderLink, Strip } from "../ui/parts";
import type { Job } from "../lib/types";

// SIGNIN_PROBLEMS explain why signing in through a forge or Google came
// back here.
const SIGNIN_PROBLEMS: Record<string, string> = {
  expired: "That sign-in took too long or started in another tab. Try again.",
  denied: "The sign-in was turned down on the other side, so cisid did not get in.",
  failed: "That sign-in did not work. Try again.",
  unknown: "That account has no cisid account. An admin can link it to yours on the Users page.",
};

// orWords joins names as choices: "a", "a or b", "a, b or c".
function orWords(names: string[]): string {
  return names.length < 2 ? names.join("") : `${names.slice(0, -1).join(", ")} or ${names[names.length - 1]}`;
}

// safeNext keeps the return address on this site.
function safeNext(next: string | null): string {
  if (!next || !next.startsWith("/") || next.startsWith("//") || next.includes("\\")) return "/jobs";
  return next;
}

const PASSED: Job = {
  id: 0, repo: "", clone_url: "", branch: "", commit_sha: "", status: "success", created_at: "", event: "push",
  steps: ["clone", "test", "deploy"], step_results: [{ state: "success" }, { state: "success" }, { state: "success" }],
};

// Aside is what the side of the page says: a short statement, set large,
// and a sentence under it.
type Aside = { statement: string; text: string };

function AuthFrame({ title, lede, children, aside }: { title: string; lede: ReactNode; children: ReactNode; aside: Aside }) {
  return (
    <div className="auth">
      <aside className="auth-side">
        <Link className="site-mark" to="/"><Mark size={22} /><span>cisid</span></Link>
        <div className="auth-side-copy">
          <p className="auth-statement">{aside.statement}</p>
          <p className="auth-text">{aside.text}</p>
          <div className="auth-strip" aria-hidden="true"><Strip job={PASSED} /></div>
        </div>
        <span className="auth-tagline">Self-hosted CI/CD. MIT licensed.</span>
      </aside>
      <main className="auth-main">
        <div className="auth-card">
          <h1>{title}</h1>
          <p className="lede">{lede}</p>
          {children}
        </div>
      </main>
    </div>
  );
}

function Providers({ verb, next }: { verb: string; next: string }) {
  const me = useMe();
  if (!me.providers?.length) return null;
  return (
    <>
      <div className="providers">
        {me.providers.map((p) => <ProviderLink key={p} provider={p} verb={verb} next={next} />)}
      </div>
      <div className="or" role="separator"><span>or</span></div>
    </>
  );
}

export function Login() {
  useTitle("Sign in");
  const me = useMe();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const next = safeNext(params.get("next"));
  const problem = SIGNIN_PROBLEMS[params.get("signin") ?? ""];
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [token, setToken] = useState("");
  const [useToken, setUseToken] = useState(!me.accounts);
  const [error, setError] = useState(problem ?? "");
  const [busy, setBusy] = useState(false);

  if (me.user) return <Navigate to={next} replace />;
  const signupOpen = me.accounts && (me.signup === "first" || me.signup === "open");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (useToken ? !token.trim() : !username.trim() || !password) {
      setError(useToken ? "Write a token." : "Write your username and password.");
      return;
    }
    setBusy(true);
    try {
      await login(useToken ? { token: token.trim() } : { username: username.trim(), password });
      await loadMe();
      navigate(next, { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  }

  return (
    <AuthFrame
      title="Sign in"
      lede={!me.accounts ? "This server has no accounts: sign in with one of its tokens."
        : me.providers?.length ? `With your cisid account, your ${orWords(me.providers.map(forgeName))} account, or a token.` : "With your cisid account, or a token."}
      aside={{ statement: "Back to your builds.", text: "Watch them as they run, and run them again when they break." }}
    >
      {me.accounts ? <Providers verb="Continue with" next={next} /> : null}
      <form className="auth-form" noValidate onSubmit={submit}>
        {useToken ? (
          <div className="field">
            <label htmlFor="token">Token</label>
            <input id="token" type="password" autoComplete="off" spellCheck={false} value={token} autoFocus
              onChange={(e) => setToken(e.target.value)} />
            <span className="hint">A personal token (cisid_...), CISID_READ_TOKEN or CISID_API_TOKEN. A server token only reads; changes ask for it again.</span>
          </div>
        ) : (
          <>
            <div className="field">
              <label htmlFor="username">Username</label>
              <input id="username" type="text" autoComplete="username" spellCheck={false} value={username} autoFocus
                onChange={(e) => setUsername(e.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="password">Password</label>
              <input id="password" type="password" autoComplete="current-password" value={password}
                onChange={(e) => setPassword(e.target.value)} />
            </div>
          </>
        )}
        <p className="form-error" role="alert">{error}</p>
        <button className="btn primary big wide" type="submit" disabled={busy}>Sign in</button>
      </form>
      <div className="auth-links">
        {me.accounts ? (
          <button className="text-action" type="button" onClick={() => { setUseToken(!useToken); setError(""); }}>
            {useToken ? "Use a username and password" : "Use a token instead"}
          </button>
        ) : null}
        {signupOpen ? <span>No account? <Link to="/register">Create one</Link></span>
          : me.accounts ? <span className="muted">No account? An admin of this server makes them.</span> : null}
      </div>
    </AuthFrame>
  );
}

export function Register() {
  const me = useMe();
  useTitle(me.signup === "first" ? "Set up cisid" : "Create an account");
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  if (me.user) return <Navigate to="/jobs" replace />;
  const first = me.signup === "first";
  const open = me.accounts && (first || me.signup === "open");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (!username.trim()) return setError("Pick a username.");
    if (password.length < 10) return setError("The password needs at least 10 characters.");
    if (password !== repeat) return setError("The passwords do not match.");
    setBusy(true);
    try {
      await register({ username: username.trim(), password });
      await loadMe();
      navigate(first ? "/users" : "/jobs", { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  }

  const aside: Aside = first
    ? { statement: "A new server.", text: "Its first account runs it: it adds the team, connects the forges and starts the first build." }
    : { statement: "One account, every build.", text: "Read the logs, run the pipelines, keep the secrets." };

  if (!open) {
    return (
      <AuthFrame title="Sign-up is closed" lede="Accounts on this server are made by its admins." aside={aside}>
        {me.provider_signup ? (
          <>
            <p className="muted">Your account there gets a cisid account the first time you sign in with it.</p>
            <Providers verb="Sign up with" next="/jobs" />
          </>
        ) : null}
        <p>Ask an admin for an account, then <Link to="/login">sign in</Link>.</p>
      </AuthFrame>
    );
  }

  return (
    <AuthFrame
      title={first ? "Set up cisid" : "Create an account"}
      lede={first ? "This server has no accounts yet. The one you create now is its admin." : "Pick a username and a password."}
      aside={aside}
    >
      {!first && me.provider_signup ? <Providers verb="Sign up with" next="/jobs" /> : null}
      <form className="auth-form" noValidate onSubmit={submit}>
        <div className="field">
          <label htmlFor="new-username">Username</label>
          <input id="new-username" type="text" autoComplete="username" spellCheck={false} value={username} autoFocus
            onChange={(e) => setUsername(e.target.value)} />
          <span className="hint">Lowercase letters, digits, dots, dashes and underscores, up to 39.</span>
        </div>
        <div className="field">
          <label htmlFor="new-password">Password</label>
          <input id="new-password" type="password" autoComplete="new-password" value={password}
            onChange={(e) => setPassword(e.target.value)} />
          <span className="hint">At least 10 characters.</span>
        </div>
        <div className="field">
          <label htmlFor="repeat-password">Password again</label>
          <input id="repeat-password" type="password" autoComplete="new-password" value={repeat}
            onChange={(e) => setRepeat(e.target.value)} />
        </div>
        <p className="form-error" role="alert">{error}</p>
        <button className="btn primary big wide" type="submit" disabled={busy}>{first ? "Create the admin account" : "Create account"}</button>
      </form>
      <div className="auth-links">
        <span>Already have one? <Link to="/login">Sign in</Link></span>
      </div>
    </AuthFrame>
  );
}
