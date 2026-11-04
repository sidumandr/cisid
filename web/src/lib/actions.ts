// Actions more than one screen offers.

import { request } from "./api";
import { openDialog } from "./dialog";
import { repoKeyRe, repoURL } from "./format";
import { t } from "./i18n";
import type { Job } from "./types";

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
