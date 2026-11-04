// ForgeAccounts is the part of the account page where a user connects
// their own GitHub, GitLab, Gitea or Forgejo access: the repositories they
// connect are then polled, cloned and reported on with it.

import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router";

import { getJSON, request } from "../lib/api";
import { openDialog } from "../lib/dialog";
import { ago, forgeName } from "../lib/format";
import { N_, t } from "../lib/i18n";
import type { ForgeAccount, ForgesInfo } from "../lib/types";
import { ProviderMark } from "../ui/Icon";
import { ErrorNote, Note } from "../ui/parts";

// TOKEN_HINTS say which token to make on each forge, and what it may do.
const TOKEN_HINTS: Record<string, string> = {
  github: N_("A fine-grained personal access token (Settings, Developer settings): Contents and Metadata read, Commit statuses read and write, on the repositories you will connect."),
  gitlab: N_("A personal access token (Preferences, Access tokens) with the api scope."),
  gitea: N_("An access token (Settings, Applications) that may read repositories and the user, and write commit statuses."),
  forgejo: N_("An access token (Settings, Applications) that may read repositories and the user, and write commit statuses."),
};

// CONNECT_PROBLEMS explain why connecting by OAuth came back here.
const CONNECT_PROBLEMS: Record<string, string> = {
  failed: N_("Connecting did not work. Try again, or connect with a token."),
  denied: N_("The forge did not give access, so nothing was connected."),
  unavailable: N_("Connecting by OAuth is not set up for this forge here: connect with a token."),
  expired: N_("That took too long, or started in another tab. Try again."),
};

export function ForgeAccounts() {
  const [info, setInfo] = useState<ForgesInfo | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [params] = useSearchParams();
  const connected = params.get("connected");
  const problem = CONNECT_PROBLEMS[params.get("connect") ?? ""];

  const load = useCallback(async () => {
    try {
      setInfo(await getJSON<ForgesInfo>("/api/me/forges"));
      setFailure(null);
    } catch (err) {
      setFailure(err instanceof Error ? err.message : String(err));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  async function withToken(forge: string) {
    const done = await openDialog({
      title: t("Connect {forge} with a token", { forge: forgeName(forge) }),
      text: t(TOKEN_HINTS[forge] ?? "An access token of your own on the forge."),
      fields: [{ name: "token", label: t("Token"), type: "password", autocomplete: "off" }],
      submit: t("Connect"),
      onSubmit: async (v) => {
        const token = (v.token ?? "").trim();
        if (!token) throw new Error(t("Write a token."));
        return request<ForgeAccount>("PUT", `/api/me/forges/${forge}`, { body: { token }, token: true });
      },
    });
    if (done) void load();
  }

  async function disconnect(a: ForgeAccount) {
    const ok = await openDialog({
      title: t("Disconnect {forge}?", { forge: forgeName(a.forge) }),
      text: t("cisid forgets your access to {forge}. The repositories you connected with it go back to the server's token for the forge, which may not reach them."),
      submit: t("Disconnect"),
      danger: true,
      onSubmit: () => request("DELETE", `/api/me/forges/${a.forge}`, { token: true }),
    });
    if (ok) void load();
  }

  let rows;
  if (failure) rows = <ErrorNote text={t("Couldn't load your forge accounts: {reason}", { reason: failure })} onRetry={load} />;
  else if (!info) rows = null;
  else if (!info.enabled) rows = <Note>{t("This server has no CISID_SECRET_KEY, so forge accounts cannot be connected: their tokens would have nothing to be sealed with.")}</Note>;
  else if (info.forges.length === 0) rows = <Note>{t("No forge here takes users' own access.")}</Note>;
  else {
    rows = (
      <>
        <div className="tr th forge-grid" role="row">
          <span role="columnheader">{t("Forge")}</span><span role="columnheader">{t("Account")}</span>
          <span role="columnheader">{t("Connected")}</span><span role="columnheader" />
        </div>
        {info.forges.map((f) => {
          const a = info.accounts.find((x) => x.forge === f.forge);
          return (
            <div key={f.forge} className="tr forge-grid" role="row">
              <div role="cell" className="line1 forge-name"><ProviderMark provider={f.forge} /><span className="name">{forgeName(f.forge)}</span></div>
              <div role="cell">
                {a ? <><span className="name">@{a.login}</span> <span className="muted">· {a.kind === "oauth" ? t("by OAuth") : t("with a token")}</span></>
                  : <span className="muted">{t("not connected")}</span>}
              </div>
              <div role="cell" className="muted">{a ? ago(a.created_at) : "-"}</div>
              <div role="cell" className="right row-actions">
                {a ? <button className="text-action danger" type="button" onClick={() => disconnect(a)}>{t("Disconnect")}</button> : <>
                  {f.oauth ? <a className="text-action" href={`/auth/${f.forge}/connect?next=${encodeURIComponent("/account")}`}>{t("Connect")}</a> : null}
                  <button className="text-action" type="button" onClick={() => withToken(f.forge)}>{f.oauth ? t("Use a token") : t("Connect with a token")}</button>
                </>}
              </div>
            </div>
          );
        })}
      </>
    );
  }

  return (
    <>
      <div className="toolbar">
        <span className="muted small">{t("Forge accounts: the repositories you connect are built with your own access.")}</span>
      </div>
      {connected ? <Note>{t("{forge} is connected. The repositories you connect on it are built with your access.", { forge: forgeName(connected) })}</Note> : null}
      {problem ? <ErrorNote text={t(problem)} /> : null}
      <div className="table" role="table" aria-label={t("Forge accounts")}>{rows}</div>
    </>
  );
}
