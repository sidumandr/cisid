// Account shows who is signed in, their password and personal tokens.

import { useCallback, useEffect, useState, type CSSProperties } from "react";
import { Navigate, useNavigate } from "react-router";

import { getJSON, logout, request } from "../lib/api";
import { openDialog } from "../lib/dialog";
import { ago, forgeName } from "../lib/format";
import { loadMe, useMe } from "../lib/session";
import type { APIToken } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { ErrorNote, Fact, Header, Note } from "../ui/parts";

export function Account() {
  useTitle("Account");
  const me = useMe();
  const navigate = useNavigate();
  const [tokens, setTokens] = useState<APIToken[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setTokens(await getJSON<APIToken[]>("/api/me/tokens"));
      setFailure(null);
    } catch (err) {
      setFailure(err instanceof Error ? err.message : String(err));
    }
  }, []);
  useEffect(() => {
    if (me.user) void load();
  }, [me.user, load]);

  if (!me.user) return <Navigate to="/login?next=/account" replace />;
  const user = me.user;

  async function changePassword() {
    await openDialog({
      title: user.has_password ? "Change password" : "Set a password",
      fields: [
        ...(user.has_password ? [{ name: "current", label: "Current password", type: "password" as const, autocomplete: "current-password" }] : []),
        { name: "new", label: "New password", type: "password", autocomplete: "new-password", hint: "At least 10 characters." },
      ],
      submit: "Save password",
      onSubmit: (v) => request("PUT", "/api/me/password", { body: { current: v.current || "", new: v.new }, token: true }),
    });
    await loadMe();
  }

  async function newToken() {
    const made = await openDialog<{ token: string } | null>({
      title: "New token",
      text: `The token acts as ${user.username}, with the ${me.role} role, for scripts and the API.`,
      fields: [{ name: "name", label: "Name", placeholder: "deploy script" }],
      submit: "Create token",
      onSubmit: (v) => request<{ token: string }>("POST", "/api/me/tokens", { body: { name: (v.name ?? "").trim() }, token: true }),
    });
    if (!made?.result) return;
    await openDialog({
      title: "Copy the token now",
      text: "It is not shown again. Send it as \"Authorization: Bearer <token>\".",
      fields: [{ name: "token", label: "Token", value: made.result.token }],
      submit: "Done",
      cancel: false,
    });
    void load();
  }

  async function revoke(tk: APIToken) {
    const ok = await openDialog({
      title: `Revoke ${tk.name}?`,
      text: "Scripts that use it stop working at once.",
      submit: "Revoke token",
      danger: true,
      onSubmit: () => request("DELETE", `/api/me/tokens/${tk.id}`, { token: true }),
    });
    if (ok) void load();
  }

  let rows;
  if (failure) rows = <ErrorNote text={`Couldn't load tokens: ${failure}`} onRetry={load} />;
  else if (!tokens) rows = null;
  else if (tokens.length === 0) rows = <Note>No tokens. A token lets a script use the API as you.</Note>;
  else {
    rows = (
      <>
        <div className="tr th secret-grid" role="row">
          <span role="columnheader">Name</span><span role="columnheader">Created</span>
          <span role="columnheader">Last used</span><span role="columnheader" />
        </div>
        {tokens.map((tk) => (
          <div key={tk.id} className="tr secret-grid" role="row">
            <div role="cell" className="line1"><span className="name">{tk.name}</span></div>
            <div role="cell" className="num">{ago(tk.created_at)}</div>
            <div role="cell" className="num">{tk.last_used_at ? ago(tk.last_used_at) : "never"}</div>
            <div role="cell" className="right"><button className="text-action danger" type="button" onClick={() => revoke(tk)}>Revoke</button></div>
          </div>
        ))}
      </>
    );
  }

  const forges = Object.entries(user.identities || {}).map(([f, l]) => `${forgeName(f)}: ${l}`).join(", ") || "None";
  return (
    <>
      <Header crumbs={<span>Account</span>} title={user.username} lede={`Signed in as ${me.role}.`}
        actions={<button className="btn" type="button" onClick={async () => { await logout(); navigate("/"); }}>Sign out</button>} />
      <dl className="facts" style={{ "--n": 4 } as CSSProperties}>
        <Fact name="Username">{user.username}</Fact>
        <Fact name="Role">{me.role}</Fact>
        <Fact name="Forges">{forges}</Fact>
        <Fact name="Password">{user.has_password ? "Set" : "None, signs in through a forge"}</Fact>
      </dl>
      <div className="toolbar">
        <span className="muted small">Personal API tokens</span>
        <span className="toolbar-actions">
          <button className="btn small" type="button" onClick={changePassword}>{user.has_password ? "Change password" : "Set a password"}</button>
          <button className="btn small" type="button" onClick={newToken}>New token</button>
        </span>
      </div>
      <div className="table" role="table" aria-label="Tokens">{rows}</div>
    </>
  );
}
