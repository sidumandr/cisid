# Security

**English** · [Türkçe](tr/security.md)

This document collects cisid's security measures in one place. The details are in the related documents.

## Trust boundaries

| Party | Trust | How it is verified |
|---|---|---|
| GitHub, Gitea, Forgejo, Bitbucket (webhook) | A sender that knows the secret | HMAC-SHA256 signature of the body |
| GitLab (webhook) | A sender that knows the secret | `X-Gitlab-Token` header, constant-time comparison. GitLab does not sign: the URL must be HTTPS. |
| Agent | A process that knows the agent token | `Authorization: Bearer`, optional TLS |
| API client (write) | Someone who knows the API token, or a user with the maintainer or admin role | `Authorization: Bearer` (server or personal token), or the session cookie and `X-Cisid-Request` |
| API client (read), web UI | Signed-in users and token holders if `CISID_READ_ACCESS=signed-in`, everyone if `public` | `Authorization: Bearer` or a cookie |
| Code in the repository (`.cisid.yml` steps) | Anyone with push access | Container boundaries, but it can reach the agent machine |

Someone who can push to a repository can read the secrets defined for that repository and run code on the agent machine. cisid does not prevent this; it limits the impact.

## Webhooks

- The signature is compared in constant time (`hmac.Equal`): `X-Hub-Signature-256` on GitHub, `X-Gitea-Signature` on Gitea, `X-Forgejo-Signature` on Forgejo, `X-Hub-Signature` on Bitbucket. GitLab sends the secret in plain text in the `X-Gitlab-Token` header. A forge's webhook URL is enabled only when its secret is set.
- Only GitHub pushes have `repository.pushed_at`. Pushes from other forges do not get the 7-day replay window; they get the deduplication of the signed body.
- At most 25 MB of the body is read.
- The same signed body creates a job only once. The deduplication key is the SHA-256 hash of the body, and the record is written in the same transaction as the job. If `repository.pushed_at` is older than 7 days, the request is rejected. Records are deleted after 8 days ([webhook.md](webhook.md#replay-protection)).
- All repositories use a single webhook secret.
- Pull requests are built too. Pull requests from forks are not built by default; with `CISID_BUILD_FORKS=true` they are built but get no secrets ([webhook.md](webhook.md#pull-requests)).

## Agent connection

- If `CISID_AGENT_TOKEN` is set, an agent with no token or a wrong token gets `401`. The comparison is constant-time. The token must be at least 16 characters. If it is not set, the server logs a warning and accepts every agent.
- The server serves TLS 1.2+ with `CISID_TLS_CERT_FILE` and `CISID_TLS_KEY_FILE`. The agent connects with `wss://`; for a private CA, use `CISID_SERVER_CA_FILE`. The agent warns if it sends the token to a non-local address over `ws://`.
- All agents share one token; there is no per-agent identity. `CISID_AGENT_TOKEN` takes several comma-separated tokens. This way you can rotate the token without disconnecting agents, and revoke the old one by removing it from the list ([agent.md](agent.md#authentication-and-tls)).

## API and web UI

- Endpoints that make changes (build, cancel, rerun, schedules, polling, notifications, secrets) require `maintainer`, and user management requires `admin` ([accounts.md](accounts.md)). If there is neither a `CISID_API_TOKEN` nor any user, these endpoints are closed (`403`).
- Passwords are stored with PBKDF2-HMAC-SHA256 (600,000 iterations, salted); sessions and personal tokens only as SHA-256 hashes. An unknown user and a wrong password get the same response in the same time.
- Signing in through a forge (OAuth) is bound by `state`, the return address can only be a path on the site, and the forge's access token is not stored. Accounts match on the forge's immutable user ID. Do not enable `CISID_OAUTH_SIGNUP_ROLE` on public forges ([accounts.md](accounts.md#signing-in-through-a-forge)).
- The session cookie is HttpOnly and `SameSite=Lax`. Changes that arrive with a cookie also require the `X-Cisid-Request` header (CSRF).
- With `CISID_READ_ACCESS=signed-in` (the default when `CISID_READ_TOKEN` is set), the read endpoints (`/api/jobs`, logs, the live stream) and `/metrics` require a sign-in or a token; with `public`, anyone can read them. The job list shows repository names, commits and logs. A browser that signs in with the read token gets an HttpOnly, `SameSite=Strict` cookie through `POST /api/login`; the cookie carries a hash of the token and allows only reading ([api.md](api.md#read-access)). `/healthz` and `/readyz` are always open.
- `/webhooks/` and `/api/` are rate-limited per IP with a token bucket (`CISID_RATE_LIMIT`, default 10 requests/s). Behind a reverse proxy, `CISID_TRUST_PROXY=true` takes the client address from `X-Forwarded-For`. Do not enable this setting without a proxy, because the header can be forged.
- The web UI is served with `Content-Security-Policy: default-src 'self'` and has no inline scripts. Every value from the API is inserted with `textContent`. The browser keeps the API token only in `sessionStorage` ([ui.md](ui.md#security)).

## Secrets

- Secrets are encrypted with AES-256-GCM. The ciphertext is bound to `repo/name` (AAD), so a value copied to another row cannot be decrypted. The key is `CISID_SECRET_KEY`, and it is not stored in the database.
- The API never returns secret values; it only lists their names.
- Values are given as environment variables only to steps that request them with `secrets:`. On the server side, `branches` can limit a secret to protected branches; then pull requests do not get it either ([secrets.md](secrets.md#limiting-a-secret-to-branches)). `when.branch` is not a security boundary, because `.cisid.yml` is read from the commit being built.
- The agent masks secret values in step output with `***`, including values split across chunks. Values shorter than 3 characters are not masked. Masking is not a security boundary: it does not stop a step that transforms the value (e.g. base64) and prints it ([secrets.md](secrets.md)).

## Notifications

- Where notifications go (a chat's webhook URL, which is a credential, or email addresses) is encrypted like secrets, bound to the repository and the notification's name, and never returned; the API shows only the URL's host or the addresses ([notifications.md](notifications.md)).
- A notification's URL is chosen by a maintainer, so the server would send requests where a maintainer says. To keep that from reaching what only the server can reach, chats and webhooks are not posted to link-local, multicast or known cloud metadata addresses, and redirects are not followed. Private networks are allowed; if the server can reach internal services a maintainer must not, block them at the network level (e.g. an egress NetworkPolicy).
- Errors and logs name only the URL's host. Webhook bodies can be signed (`X-Cisid-Signature`). SMTP uses STARTTLS by default and refuses a relay that does not offer it; the password never goes over an unencrypted connection except to `localhost`.
- Badges show whether a branch's latest build passed. They follow read access unless `CISID_PUBLIC_BADGES=true`.

## Clone tokens

- The forge's token for cloning private repositories is given as a secret only to the clone step. It is masked in output, it is not written to `.git/config` (git reads it from a helper defined through `GIT_CONFIG_*` environment variables), and user steps do not get it.
- Create the token with the narrowest permissions possible: read and commit status. A forge's token is used for all repositories on that forge.

## Step containers

- Each step and service runs in its own container with `no-new-privileges`. `--privileged` is not used.
- Memory (including swap), CPU and the number of processes can be limited. The process limit is 4096 by default. Running out of memory is reported as `out of memory` ([runner.md](runner.md)).
- Jobs with services run in their own Docker network. The workspace volume, the network and the containers are deleted when the job ends.
- Cache volumes are per repository and persist between jobs. Someone who can push to the repository can leave files in the cache; fork PRs can only read the cache ([pipeline.md](pipeline.md#cache)).
- The clone step gets the repository URL and the commit as environment variables; it does not embed them in a shell command.
- The agent runs with full access to the host's Docker daemon. This is equivalent to root: even though a malicious step cannot reach the Docker API, a container escape or a kernel vulnerability affects the host. Run agents on isolated machines or VMs dedicated to CI ([deployment.md](deployment.md#agents-and-the-docker-socket)).
- On Kubernetes each agent pod runs a privileged Docker-in-Docker sidecar. Docker listens only on a Unix socket shared inside the pod. The agent container itself runs with a read-only file system and all capabilities dropped. The chart installs a NetworkPolicy that lets only the server connect to the bundled PostgreSQL ([Helm chart](../deployments/helm/cisid/README.md)).

## Database and logs

- SQL text is never concatenated with request data; all values are sent as query parameters.
- Secret values, tokens and the database password are not written to the server and agent logs. In the configuration logged at startup, the password in the database URL is hidden as `xxxxx`, and for tokens only whether they are set is logged.
- Job logs are stored in the database as plain text (in their masked form).
- Traces carry the repository, branch, commit, step name and image. Secret values, environment variables and step output are not written to spans. User info in the collector address is hidden in logs ([tracing.md](tracing.md)).

## Production checklist

- [ ] Long, random values for `CISID_GITHUB_WEBHOOK_SECRET`, `CISID_AGENT_TOKEN` and `CISID_API_TOKEN`.
- [ ] The server runs with TLS or behind a reverse proxy that terminates TLS. Agents connect with `wss://`.
- [ ] `CISID_READ_ACCESS=signed-in` (or `CISID_READ_TOKEN` set), or the server is behind a reverse proxy with access control.
- [ ] If the first admin was created, `CISID_ADMIN_PASSWORD` is removed from the environment and the password is changed in the UI.
- [ ] `CISID_SECRET_KEY` is stored separately from database backups.
- [ ] Agents run on separate machines, with `CISID_STEP_MEMORY` and `CISID_STEP_CPUS` set.
- [ ] Sensitive secrets are limited to protected branches with `branches`.

## Known gaps

- Roles apply to the whole server; there are no per-repository permissions. A maintainer manages the builds and secrets of all repositories.
- There is no per-account lockout for sign-in attempts; a per-IP rate limit (`CISID_RATE_LIMIT`) applies.
- There is no per-agent identity. Changing the token requires restarting the server (the rotation list avoids downtime).
