// Shell is the frame of the working screens: the rail of places on the left
// (a bar at the bottom on phones) and the canvas the page draws in.

import { useState } from "react";
import { Link, Navigate, NavLink, Outlet, useLocation } from "react-router";

import { LANGS, setLang, t, useLang } from "../lib/i18n";
import { useMe } from "../lib/session";
import { useCursor } from "./cursor";
import { Icon, ThemeIcon } from "./Icon";
import { LogoMark, Wordmark } from "./Logo";

const THEME_KEY = "cisid.theme";

export function currentTheme(): "light" | "dark" {
  const set = document.documentElement.dataset.theme;
  if (set === "light" || set === "dark") return set;
  return matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

// applySavedTheme applies the theme the reader picked before, if any.
export function applySavedTheme() {
  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (saved === "light" || saved === "dark") document.documentElement.dataset.theme = saved;
  } catch {
    // Storage can be off; the system theme then applies.
  }
}

export function useTheme(): [string, () => void] {
  const [theme, setTheme] = useState(currentTheme);
  const toggle = () => {
    const next = currentTheme() === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    setTheme(next);
    try {
      localStorage.setItem(THEME_KEY, next);
    } catch {
      // Not remembered; it still applies now.
    }
  };
  return [theme, toggle];
}

export function Shell() {
  const me = useMe();
  const location = useLocation();
  const [theme, toggleTheme] = useTheme();
  const [plainCursor, toggleCursor] = useCursor();
  const lang = useLang();
  const other = LANGS.find((l) => l.lang !== lang)!;
  // Where reading needs a sign-in, the working screens start at the sign-in
  // page and come back here.
  if (!me.public_read && !me.via) {
    return <Navigate to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} replace />;
  }
  const account = me.user ? me.user.username : t("Sign in");
  return (
    <div className="shell">
      <header className="topbar">
        <Link className="wordmark" to="/"><Wordmark height={22} /></Link>
      </header>
      <nav className="rail" aria-label={t("Main")}>
        <Link className="mark" to="/" aria-label={t("cisid home")}><LogoMark size={34} /></Link>
        <NavLink className="rail-item" to="/jobs" title={t("Jobs")}>
          <Icon name="jobs" size={18} /><span className="rail-label">{t("Jobs")}</span>
        </NavLink>
        <NavLink className="rail-item" to="/repos" title={t("Repositories")}>
          <Icon name="repos" size={18} /><span className="rail-label">{t("Repositories")}</span>
        </NavLink>
        {me.user && me.role === "admin" ? (
          <NavLink className="rail-item" to="/users" title={t("Users")}>
            <Icon name="users" size={18} /><span className="rail-label">{t("Users")}</span>
          </NavLink>
        ) : null}
        <NavLink className="rail-item rail-end" to={me.user ? "/account" : "/login"}
          title={me.user ? `${me.user.username} (${me.role})` : t("Sign in")}>
          <Icon name="account" size={18} /><span className="rail-label">{account}</span>
        </NavLink>
        <button className="rail-item" type="button" title={t("Switch light and dark")} onClick={toggleTheme}
          aria-label={theme === "dark" ? t("Switch to light") : t("Switch to dark")}>
          <ThemeIcon /><span className="rail-label">{t("Theme")}</span>
        </button>
        <button className="rail-item cursor-toggle" type="button" onClick={toggleCursor}
          title={plainCursor ? t("Use the owl cursor") : t("Use the normal cursor")}
          aria-label={plainCursor ? t("Use the owl cursor") : t("Use the normal cursor")}>
          <Icon name={plainCursor ? "owl" : "pointer"} size={18} /><span className="rail-label">{t("Cursor")}</span>
        </button>
        <button className="rail-item" type="button" lang={other.lang} title={other.name} onClick={() => setLang(other.lang)}
          aria-label={other.name}>
          <span className="rail-lang" aria-hidden="true">{other.label}</span><span className="rail-label">{other.name}</span>
        </button>
      </nav>
      <main className="canvas" aria-live="polite">
        <Outlet />
      </main>
    </div>
  );
}
