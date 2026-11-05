// The pieces every screen is built from.

import { useState, type ReactNode } from "react";

import { FINISHED, forgeName, stateWord, statusWord, stepStates } from "../lib/format";
import { t } from "../lib/i18n";
import type { Job, JobStatus } from "../lib/types";
import { Icon, ProviderMark } from "./Icon";

export function Header({ crumbs, title, lede, actions }: {
  crumbs?: ReactNode;
  title: ReactNode;
  lede?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="head">
      <div className="head-main">
        {crumbs ? <nav className="crumbs" aria-label={t("Breadcrumb")}>{crumbs}</nav> : null}
        <h1>{title}</h1>
        {typeof lede === "string" ? <p className="lede">{lede}</p> : lede}
      </div>
      {actions ? <div className="actions">{actions}</div> : null}
    </div>
  );
}

export interface Tab<T extends string> {
  id: T;
  label: string;
}

// TabBar draws underline tabs; the selected one carries the orange line.
// Labels are English, marked with N_, and shown in the language chosen.
export function TabBar<T extends string>({ tabs, selected, onSelect, counts = {} }: {
  tabs: readonly Tab<T>[];
  selected: T;
  onSelect: (id: T) => void;
  counts?: Partial<Record<T, number | null>>;
}) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((tab) => (
        <button key={tab.id} className="tab" type="button" role="tab" aria-selected={tab.id === selected} onClick={() => onSelect(tab.id)}>
          {t(tab.label)}
          <span className="num">{counts[tab.id] ?? ""}</span>
        </button>
      ))}
    </div>
  );
}

export function SearchBox({ placeholder, onChange }: { placeholder: string; onChange: (q: string) => void }) {
  return (
    <label className="search">
      <Icon name="search" />
      <input type="search" placeholder={placeholder} aria-label={placeholder} autoComplete="off" spellCheck={false}
        onChange={(e) => onChange(e.target.value.trim().toLowerCase())} />
    </label>
  );
}

export function Skeleton({ grid, rows = 5 }: { grid: string; rows?: number }) {
  return Array.from({ length: rows }, (_, i) => (
    <div key={i} className={"tr " + grid} aria-hidden="true">
      <div className="ident">
        <span className="tile" />
        <div className="stack"><span className="sk" data-w="1" /><span className="sk" data-w="2" /></div>
      </div>
      <span className="sk" /><span className="sk" /><span className="sk col-agent" /><span className="sk col-time" />
    </div>
  ));
}

export function Note({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return <div className="note"><span>{children}</span>{action}</div>;
}

export function ErrorNote({ text, onRetry }: { text: string; onRetry?: () => void }) {
  return (
    <div className="note error" role="alert">
      <span><span className="sign">{t("Error: ")}</span>{text}</span>
      {onRetry ? <button className="text-action" type="button" onClick={onRetry}>{t("Retry")}</button> : null}
    </div>
  );
}

export function Pill({ status, children }: { status: JobStatus | "canceled"; children?: ReactNode }) {
  return <span className="pill" data-status={status}>{children ?? (status === "canceled" ? t("Canceled") : statusWord(status))}</span>;
}

export const Dash = () => <span className="dash">-</span>;

export function RepoTile({ repo }: { repo: string }) {
  const name = repo.split("/").pop() || "?";
  return <span className="tile" aria-hidden="true">{name.slice(0, 1).toLowerCase()}</span>;
}

export function Fact({ name, children }: { name: string; children: ReactNode }) {
  return <div className="fact"><dt>{name}</dt><dd>{children}</dd></div>;
}

// Strip draws a job's pipeline as one segment per step.
export function Strip({ job }: { job: Job }) {
  const steps = stepStates(job);
  if (steps.length === 0) {
    const text = job.status === "pending" ? t("Waiting for an agent") : FINISHED.has(job.status) ? t("No steps ran") : t("Planning");
    return <span className="strip-wait">{text}</span>;
  }
  return (
    <div className="strip" role="img" aria-label={steps.map((s) => `${s.name} ${stateWord(s.state)}`).join(", ")}>
      {steps.map((s, i) => (
        <span key={i} className="seg" data-state={s.state}>
          <span className="seg-bar" />
          <span className="seg-label" aria-hidden="true">{s.name}</span>
        </span>
      ))}
    </div>
  );
}

// CopyButton copies text and shows a tick for a moment.
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [done, setDone] = useState(false);
  return (
    <button className="icon-btn" type="button" title={label} aria-label={label} onClick={async () => {
      await navigator.clipboard.writeText(text).catch(() => {});
      setDone(true);
      setTimeout(() => setDone(false), 1200);
    }}>
      <Icon name={done ? "check" : "copy"} size={14} />
    </button>
  );
}

// ProviderLink starts a sign-in through a forge, coming back to next;
// label words the button for the forge's name.
// short shows only the forge's name, where the page around says what the
// button does; its label still says it in full to screen readers.
export function ProviderLink({ provider, label, next, short = false }: { provider: string; label: (forge: string) => string; next: string; short?: boolean }) {
  const name = forgeName(provider);
  return (
    <a className="btn provider" href={`/auth/${provider}/login?next=${encodeURIComponent(next)}`} aria-label={short ? label(name) : undefined}>
      <ProviderMark provider={provider} />
      <span>{short ? name : label(name)}</span>
    </a>
  );
}
