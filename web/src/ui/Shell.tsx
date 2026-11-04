// Shell is the frame of the working screens: the rail of places on the left
// (a bar at the bottom on phones) and the canvas the page draws in.

import { useState } from "react";
import { Link, NavLink, Outlet } from "react-router";

import { useMe } from "../lib/session";
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
  const [theme, toggleTheme] = useTheme();
  const account = me.user ? me.user.username : "Sign in";
  return (
    <div className="shell">
      <header className="topbar">
        <Link className="wordmark" to="/"><Wordmark height={22} /></Link>
      </header>
      <nav className="rail" aria-label="Main">
        <Link className="mark" to="/" aria-label="cisid home"><LogoMark size={34} /></Link>
        <NavLink className="rail-item" to="/jobs" title="Jobs">
          <Icon name="jobs" size={18} /><span className="rail-label">Jobs</span>
        </NavLink>
        <NavLink className="rail-item" to="/repos" title="Repositories">
          <Icon name="repos" size={18} /><span className="rail-label">Repositories</span>
        </NavLink>
        {me.user && me.role === "admin" ? (
          <NavLink className="rail-item" to="/users" title="Users">
            <Icon name="users" size={18} /><span className="rail-label">Users</span>
          </NavLink>
        ) : null}
        <NavLink className="rail-item rail-end" to={me.user ? "/account" : "/login"}
          title={me.user ? `${me.user.username} (${me.role})` : "Sign in"}>
          <Icon name="account" size={18} /><span className="rail-label">{account}</span>
        </NavLink>
        <button className="rail-item" type="button" title="Switch light and dark" onClick={toggleTheme}
          aria-label={theme === "dark" ? "Switch to light" : "Switch to dark"}>
          <ThemeIcon /><span className="rail-label">Theme</span>
        </button>
      </nav>
      <main className="canvas" aria-live="polite">
        <Outlet />
      </main>
    </div>
  );
}
