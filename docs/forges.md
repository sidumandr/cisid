# Forges

**English** · [Türkçe](tr/forges.md)

cisid builds repositories on five forges: **GitHub**, **Gitea**, **Forgejo**, **GitLab** (GitLab.com or your own installation) and **Bitbucket Cloud**. Each forge's client lives in its own package (`internal/github`, `internal/gitea`, `internal/gitlab`, `internal/bitbucket`). The common interface is in `internal/forge`.

You can configure three things for each forge, all optional (a fourth, signing in through the forge, is in [accounts.md](accounts.md#signing-in-through-a-forge)):

| Setting | What it does |
|---|---|
| URL | Gitea and Forgejo are enabled when their URL is set. GitHub, GitLab.com and Bitbucket Cloud are always enabled. |
| Token | Reads and clones private repositories, raises the rate limit and writes commit statuses. |
| Webhook secret | If set, it enables that forge's webhook URL: `/webhooks/<forge>`. If not, pushes are built through [polling](polling.md), or with manual and scheduled builds. |

## Repository names

cisid refers to a repository by its **repository key**: `owner/name` on GitHub; on the other forges the forge's name comes first.

| Forge | Example key |
|---|---|
| GitHub | `sidumandr/cisid` |
| Gitea | `gitea:office/app` |
| Forgejo | `forgejo:office/app` |
| GitLab | `gitlab:group/subgroup/project` (at most 20 levels) |
| Bitbucket | `bitbucket:workspace/repo` |

The key is the same in jobs (`repo`), steps (`CISID_REPO`), secrets, schedules and the cache. In API and UI URLs, the last segment is `{repo}` and the rest is `{owner}`; `:` and `/` inside `{owner}` are encoded in the URL:

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"branch":"main"}' \
  "http://localhost:8080/api/repos/gitlab:group%2Fsubgroup/project/builds"
```

GitHub keys have no prefix, so jobs, secrets and schedules created by older versions of cisid keep working as they are.

## GitHub

| Variable | Description |
|---|---|
| `CISID_GITHUB_TOKEN` | Fine-grained token: **Contents: Read-only** and **Commit statuses: Read and write** |
| `CISID_GITHUB_WEBHOOK_SECRET` | Enables `/webhooks/github` |
| `CISID_GITHUB_API_URL` | For GitHub Enterprise Server, e.g. `https://ghe.example.com/api/v3` |

Webhook: Repository → **Settings → Webhooks → Add webhook**, content type `application/json`, events *Pushes* and *Pull requests*. Details: [webhook.md](webhook.md).

## Gitea and Forgejo

Forgejo uses Gitea's API, so the same client works for both. You can also use both at the same time.

| Variable | Description |
|---|---|
| `CISID_GITEA_URL` / `CISID_FORGEJO_URL` | The server's web address, e.g. `https://git.example.com`. If not set, the forge is disabled. |
| `CISID_GITEA_TOKEN` / `CISID_FORGEJO_TOKEN` | Access token: `read:repository`, and `write:repository` for commit statuses |
| `CISID_GITEA_WEBHOOK_SECRET` / `CISID_FORGEJO_WEBHOOK_SECRET` | Enables `/webhooks/gitea` or `/webhooks/forgejo` |

Webhook: Repository → **Settings → Webhooks → Add webhook → Gitea** (*Forgejo* on Forgejo). Target URL `https://<cisid>/webhooks/gitea`, HTTP method `POST`, content type `application/json`, secret the value above. Triggers: *Push* and *Pull request* events (under *Custom events*: *Push*, *Pull Request* and *Pull Request Synchronized*). The signature is the HMAC-SHA256 of the body, in the `X-Gitea-Signature` header (`X-Forgejo-Signature` on Forgejo).

## GitLab

| Variable | Description |
|---|---|
| `CISID_GITLAB_URL` | The URL of your own GitLab installation. If not set, `https://gitlab.com`. |
| `CISID_GITLAB_TOKEN` | Personal, group or project access token with the `api` scope (`read_api` and `read_repository` are enough for reading only, but commit statuses require `api`) |
| `CISID_GITLAB_WEBHOOK_SECRET` | Enables `/webhooks/gitlab` |

Webhook: Project → **Settings → Webhooks → Add new webhook**. URL `https://<cisid>/webhooks/gitlab`, *Secret token* the value above, triggers *Push events* and *Merge request events*.

- GitLab does not sign webhooks; it sends the secret as is in the `X-Gitlab-Token` header. So the webhook URL must be reached over **HTTPS**, or the secret can be read in transit.
- Merge requests are built when they are opened, when they are reopened and when new commits arrive; changes such as the title do not start a build. When a merge request is closed or merged, its unfinished jobs are canceled.
- Merge requests from forks are built with `CISID_BUILD_FORKS=true`. The commit is fetched from the target project's `refs/merge-requests/<iid>/head` ref.

## Bitbucket Cloud

| Variable | Description |
|---|---|
| `CISID_BITBUCKET_TOKEN` | Repository, project or workspace **access token**: *Repositories: Read*, and *Repositories: Write* for commit statuses |
| `CISID_BITBUCKET_WEBHOOK_SECRET` | Enables `/webhooks/bitbucket` |

Webhook: Repository → **Repository settings → Webhooks → Add webhook**. URL `https://<cisid>/webhooks/bitbucket`, *Secret* the value above, triggers *Repository: Push* and *Pull request: Created, Updated, Merged, Declined*.

- The signature arrives in the `X-Hub-Signature` header (`sha256=<hex>`).
- If a push updated more than one branch, cisid opens a separate job for each branch. The response lists all of them as `job_ids`.
- In pull request events Bitbucket writes the commit as an abbreviated hash; cisid gets the full hash from the API. If Bitbucket is unreachable at that moment, the webhook returns `503`, and Bitbucket can retry the delivery later.
- Pull requests from forks are not built on Bitbucket: the fork's commits are not in the main repository.
- A commit status requires a link. If `CISID_PUBLIC_URL` is not set, the status links to the repository page.
- Bitbucket Data Center (Bitbucket installed on your own server) is not supported.

## Private repositories

For a forge that has a token, the clone step fetches its repositories with that token. The token is given only to the clone step, as a secret. It is masked as `***` in output and is not written to `.git/config`: git gets the credentials through a helper that reads them from environment variables (`GIT_CONFIG_*`). User steps do not see the token. Details: [pipeline.md](pipeline.md#how-it-runs).

## Commit statuses

Job statuses are written as commit statuses to every forge that has a token; forges without a token are skipped. Each forge uses different status names:

| Job | GitHub | Gitea / Forgejo | GitLab | Bitbucket |
|---|---|---|---|---|
| `pending` | `pending` | `pending` | `pending` | `INPROGRESS` |
| `running` | `pending` | `pending` | `running` | `INPROGRESS` |
| `success` | `success` | `success` | `success` | `SUCCESSFUL` |
| `failed` | `failure` | `failure` | `failed` | `FAILED` |
| `canceled` | `error` | `error` | `canceled` | `STOPPED` |

Details: [commit-status.md](commit-status.md).
