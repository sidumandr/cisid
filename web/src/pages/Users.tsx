// Users lists the accounts for admins and lets them add and change them.

import { useCallback, useEffect, useState } from "react";

import { request } from "../lib/api";
import { openDialog } from "../lib/dialog";
import { forgeName, parseIdentities, showIdentities } from "../lib/format";
import type { Role, User } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { ErrorNote, Header, Note } from "../ui/parts";

const ROLES: Role[] = ["viewer", "maintainer", "admin"];
const IDENTITY_HINT = "Their logins on the forges and Google, as github=ahmet-y, google=ahmet@example.com. They can then sign in with those.";

// usersLede counts the accounts and the admins among them.
function usersLede(n: number, admins: number): string {
  if (n === 1) return admins ? "1 user, an admin." : "1 user.";
  return `${n} users, ${admins} of them ${admins === 1 ? "an admin" : "admins"}.`;
}

export function Users() {
  useTitle("Users");
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
      title: "Add user",
      fields: [
        { name: "username", label: "Username", hint: "Lowercase letters, digits, dots, dashes and underscores." },
        { name: "role", label: "Role", options: ROLES },
        { name: "password", label: "Password", type: "password", autocomplete: "new-password", hint: "Optional: without one, they sign in through a forge." },
        { name: "identities", label: "Linked logins", hint: IDENTITY_HINT },
      ],
      submit: "Add user",
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
      title: `Change ${u.username}`,
      fields: [
        { name: "role", label: "Role", options: [u.role, ...ROLES.filter((r) => r !== u.role)] },
        { name: "state", label: "Status", options: u.disabled ? ["disabled", "active"] : ["active", "disabled"] },
        { name: "password", label: "New password", type: "password", autocomplete: "new-password", hint: "Empty keeps the password." },
        { name: "identities", label: "Linked logins", value: showIdentities(u.identities), hint: IDENTITY_HINT },
      ],
      submit: "Save",
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
      title: `Delete ${u.username}?`,
      text: "Their sessions and tokens stop working. Builds they started stay.",
      submit: "Delete user",
      danger: true,
      onSubmit: () => request("DELETE", `/api/users/${encodeURIComponent(u.username)}`, { token: true }),
    });
    if (ok) void load();
  }

  let rows;
  if (denied) rows = <Note>Only admins see users.</Note>;
  else if (failure) rows = <ErrorNote text={`Couldn't load users: ${failure}`} onRetry={load} />;
  else if (!list) rows = null;
  else {
    rows = (
      <>
        <div className="tr th user-grid" role="row">
          <span role="columnheader">User</span><span role="columnheader">Role</span>
          <span role="columnheader">Signs in with</span><span role="columnheader" />
        </div>
        {list.map((u) => (
          <div key={u.username} className="tr user-grid" role="row">
            <div role="cell" className="line1">
              <span className="name">{u.username}</span>
              {u.disabled ? <span className="pill" data-status="canceled">disabled</span> : null}
            </div>
            <div role="cell">{u.role}</div>
            <div role="cell" className="muted">
              {[u.has_password ? "password" : null, ...Object.keys(u.identities || {}).map(forgeName)].filter(Boolean).join(", ") || "nothing yet"}
            </div>
            <div role="cell" className="right row-actions">
              <button className="text-action" type="button" onClick={() => edit(u)}>Change</button>
              <button className="text-action danger" type="button" onClick={() => remove(u)}>Delete</button>
            </div>
          </div>
        ))}
      </>
    );
  }

  const admins = list ? list.filter((u) => u.role === "admin").length : 0;
  return (
    <>
      <Header crumbs={<span>Users</span>} title="Users"
        lede={list ? usersLede(list.length, admins) : "Loading users"}
        actions={<button className="btn primary" type="button" onClick={add}>Add user</button>} />
      <div className="table" role="table" aria-label="Users">{rows}</div>
    </>
  );
}
