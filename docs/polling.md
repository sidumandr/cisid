# Polling: push builds without webhooks

**English** · [Türkçe](tr/polling.md)

If a forge's webhook cannot reach the server (e.g. a server on an office network, behind NAT or a firewall), cisid can poll branches itself. The server checks the latest commit of every watched branch at regular intervals; if the commit has changed, it opens a **push** build. You do not need to open a port, set up a tunnel or define a webhook on the forge: the server always opens the connection.

Polling works with all forges ([forges.md](forges.md)). You can also use it together with webhooks.

## Setup

| Variable | Default | Description |
|---|---|---|
| `CISID_POLL_INTERVAL` | `1m` | How often each branch is polled. At least `10s`. `0` disables polling. |

To watch a branch, use the **Poll a branch** button on the **Polling** tab of the repository page in the UI, or call the API (requires a maintainer: a sign-in or a token):

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"branch":"main"}' \
  http://localhost:8080/api/repos/gitea:office/app/watches
```

If cisid has never built the repository, the repository does not appear in the UI. Add the first branch through the API, or open the repository's URL directly: `/repos/gitea:office/app`.

| Endpoint | Description |
|---|---|
| `GET /api/repos/{owner}/{repo}/watches` | Watched branches: `branch`, `last_sha`, `last_poll_at`, `last_job_id`, `last_error` (requires read access) |
| `POST /api/repos/{owner}/{repo}/watches` | Starts watching the branch with `{"branch":"main"}`; without `branch`, the default branch. `201` if new, `200` if already watched. `404` if the repository or branch does not exist, `409` if polling is disabled. |
| `DELETE /api/repos/{owner}/{repo}/watches/{branch}` | Stops watching: `204`, or `404` if the branch is not watched. Write branch names that contain `/` as they are: `.../watches/release/1.0`. |

## How it works

- The **first poll** builds the branch's current latest commit, so you see right away that watching works. Later polls open a build only when a new commit arrives.
- The builds are **push** builds: `CISID_EVENT=push`, commit status context `cisid`, and `when.event: [push]` steps run. If more than one commit was pushed between polls, only the last one is built.
- Each poll sends a single request to the forge (the branch's latest commit). Every 5 seconds the server picks up the branches that are due.
- If **several server replicas** share the same database, only one replica does each poll, and the same commit is never built twice: the branch's last seen commit is updated in the transaction that opens the build.
- If a poll fails (the forge is unreachable, the token is invalid, the branch was deleted), the error is written to `last_error`, shows up in the UI and is retried at the next poll. A successful poll clears the error.
- Polling watches only branches. **Pull requests** require a webhook.

## Rate limits

Each watched branch means one request per interval. With a 1-minute interval, one branch makes 60 requests an hour.

| Forge | Without a token | With a token |
|---|---|---|
| GitHub | 60 an hour per IP: barely enough for a single branch | 5000 an hour: about 80 branches at a 1-minute interval |
| GitLab.com, Bitbucket Cloud | Low limits | Plenty |
| Gitea, Forgejo, your own GitLab | Depends on the server's settings | Depends on the server's settings |

If you watch many branches, set the forge's token or increase `CISID_POLL_INTERVAL`.
