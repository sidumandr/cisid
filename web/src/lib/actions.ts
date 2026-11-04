// Actions more than one screen offers.

import { createElement } from "react";

import { request } from "./api";
import { openDialog } from "./dialog";
import { repoKeyRe, repoURL } from "./format";
import { t } from "./i18n";
import type { Connected, Job, Reachable } from "./types";

// runBuild builds the head of a branch now and returns the new job, or
// null when the dialog was dismissed.
export async function runBuild(repo: string, known: string[] = []): Promise<Job | null> {
  const done = await openDialog<Job | null>({
    title: t("Run a build"),
    text: t("Builds the latest commit of a branch, as a push would."),
    fields: [
      { name: "repo", label: t("Repository"), value: repo, placeholder: "owner/name", suggestions: known,
        hint: t("On GitHub owner/name; elsewhere prefix the forge, as in gitea:owner/name or gitlab:group/project.") },
      { name: "branch", label: t("Branch"), hint: t("Empty builds the repository's default branch.") },
    ],
    submit: t("Run build"),
    onSubmit: async (v) => {
      const name = (v.repo ?? "").trim();
      if (!repoKeyRe.test(name)) throw new Error(t("Write the repository as owner/name, or forge:owner/name."));
      const queued = await request<Job>("POST", `/api/repos/${repoURL(name)}/builds`,
        { body: { branch: (v.branch ?? "").trim() }, token: true });
      if (!queued) throw new Error(t("A build needs a maintainer: sign in or use a token."));
      return queued;
    },
  });
  return done?.result ?? null;
}

export const knownRepos = (jobs: Job[]) => [...new Set(jobs.map((j) => j.repo))].sort();

// STARTER is a first .cisid.yml to commit, for a repository without one.
export const STARTER = `steps:
  - name: hello
    image: alpine:3.22
    commands:
      - echo "Hello from cisid"
      - ls -la`;

// forgeOf names the forge of a repository key: GitHub when it has no prefix.
export const forgeOf = (repo: string) => (/^([a-z]+):/.exec(repo)?.[1] ?? "github");

// connectRepo asks which repository to connect and how cisid hears of its
// commits, connects it, and then tells what is left to do: a pipeline to
// commit, a webhook to add. It returns the repository, or null.
export async function connectRepo(): Promise<Connected | null> {
  let reachable: Reachable = { repositories: [], unlisted: [] };
  try {
    reachable = (await request<Reachable>("GET", "/api/forges/repositories", { token: true })) ?? reachable;
  } catch {
    // Picking from a list is a help; typing the name still works.
  }
  const picks = reachable.repositories.map((r) => r.repo);
  const done = await openDialog<Connected | null>({
    title: t("Connect a repository"),
    text: picks.length
      ? t("Pick one of the repositories the server's tokens reach, or write another. cisid then builds it as new commits arrive.")
      : t("Write the repository. cisid then builds it as new commits arrive. With CISID_GITHUB_TOKEN (or another forge's token) set on the server, you can pick from your repositories here, and private ones work too."),
    fields: [
      { name: "repo", label: t("Repository"), placeholder: "owner/name", suggestions: picks,
        hint: t("On GitHub owner/name; elsewhere prefix the forge, as in gitea:owner/name or gitlab:group/project.") },
      { name: "how", label: t("New commits"), options: [
        { value: "poll", label: t("Poll the default branch") },
        { value: "webhook", label: t("A webhook from the forge") },
      ], hint: t("Polling checks for new commits regularly and works anywhere, even on your own computer. A webhook tells cisid at once, but needs a server the forge can reach.") },
    ],
    submit: t("Connect"),
    onSubmit: async (v) => {
      const repo = (v.repo ?? "").trim();
      if (!repoKeyRe.test(repo)) throw new Error(t("Write the repository as owner/name, or forge:owner/name."));
      const made = await request<Connected>("POST", "/api/repos", { body: { repo, poll: v.how !== "webhook" }, token: true });
      if (!made) throw new Error(t("Connecting a repository needs a maintainer."));
      return made;
    },
  });
  const made = done?.result ?? null;
  if (!made) return null;
  if (!made.has_pipeline) {
    await openDialog({
      title: t("Add a pipeline"),
      text: t("{repo} has no .cisid.yml on {branch} yet, so nothing builds. Commit one like this to the root of the repository, then edit its steps:",
        { repo: made.repo, branch: made.default_branch }),
      extra: createElement("pre", { className: "snippet" }, STARTER),
      submit: t("Copy"),
      cancel: t("Later"),
      onSubmit: async () => {
        await navigator.clipboard.writeText(STARTER).catch(() => {
          throw new Error(t("Couldn't copy. Select the text above and copy it."));
        });
      },
    });
  }
  if (!made.polled) {
    const url = `${location.origin}/webhooks/${forgeOf(made.repo)}`;
    await openDialog({
      title: t("Add the webhook"),
      text: t("In the repository's settings on its forge, add a webhook to {url}, with content type application/json, the server's webhook secret for the forge, and push and pull request events.", { url }),
      submit: t("Done"),
      cancel: false,
    });
  }
  return made;
}
