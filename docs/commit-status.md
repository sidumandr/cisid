# Commit Status

**English** · [Türkçe](tr/commit-status.md)

cisid writes the state of each job to the commit being built as a **commit status** (`internal/status`). It appears as `cisid` next to the commit, in pull requests and in branch protection rules. It is written to every forge that has a token set; the table below shows GitHub's state names, and the others are in [forges.md](forges.md#commit-statuses).

| Job status | GitHub state | Description |
|---|---|---|
| `pending` | `pending` | Waiting for an agent |
| `running` | `pending` (`running` on GitLab) | Running on worker-1 |
| `success` | `success` | Passed in 1m23s |
| `failed` | `failure` | Step "test" failed with exit code 1 · Step "clone" failed with exit code 128 · Failed: no .cisid.yml in repository |
| `canceled` | `error` | Canceled |

Each matrix job gets its own context, for example `cisid (GO_VERSION=1.27)`. This way the statuses of the combinations do not overwrite each other, and each one can be made required in branch protection.

Pull request builds use the `cisid/pr` context (`cisid/pr (GO_VERSION=1.27)` in a matrix). When the same commit is both pushed to a branch and built in a PR, the two results do not overwrite each other. To require passing tests before a merge, make `cisid/pr` required in branch protection. Manual and scheduled builds also use their own contexts: `cisid/manual` and `cisid/schedule`.

## Setup

| Variable | Description |
|---|---|
| `CISID_GITHUB_TOKEN` (or the other forges' tokens) | Required. Statuses are not written to forges without a token. A GitHub fine-grained token needs the **Commit statuses: Read and write** and **Contents: Read-only** permissions. For a classic token, `repo:status` is enough, but reading `.cisid.yml` from private repositories needs `repo`. Other forges: [forges.md](forges.md). |
| `CISID_PUBLIC_URL` | Optional. If set, each status links to the job's page in the web UI (`<url>/jobs/<id>`). |

## Reliability

- The state last reported to the forge for each job is kept in the `jobs.reported_status` field. The reporter finds the jobs where this field differs from the real state and reports them.
- The reporter is woken right away when a job is created, assigned to an agent and finished, and it also checks on its own every 5 s.
- **If the forge is temporarily unreachable** (5xx, rate limit, network error), that round stops and the report is retried later. No state change is lost, even if the server restarts.
- **On permanent errors** (unauthorized token, deleted commit), a warning is logged and that state is given up. The same request is not repeated forever.
- Jobs older than one day are not reported. Commit statuses are also not sent for jobs created before this feature (migration `004`).
- Several servers may send the same state twice. This does no harm: the state written last wins.
- Jobs of a forge without a token are skipped silently (`cisid_commit_statuses_total{result="skipped"}`).
