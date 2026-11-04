# Job API

**English** · [Türkçe](tr/api.md)

The server exposes jobs and their logs over an HTTP API (`internal/api/jobs.go`). Jobs can be read, canceled and rerun. All responses are JSON, and the live log stream uses [Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html).

> Read endpoints require a sign-in or a token unless the server makes them public ([Read access](#read-access)). Endpoints that make changes require the `maintainer` role: the API token, a user's personal token, or a session ([accounts.md](accounts.md)).

## Endpoints

| Path | Description |
|---|---|
| `GET /api/jobs?limit=50` | Recent jobs, newest first. `limit` is at most 200. |
| `GET /api/jobs/{id}` | A single job |
| `GET /api/jobs/{id}/logs?after=0` | Log chunks with `seq` > `after`, at most 1000 per request |
| `GET /api/jobs/{id}/logs/stream` | Live log stream (SSE) |
| `GET /api/jobs/{id}/artifacts` | The job's artifacts: `name`, `size`, `sha256`, `created_at` ([Artifacts](#artifacts)) |
| `GET /api/jobs/{id}/artifacts/{name}` | An artifact's content, as a download |
| `POST /api/jobs/{id}/cancel` | Cancel the job (maintainer) |
| `POST /api/jobs/{id}/rerun` | Create a new job for the same commit (maintainer) |
| `POST /api/jobs/{id}/approve` | Let agents run a job from a fork that waits for approval (maintainer); `409` if it does not wait ([webhook.md](webhook.md#pull-requests)) |

Errors: `400` for an invalid parameter, `404` for a job that does not exist, `429` when a client exceeds the rate limit (`Retry-After: 1`, see `CISID_RATE_LIMIT`). The body has the form `{"status":"error","reason":"..."}`.

### Job

```json
{
  "id": 42,
  "repo": "sidumandr/cisid",
  "clone_url": "https://github.com/sidumandr/cisid.git",
  "branch": "main",
  "commit_sha": "6113728f27ae82c7b1a177c8d03f9e96e0adf246",
  "status": "failed",
  "created_at": "2026-11-03T10:45:01Z",
  "event": "pull_request",
  "pull_request": 7,
  "target_branch": "main",
  "fork": true,
  "agent": "worker-1",
  "started_at": "2026-11-03T10:45:01Z",
  "steps": ["clone", "test"],
  "matrix": {"GO_VERSION": "1.27"},
  "parent_id": 41,
  "exit_code": 1,
  "failed_step": 1,
  "error": "",
  "finished_at": "2026-11-03T10:45:31Z",
  "logs_expired_at": "2027-02-01T09:00:00Z"
}
```

`status`: `pending`, `running`, `success`, `failed`, `canceled`. `event` is the event that started the job: `push`, `pull_request`, `manual` or `schedule`. In pull request jobs, `pull_request` is the PR number, `target_branch` is the branch it merges into, `branch` is the PR's own branch, and `fork` means the change comes from a fork. `steps` holds the names of the steps planned for the job and fills in when the job is given to an agent. `failed_step` and the `step` in log chunks give the position in this list (`0` is always clone). Steps skipped by `when` are not in the list. `matrix` is the combination the job runs, and `parent_id` is the first matrix job that created it ([pipeline.md](pipeline.md#matrix)). `current_step` appears only on running jobs: it is the position of the step whose log arrived last. `needs_approval` marks a job from a fork that waits for a maintainer, and `approved_by` says who approved it. `labels` are the labels an agent needs to run the job, once the job went back to the queue for them ([pipeline.md](pipeline.md#labels)). `step_results` says how each step in `steps` went, as its agent reported it: `state` (`pending`, `running`, `success`, `failed`, `canceled` or `skipped`), `exit_code`, `started_at` and `finished_at` (the agent's clock); it is absent for jobs run by agents older than 0.4. If `logs_expired_at` is set, the job's logs were deleted on that date because their retention period ran out (`CISID_LOG_RETENTION`, [server.md](server.md)); the log endpoints return nothing.

### Log chunk

```json
{"job_id": 42, "step": 1, "stream": "stderr", "seq": 17, "data": "+ go test ./...\n"}
```

`seq` starts at 1 within a job and sets the order. Chunks may not end on a line boundary. A client shows them by joining the chunks of the same `stream` end to end.

## Read access

Unless `CISID_READ_ACCESS=public`, the job, log and live stream endpoints and `/metrics` require one of these:

- An `Authorization: Bearer <token>` header: the read token, the API token, or a user's personal token.
- A user's session cookie ([accounts.md](accounts.md#sign-in-and-sessions)).
- A read cookie obtained from `POST /api/login` with the body `{"token":"..."}`. If the token is correct, it returns `204` and the server sets an HttpOnly, `SameSite=Strict` cookie. The web UI uses cookies because the browser's live log stream (EventSource) cannot send headers. This cookie carries a digest of the token, not the token itself, and only allows reading: it cannot make changes even when you sign in with the API token. `POST /api/logout` deletes the cookies.

Missing or wrong credentials return `401`. Health checks (`/healthz`, `/readyz`) and the webhook are always open.

```bash
curl -H "Authorization: Bearer $CISID_READ_TOKEN" http://localhost:8080/api/jobs
```

## Connected repositories

A repository is connected so it is listed, and polled or built, before any webhook arrives for it. The UI's **Connect a repository** button uses these:

| Endpoint | Description |
|---|---|
| `GET /api/repos` | The connected repositories: `repo`, `clone_url`, `default_branch`, `added_by`, `added_at`, `polled`, whether a branch of it is polled, and `via`, the user whose own forge access builds it (requires read access) |
| `POST /api/repos` | Connects `{"repo":"owner/name","poll":true}` (maintainer). The repository is looked up on its forge, with the caller's own access when they connected that forge ([accounts.md](accounts.md#connecting-forge-accounts)): it is then built with their access. With `poll`, its default branch is polled, and the first poll builds its head. The answer is the repository with `has_pipeline`: `false` when the default branch has no `.cisid.yml`, so nothing builds yet. `201` if new, `200` if it was connected; `404` if the forge does not have it, or the access used cannot see it; `409` with `poll` when polling is off. |
| `DELETE /api/repos/{owner}/{repo}` | Disconnects it (maintainer): its polled branches and schedules go, its jobs, secrets and notifications stay. A webhook on the forge still starts builds until it is removed there. `204`, or `404` if nothing was connected. |
| `GET /api/forges/repositories` | The repositories the caller's own forge accounts reach, and the server's forge tokens on the forges they did not connect, to pick from (maintainer): `{"repositories":[{"repo","forge","private"}],"unlisted":["gitlab"]}`. GitHub lists the hundred pushed to last, GitLab the hundred projects active last, Gitea and Forgejo fifty; `unlisted` names the forges without a token, or that cannot list (Bitbucket). |

## Polling

For forges whose webhooks cannot reach the server, branches can be polled: `GET`, `POST /api/repos/{owner}/{repo}/watches` and `DELETE /api/repos/{owner}/{repo}/watches/{branch}`. Details: [polling.md](polling.md).

## Artifacts

The files a build kept ([pipeline.md](pipeline.md#artifacts)) are read like jobs: `GET /api/jobs/{id}/artifacts` lists them, and `GET /api/jobs/{id}/artifacts/{name}` downloads one, with the `/` in its name as it is (`.../artifacts/dist/app`). Downloads come as `application/octet-stream` with `Content-Disposition: attachment`, so a browser saves them instead of opening them, whatever they contain. The `ETag` is the content's SHA-256.

```bash
curl -fLO http://localhost:8080/api/jobs/42/artifacts/dist/app
```

## Notifications and badges

Where a repository's build results are announced (a chat, a webhook, email): `GET /api/repos/{owner}/{repo}/notifications`, `PUT` and `DELETE .../notifications/{name}`, `POST .../notifications/{name}/test`, all for maintainers. A branch's latest result as an SVG badge: `GET /api/repos/{owner}/{repo}/badge.svg?branch=main`. Details: [notifications.md](notifications.md).

## Manual and scheduled builds

You can build the latest commit of a branch without waiting for a push. The server gets the commit and the clone URL from the API of the repository's forge (private repositories need the forge's token). Repositories on other forges carry their prefix in the path, e.g. `/api/repos/gitea:office/app/builds` or `/api/repos/gitlab:group%2Fsub/project/builds` ([forges.md](forges.md#repository-names)). These endpoints require a maintainer; listing schedules requires read access.

| Path | Description |
|---|---|
| `POST /api/repos/{owner}/{repo}/builds` | Builds the latest commit of the branch right away, with `{"branch":"main"}`; without `branch`, the default branch. Returns `201` and the job. `404` if the repository or branch does not exist or the server is not configured for that forge, `503` if the forge cannot be reached. |
| `GET /api/repos/{owner}/{repo}/schedules` | The repository's schedules: name, cron, branch, `next_run_at`, `last_run_at`, `last_job_id` |
| `PUT /api/repos/{owner}/{repo}/schedules/{name}` | Creates or replaces a schedule with `{"cron":"0 3 * * *","branch":"main"}`. Returns the schedule with its next run time. |
| `DELETE /api/repos/{owner}/{repo}/schedules/{name}` | `204`, or `404` if it does not exist |

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"branch":"main"}' http://localhost:8080/api/repos/sidumandr/cisid/builds
```

```bash
curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"cron":"CRON_TZ=Europe/Istanbul 0 3 * * *"}' http://localhost:8080/api/repos/sidumandr/cisid/schedules/nightly
```

- **Cron:** The standard five-field format (`minute hour day month day-of-week`, e.g. `0 3 * * 1-5`) or `@hourly`, `@daily`, `@weekly`, `@monthly`. Times are in UTC; for another time zone, add `CRON_TZ=Europe/Istanbul ` at the start. A schedule name can contain lowercase letters, digits, `.`, `_` and `-`.
- **Runs:** Every 30 seconds the server picks up the schedules that are due and opens a job for the latest commit of the branch. Several server replicas do not run the same schedule twice: the schedule is locked in the database and given to a single replica. Runs missed while the server was down happen once when it comes back up, not once for each missed time. If the forge cannot be reached, that run is skipped and the schedule continues at its next time.
- **Jobs:** `event` is `manual` for manually started jobs and `schedule` for scheduled ones. You can write steps just for these with `when.event`. Secrets limited to branches are given to these jobs as they are on a push. Commit statuses appear under separate contexts: `cisid/manual`, `cisid/schedule` ([commit-status.md](commit-status.md)).

## Cancel and rerun

These endpoints require the `maintainer` role: `CISID_API_TOKEN`, a maintainer's personal token (`Authorization: Bearer`), or a maintainer's session. They return `403` if there is neither an API token nor any user, `401` if credentials are missing or wrong, and `403` if the role is not enough.

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/jobs/42/cancel
```

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/jobs/42/rerun
```

**Cancel:**

| Job status | Result |
|---|---|
| `pending` | Becomes `canceled` at once, response `200` |
| `running` | The cancel request is recorded, response `202`. The server the agent is connected to sends `job_cancel` to the agent (within 2 s), the agent stops the container, and the job becomes `canceled`. |
| Finished | `409` |

In both cases the job's `error` field becomes `canceled by request`. This works whichever server receives the request. If the agent never connects again, a job with a cancel request is not requeued when its lease expires; it becomes `canceled`.

**Rerun:** Creates a new `pending` job for the same repository, branch and commit. The response is `201`, and the `Location` header points to the new job.

## Live stream

```bash
curl -N http://localhost:8080/api/jobs/42/logs/stream
```

```
retry: 3000

event: status
data: {"id":42,"status":"running",...}

id: 1
event: log
data: {"job_id":42,"step":0,"stream":"stderr","seq":1,"data":"+ git init -q .\n"}

: keepalive

event: status
data: {"id":42,"status":"success",...}
```

- The first event is a `status` event with the job's current state. Then come the logs stored so far, then new logs.
- When the job finishes, the server sends a final `status` and closes the stream. For a finished job, the stream replays all the logs and closes right away.
- The `id` of each `log` event is its `seq` value. If the connection drops, the browser's `EventSource` resumes where it left off with the `Last-Event-ID` header. To resume manually, use `?after=<seq>`.
- The stream sends a `: keepalive` comment every 5 s, so proxies do not close the idle connection.
- When the server shuts down, it closes open streams. So they do not hold up the shutdown.

```js
const es = new EventSource("/api/jobs/42/logs/stream");
es.addEventListener("log", (e) => append(JSON.parse(e.data)));
es.addEventListener("status", (e) => {
  const job = JSON.parse(e.data);
  if (["success", "failed", "canceled"].includes(job.status)) es.close();
});
```

## How is it live?

When a log chunk is written and when a job finishes, the database sends the job number on the `cisid_job_events` channel with `NOTIFY` (`internal/db`). Each server runs `LISTEN` on this channel over a single connection (`internal/events`) and wakes the open streams of that job. A stream that wakes up reads the new logs from the database. As a result:

- Logs stream live even when the agent is connected to server A and the client to server B.
- If the notification listener disconnects, it reconnects and wakes all streams. Streams also check on their own every 5 s. Even if a notification is missed, no log is lost; at worst it is late.
- If the same log chunk arrives twice (a resend under acknowledged delivery), only the first one is written and only the first one is notified.

## Why not WebSocket?

The stream is one-way, from the server to the client. SSE does this over plain HTTP: you can watch it with `curl`, it needs no extra library in the browser, and it resumes a dropped connection automatically with `Last-Event-ID`. WebSocket is used for two-way channels such as agent ↔ server.
