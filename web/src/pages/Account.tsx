// Account shows who is signed in, their password and personal tokens.

import { useCallback, useEffect, useState, type CSSProperties } from "react";
import { Navigate, useNavigate } from "react-router";

import { getJSON, logout, request } from "../lib/api";
import { openDialog } from "../lib/dialog";
import { ago, forgeName } from "../lib/format";
import { t } from "../lib/i18n";
import { loadMe, useMe } from "../lib/session";
import type { APIToken } from "../lib/types";
import { useTitle } from "../lib/useTitle";
import { ErrorNote, Fact, Header, Note } from "../ui/parts";

export function Account() {
  useTitle(t("Account"));
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
      title: user.has_password ? t("Change password") : t("Set a password"),
      fields: [
        ...(user.has_password ? [{ name: "current", label: t("Current password"), type: "password" as const, autocomplete: "current-password" }] : []),
        { name: "new", label: t("New password"), type: "password", autocomplete: "new-password", hint: t("At least 10 characters.") },
      ],
      submit: t("Save password"),
      onSubmit: (v) => request("PUT", "/api/me/password", { body: { current: v.current || "", new: v.new }, token: true }),
    });
    await loadMe();
  }

  async function newToken() {
    const made = await openDialog<{ token: string } | null>({
      title: t("New token"),
      text: t("The token acts as {user}, with the {role} role, for scripts and the API.", { user: user.username, role: me.role ?? "" }),
      fields: [{ name: "name", label: t("Name"), placeholder: t("deploy script") }],
      submit: t("Create token"),
      onSubmit: (v) => request<{ token: string }>("POST", "/api/me/tokens", { body: { name: (v.name ?? "").trim() }, token: true }),
    });
    if (!made?.result) return;
    await openDialog({
      title: t("Copy the token now"),
      text: t("It is not shown again. Send it as \"Authorization: Bearer <token>\"."),
      fields: [{ name: "token", label: t("Token"), value: made.result.token }],
      submit: t("Done"),
      cancel: false,
    });
    void load();
  }

  async function revoke(tk: APIToken) {
    const ok = await openDialog({
      title: t("Revoke {name}?", { name: tk.name }),
      text: t("Scripts that use it stop working at once."),
      submit: t("Revoke token"),
      danger: true,
      onSubmit: () => request("DELETE", `/api/me/tokens/${tk.id}`, { token: true }),
    });
    if (ok) void load();
  }

  let rows;
  if (failure) rows = <ErrorNote text={t("Couldn't load tokens: {reason}", { reason: failure })} onRetry={load} />;
  else if (!tokens) rows = null;
  else if (tokens.length === 0) rows = <Note>{t("No tokens. A token lets a script use the API as you.")}</Note>;
  else {
    rows = (
      <>
        <div className="tr th secret-grid" role="row">
          <span role="columnheader">{t("Name")}</span><span role="columnheader">{t("Created")}</span>
          <span role="columnheader">{t("Last used")}</span><span role="columnheader" />
        </div>
        {tokens.map((tk) => (
          <div key={tk.id} className="tr secret-grid" role="row">
            <div role="cell" className="line1"><span className="name">{tk.name}</span></div>
            <div role="cell" className="num">{ago(tk.created_at)}</div>
            <div role="cell" className="num">{tk.last_used_at ? ago(tk.last_used_at) : t("never")}</div>
            <div role="cell" className="right"><button className="text-action danger" type="button" onClick={() => revoke(tk)}>{t("Revoke")}</button></div>
          </div>
        ))}
      </>
    );
  }

  const forges = Object.entries(user.identities || {}).map(([f, l]) => `${forgeName(f)}: ${l}`).join(", ") || t("None");
  return (
    <>
      <Header crumbs={<span>{t("Account")}</span>} title={user.username} lede={t("Signed in as {role}.", { role: me.role ?? "" })}
        actions={<button className="btn" type="button" onClick={async () => { await logout(); navigate("/"); }}>{t("Sign out")}</button>} />
      <dl className="facts" style={{ "--n": 4 } as CSSProperties}>
        <Fact name={t("Username")}>{user.username}</Fact>
        <Fact name={t("Role")}>{me.role}</Fact>
        <Fact name={t("Forges")}>{forges}</Fact>
        <Fact name={t("Password")}>{user.has_password ? t("Set") : t("None, signs in through a forge")}</Fact>
      </dl>
      <div className="toolbar">
        <span className="muted small">{t("Personal API tokens")}</span>
        <span className="toolbar-actions">
          <button className="btn small" type="button" onClick={changePassword}>{user.has_password ? t("Change password") : t("Set a password")}</button>
          <button className="btn small" type="button" onClick={newToken}>{t("New token")}</button>
        </span>
      </div>
      <div className="table" role="table" aria-label={t("Tokens")}>{rows}</div>
    </>
  );
}
