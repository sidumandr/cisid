# Notifications and badges

**English** · [Türkçe](tr/notifications.md)

cisid can tell you how builds went without anyone watching the UI: a message in a chat, a JSON request to a webhook or an email when a branch breaks or is fixed. A badge shows a branch's latest result in a README.

## Notifications

Each repository has its own notifications. A notification says where results go and which results:

| Setting | Values |
|---|---|
| Name | Lowercase letters, digits, `.`, `_` and `-`, up to 63 characters. Saving a name again replaces it. |
| Kind | `slack`: a Slack incoming webhook, or a chat that accepts its messages (Mattermost, Rocket.Chat, Discord's webhook URL with `/slack` at the end). `webhook`: a JSON request to any URL. `email`: through the server's SMTP relay. |
| Target | The URL (`slack`, `webhook`), or 1 to 10 email addresses (`email`) |
| When (`on`) | Any of `failed` (every failed build), `broken` (a failure after a pass, or a first build that fails), `fixed` (a pass after a failure), `passed` (every passed build). Default: `failed` and `fixed`. |
| Branches | Glob patterns like `when.branch` (`main`, `release/*`); empty means every branch |
| Pull requests | Off by default. When on, pull request builds are announced too, matched by the branch they merge into. |
| Signing secret | `webhook` only, optional: the body is signed in `X-Cisid-Signature` |

Notifications need `CISID_SECRET_KEY` ([secrets.md](secrets.md)): a chat's webhook URL is a credential, so targets are stored encrypted with the same key as secrets and never shown again. The UI and the API show only the URL's host, or the email addresses.

### In the UI

On the repository page, the **Notifications** tab lists the notifications with when each was last sent and why the last attempt failed, if it did. **Add notification** opens a form; **Test** sends a test message at once. Managing notifications needs a maintainer.

### Through the API

| Path | Description |
|---|---|
| `GET /api/repos/{owner}/{repo}/notifications` | The notifications: `name`, `kind`, `target` (the host or the addresses), `on`, `branches`, `pull_requests`, `last_sent_at`, `last_error` |
| `PUT /api/repos/{owner}/{repo}/notifications/{name}` | Creates or replaces one. `204`, or `400` with the reason. |
| `DELETE /api/repos/{owner}/{repo}/notifications/{name}` | `204`, or `404` |
| `POST /api/repos/{owner}/{repo}/notifications/{name}/test` | Sends a test message: `204` once it went through, `502` with the reason if not |

Every endpoint needs a maintainer. Without `CISID_SECRET_KEY` they answer `403`.

```bash
curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" \
  -d '{"kind":"slack","url":"https://hooks.slack.com/services/T000/B000/XXXX","on":["broken","fixed"],"branches":["main"]}' \
  http://localhost:8080/api/repos/sidumandr/cisid/notifications/team-chat

curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" \
  -d '{"kind":"email","to":["dev@example.com","ops@example.com"]}' \
  http://localhost:8080/api/repos/sidumandr/cisid/notifications/team-mail
```

The body of `PUT`: `kind`, `url` or `to`, and optionally `secret`, `on`, `branches`, `pull_requests`.

### Messages

A message names the repository, the branch (or the pull request) and what happened, with the reason of a failure, the build number, the commit and what started the build. With `CISID_PUBLIC_URL` set, it links to the job's page.

```
sidumandr/cisid: main is broken
Step "test" failed with exit code 1 after 1m48s.
Build #42 · commit 6113728 · push
```

The headline is the most telling result: `is broken` and `is fixed` once the build before is known, `failed again` for a failure after a failure, `failed` and `passed` otherwise. Matrix combinations are compared with the same combination's build before.

### Webhooks

A `webhook` notification posts JSON:

```json
{
  "result": "broken",
  "repo": "sidumandr/cisid",
  "title": "sidumandr/cisid: main is broken",
  "detail": "Step \"test\" failed with exit code 1 after 1m48s.",
  "url": "https://ci.example.com/jobs/42",
  "job": { "id": 42, "repo": "sidumandr/cisid", "branch": "main", "status": "failed", "...": "..." }
}
```

`job` is the job as the job API returns it ([api.md](api.md#job)); a test has `"result": "test"` and no `job`. The headers carry `X-Cisid-Result` (the result) and, with a signing secret, `X-Cisid-Signature: sha256=<hex>`: the HMAC-SHA256 of the body under the secret, as GitHub signs its webhooks. Check it before trusting the body.

### Email

Email goes through an SMTP relay the server is configured with:

| Variable | Default | Description |
|---|---|---|
| `CISID_SMTP_HOST` | none | The relay. Without it, email notifications cannot be saved. |
| `CISID_SMTP_PORT` | `587`, `465` with `tls` | |
| `CISID_SMTP_TLS` | `starttls` | `starttls`: upgrade with STARTTLS, and refuse a relay that does not offer it. `tls`: TLS from the start (port 465). `none`: no encryption, only for a relay on the same host or a trusted network. |
| `CISID_SMTP_USERNAME`, `CISID_SMTP_PASSWORD` | none | Set together. The password is never sent over an unencrypted connection except to `localhost`. |
| `CISID_SMTP_FROM` | none, required with a host | The sender, e.g. `cisid <ci@example.com>` |

Messages are plain text in UTF-8, marked `Auto-Submitted: auto-generated` so auto-replies leave them alone.

### Delivery

- When a job finishes, one server takes its result and announces it once, even when several servers share the database. Canceled builds are not announced, nor results older than a day (a server that was down does not announce yesterday when it comes back).
- A send that may succeed later (a network error, `429`, `5xx`, a temporary SMTP error) is tried twice more, 2 and 10 seconds later. Then the error is recorded on the notification and shown in the UI; the next result is sent as usual. A send is not retried after a server restart: a result is announced at most once.
- The `cisid_notifications_total` metric counts the sends by kind and outcome ([metrics.md](metrics.md)).
- Chats and webhooks are not posted to link-local addresses (such as the cloud metadata service `169.254.169.254`), multicast addresses or known metadata addresses, and redirects are not followed. Private networks are allowed, since chats are often self-hosted. Errors name only the URL's host; the rest of the URL is never logged.

## Badges

```markdown
[![cisid](https://ci.example.com/api/repos/sidumandr/cisid/badge.svg)](https://ci.example.com/repos/sidumandr/cisid)
```

`GET /api/repos/{owner}/{repo}/badge.svg` draws the latest result of the repository's default branch as an SVG: **passing**, **failing**, or **unknown** when the branch has no finished build yet. `?branch=release/1.0` shows another branch. The **Badge** button on the repository page gives the Markdown to copy.

- A build counts when it passed or failed; canceled and running builds and pull requests do not. A matrix build fails when any of its combinations failed; a rerun of a combination replaces its earlier result.
- The default branch comes from the forge and is remembered for 10 minutes. Repositories cisid never built show `unknown` without asking the forge.
- Badges are read like jobs: when reading needs a sign-in (`CISID_READ_TOKEN` or `CISID_READ_ACCESS=signed-in`), a README on GitHub cannot show them. `CISID_PUBLIC_BADGES=true` lets anyone see badges while everything else still needs a sign-in. A badge shows only whether a branch's latest build passed.
- Responses carry `Cache-Control: no-cache` and an `ETag`, so image proxies such as GitHub's show the latest result.
