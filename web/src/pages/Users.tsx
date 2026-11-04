// Users lists the accounts for admins and lets them add and change them.

import { useCallback, useEffect, useState } from "react";

import { request } from "../lib/api";
import { openDialog } from "../lib/dialog";
import { forgeName, parseIdentities, showIdentities } from "../lib/format";
import { t } from "../lib/i18n";
import type { Role, User } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { ErrorNote, Header, Note } from "../ui/parts";

const ROLES: Role[] = ["viewer", "maintainer", "admin"];
const identityHint = () => t("Their logins on the forges, as github=ahmet-y, gitlab=ahmet. They can then sign in through those forges.");

// usersLede counts the accounts and the admins among them.
function usersLede(n: number, admins: number): string {
  if (n === 1) return admins ? t("1 user, an admin.") : t("1 user.");
  return admins === 1 ? t("{n} users, 1 of them an admin.", { n }) : t("{n} users, {admins} of them admins.", { n, admins });
}

// The status choices: the value the form reads, the word the reader sees.
const STATES = () => ({ active: { value: "active", label: t("active") }, disabled: { value: "disabled", label: t("disabled") } });

export function Users() {
  useTitle(t("Users"));
  const [list, setList] = useState<User[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [denied, setDenied] = useState(false);

  const load = useCallback(async () => {
    try {
      const l = await request<User[]>("GET", "/api/users", { token: true });
      if (!l) {
        setDenied(true);
        return;
      }
      setDenied(false);
      setList(l);
      setFailure(null);
    } catch (err) {
      setFailure(err instanceof Error ? err.message : String(err));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  async function add() {
    const done = await openDialog({
      title: t("Add user"),
      fields: [
        { name: "username", label: t("Username"), hint: t("Lowercase letters, digits, dots, dashes and underscores.") },
        { name: "role", label: t("Role"), options: ROLES },
        { name: "password", label: t("Password"), type: "password", autocomplete: "new-password", hint: t("Optional: without one, they sign in through a forge.") },
        { name: "identities", label: t("Forge logins"), hint: identityHint() },
      ],
      submit: t("Add user"),
      onSubmit: (v) => {
        const body: Record<string, unknown> = { username: (v.username ?? "").trim(), role: v.role, identities: parseIdentities(v.identities ?? "") };
        if (v.password) body.password = v.password;
        return request("POST", "/api/users", { body, token: true });
      },
    });
    if (done) void load();
  }

  async function edit(u: User) {
    const done = await openDialog({
      title: t("Change {user}", { user: u.username }),
      fields: [
        { name: "role", label: t("Role"), options: [u.role, ...ROLES.filter((r) => r !== u.role)] },
        { name: "state", label: t("Status"), options: u.disabled ? [STATES().disabled, STATES().active] : [STATES().active, STATES().disabled] },
        { name: "password", label: t("New password"), type: "password", autocomplete: "new-password", hint: t("Empty keeps the password.") },
        { name: "identities", label: t("Forge logins"), value: showIdentities(u.identities), hint: identityHint() },
      ],
      submit: t("Save"),
      onSubmit: (v) => {
        const body: Record<string, unknown> = { role: v.role, disabled: v.state === "disabled", identities: parseIdentities(v.identities ?? "") };
        if (v.password) body.password = v.password;
        return request("PATCH", `/api/users/${encodeURIComponent(u.username)}`, { body, token: true });
      },
    });
    if (done) void load();
  }

  async function remove(u: User) {
    const ok = await openDialog({
      title: t("Delete {user}?", { user: u.username }),
      text: t("Their sessions and tokens stop working. Builds they started stay."),
      submit: t("Delete user"),
      danger: true,
      onSubmit: () => request("DELETE", `/api/users/${encodeURIComponent(u.username)}`, { token: true }),
    });
    if (ok) void load();
  }

  let rows;
  if (denied) rows = <Note>{t("Only admins see users.")}</Note>;
  else if (failure) rows = <ErrorNote text={t("Couldn't load users: {reason}", { reason: failure })} onRetry={load} />;
  else if (!list) rows = null;
  else {
    rows = (
      <>
        <div className="tr th user-grid" role="row">
          <span role="columnheader">{t("User")}</span><span role="columnheader">{t("Role")}</span>
          <span role="columnheader">{t("Signs in with")}</span><span role="columnheader" />
        </div>
        {list.map((u) => (
          <div key={u.username} className="tr user-grid" role="row">
            <div role="cell" className="line1">
              <span className="name">{u.username}</span>
              {u.disabled ? <span className="pill" data-status="canceled">{t("disabled")}</span> : null}
            </div>
            <div role="cell">{u.role}</div>
            <div role="cell" className="muted">
              {[u.has_password ? t("password") : null, ...Object.keys(u.identities || {}).map(forgeName)].filter(Boolean).join(", ") || t("nothing yet")}
            </div>
            <div role="cell" className="right row-actions">
              <button className="text-action" type="button" onClick={() => edit(u)}>{t("Change")}</button>
              <button className="text-action danger" type="button" onClick={() => remove(u)}>{t("Delete")}</button>
            </div>
          </div>
        ))}
      </>
    );
  }

  const admins = list ? list.filter((u) => u.role === "admin").length : 0;
  return (
    <>
      <Header crumbs={<span>{t("Users")}</span>} title={t("Users")}
        lede={list ? usersLede(list.length, admins) : t("Loading users")}
        actions={<button className="btn primary" type="button" onClick={add}>{t("Add user")}</button>} />
      <div className="table" role="table" aria-label={t("Users")}>{rows}</div>
    </>
  );
}
