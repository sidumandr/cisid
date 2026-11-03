# Webhooks

**English** · [Türkçe](tr/webhook.md)

`internal/api/webhook.go` receives webhooks from forges. For every branch push and every pull request change, it inserts a row with the `pending` status into the `jobs` table. Each forge has its own URL: `/webhooks/github`, `/webhooks/gitea`, `/webhooks/forgejo`, `/webhooks/gitlab`, `/webhooks/bitbucket`. A forge's URL is enabled when its webhook secret is set.

This document covers GitHub; setup and differences for the other forges are in [forges.md](forges.md). Deduplication, the fork rule, PR cancellation and the responses are the same for all of them.

## Setup on GitHub

Repository → **Settings → Webhooks → Add webhook**:

| Field | Value |
|---|---|
| Payload URL | `https://<server>/webhooks/github` |
| Content type | `application/json` |
| Secret | A long, random value (e.g. `openssl rand -hex 32`) |
| Events | **Let me select individual events** → *Pushes* and *Pull requests* |

The server must get the same secret. If the secret is empty, `NewWebhookHandler` returns `ErrEmptySecret` and no handler is created.

## Request handling

1. Only `POST` is accepted.
2. The body can be at most 25 MB (GitHub's limit).
3. The `X-Hub-Signature-256` header must be in the `sha256=<hex>` format and must equal the HMAC-SHA256 of the body with the secret. The comparison is constant-time (`hmac.Equal`).
4. Depending on the `X-GitHub-Event` header:
   - `ping` → `200 {"status":"pong"}`
   - `push` → see below
   - `pull_request` → [Pull requests](#pull-requests)
   - others → `200 {"status":"ignored"}`
5. Fields taken from the push payload:
   - `repository.full_name` → `jobs.repo`
   - `repository.clone_url` → `jobs.clone_url`
   - `ref` (with the `refs/heads/` prefix stripped) → `jobs.branch`
   - `after` → `jobs.commit_sha` (must be 40 or 64 hex characters)

## Pull requests

In a `pull_request` event only these actions create a job: `opened`, `reopened` and `synchronize`, which brings new commits. Other actions (such as `labeled` and `edited`) are ignored.

- The commit that is built is the PR's latest commit (`pull_request.head.sha`). The commit is fetched from the main repository. GitHub serves every PR's commits in the main repository too, so the fork's URL is never used.
- `jobs.branch` is the PR's own branch (`head.ref`), `target_branch` is the branch it merges into (`base.ref`), and `pull_request` is the PR number.
- When a new commit arrives on a PR, the unfinished older jobs of the same PR are canceled: pending jobs immediately, running jobs by their agents, which stop them.
- When a PR is closed (merges included), its unfinished jobs are canceled.
- **Forks:** PRs from another repository are not built by default, because building them runs outside code on the agents. Enable them with `CISID_BUILD_FORKS=true`. Even then, fork PRs get no secrets ([secrets.md](secrets.md)).
- If `pull_request.updated_at` is older than 7 days, the event is ignored (`stale delivery`).

## Replay protection

- A push payload creates a job only once. The deduplication key is **the SHA-256 hash of the signed body**, not the `X-GitHub-Delivery` header. That header is not part of the signature, so an attacker can change it, but an attacker cannot change the body without breaking the signature. The record and the job are created in the same transaction, so resends through GitHub's "Redeliver" are also safe.
- If `repository.pushed_at` is older than 7 days, the payload is ignored (`stale delivery`). Deduplication records are kept for 8 days and then deleted. This way a captured old payload can never create a job a second time.

## Responses

| Case | Code | Body |
|---|---|---|
| Job queued | `202` | `{"status":"queued","job_id":42}` (if more than one job was opened, also `"job_ids":[42,43]`) |
| Tag push | `200` | `{"status":"ignored","reason":"not a branch push"}` |
| Branch deleted | `200` | `{"status":"ignored","reason":"branch deleted"}` |
| Same payload already processed | `200` | `{"status":"ignored","reason":"duplicate delivery"}` |
| `pushed_at` older than 7 days | `200` | `{"status":"ignored","reason":"stale delivery"}` |
| PR action that does not need a build | `200` | `{"status":"ignored","reason":"pull request action \"labeled\" does not build"}` |
| PR from a fork, `CISID_BUILD_FORKS` off | `200` | `{"status":"ignored","reason":"pull request from a fork (CISID_BUILD_FORKS is off)"}` |
| PR closed | `200` | `{"status":"canceled","reason":"pull request closed","canceled":[41,42]}` |
| Rate limit exceeded | `429` | `{"status":"error","reason":"too many requests"}`, `Retry-After: 1` |
| Ping | `200` | `{"status":"pong"}` |
| Unsupported event | `200` | `{"status":"ignored","reason":"unsupported event \"issues\""}` |
| Signature missing or wrong | `401` | `{"status":"error","reason":"invalid signature"}` |
| Malformed or missing payload | `400` | `{"status":"error","reason":"invalid push payload"}` (`invalid pull request payload` for PRs) |
| Body larger than 25 MB | `413` | `{"status":"error","reason":"payload too large"}` |
| Method other than `POST` | `405` | `{"status":"error","reason":"method not allowed"}` |
| Database error | `500` | `{"status":"error","reason":"could not queue job"}` |
| The forge's API is unreachable (Bitbucket PRs only) | `503` | `{"status":"error","reason":"bitbucket is unavailable, redeliver later"}` |

## Trying it by hand

```bash
SECRET=s3cr3t-webhook-key
BODY=internal/api/testdata/push.json
SIG="sha256=$(openssl dgst -sha256 -hmac "$SECRET" < "$BODY" | awk '{print $NF}')"

curl -i -X POST http://localhost:8080/webhooks/github \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: push" \
  -H "X-Hub-Signature-256: $SIG" \
  --data-binary @"$BODY"
```

To run the server locally, see [server.md](server.md).

## Tests

`internal/api/webhook_test.go` uses realistic GitHub payloads from `internal/api/testdata/`:

| File | Scenario |
|---|---|
| `push.json` | Push to the `main` branch |
| `push_nested_branch.json` | A branch that contains `/`, such as `feature/webhook` |
| `push_branch_deleted.json` | Branch deletion (`deleted: true`) |
| `push_tag.json` | Tag push (`refs/tags/...`) |
| `ping.json` | The ping sent when the webhook is first added |

The database is mocked with `testify/mock`. The `internal/db` test requires a real PostgreSQL:

```bash
CISID_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/cisid_test?sslmode=disable go test ./internal/db/...
```
