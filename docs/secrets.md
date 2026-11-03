# Secrets

**English** · [Türkçe](tr/secrets.md)

Secret values such as a deploy token are stored per repository and given as environment variables only to the steps that ask for them (`internal/secrets`).

## Setup

| Variable | Description |
|---|---|
| `CISID_SECRET_KEY` | 32-byte AES-256 key, base64. Generate it with: `openssl rand -base64 32`. If unset, secrets are turned off. |
| `CISID_API_TOKEN` or a maintainer | The secrets API requires the `maintainer` role ([accounts.md](accounts.md)). |

> If the key is lost, the stored secrets cannot be decrypted and must be entered again. Keep the key somewhere separate from the database.

## Adding and deleting secrets

```bash
curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"value":"...","branches":["main"]}' http://localhost:8080/api/repos/sidumandr/cisid/secrets/DEPLOY_TOKEN
```

```bash
curl -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/repos/sidumandr/cisid/secrets
```

```bash
curl -X DELETE -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/repos/sidumandr/cisid/secrets/DEPLOY_TOKEN
```

| Endpoint | Response |
|---|---|
| `PUT …/secrets/{name}` | `204`. Overwrites the existing value and branch list. `branches` is optional. |
| `GET …/secrets` | `200 [{"name":"DEPLOY_TOKEN","updated_at":"…","branches":["main"]}]`. Values are **never** returned. |
| `DELETE …/secrets/{name}` | `204`, or `404` if it does not exist |

Rules:

- The name must be an environment variable name: letters, digits and `_`, not starting with a digit, at most 100 characters. The variables cisid gives to steps itself (`CI`, `CISID`, `CISID_JOB_ID`, `CISID_STEP`, `CISID_REPO`, `CISID_BRANCH`, `CISID_COMMIT_SHA`, `CISID_CLONE_URL`) cannot be used.
- The value can be at most 64 KB.
- `branches` takes at most 20 patterns. The patterns use the same syntax as `when.branch` (`main`, `release/*`).

## Limiting a secret to branches

A secret with `branches` goes only to push builds on those branches and never to pull requests. The server makes this decision.

`when.branch`, on the other hand, is not a security boundary. `.cisid.yml` is read from the commit being built, so anyone who can push to the repository can remove the condition on their own branch. Limit sensitive secrets such as a deploy token to protected branches with `branches`:

| Job | No `branches` | `branches: ["main"]` |
|---|---|---|
| Push to `main` | given | given |
| Push to `feature/x` | given | not given |
| Pull request opened from the repository | given | not given |
| Pull request from a fork | not given | not given |

A step that does not get the secret still runs; only that environment variable is not set.

## Usage

```yaml
steps:
  - name: deploy
    image: alpine:3.22
    commands:
      - ./deploy.sh
    secrets: [DEPLOY_TOKEN]
    when:
      branch: main
```

- The step sees the `DEPLOY_TOKEN` environment variable. Other steps do not.
- A secret cannot also be defined in the same step's `environment` field.
- If a requested secret does not exist in the repository, the job becomes `failed` before it starts: `secret not found: DEPLOY_TOKEN is not set for sidumandr/cisid`.
- The secrets of steps skipped by `when` are not read.

## Masking in logs

The agent replaces secret values with `***` before it sends the step's output to the server. The values never reach the logs in the database.

- A value is masked even when it is split across two chunks of output. If the end of a chunk could be the start of a secret, that part is held back until the next chunk.
- Each line of a multi-line value (e.g. a private key) is also masked on its own.
- Values shorter than 3 characters are not masked, because masking them would make the log unreadable.

Masking is a safety net, not a boundary. If a step writes the value in a transformed form (base64, reversed), the mask cannot catch it.

## Security model

- **Storage:** Values are encrypted with AES-256-GCM, with a random nonce for each value. The ciphertext is bound to its `repo/name`. A value that is moved to another row in the database or modified cannot be decrypted.
- **On the server:** Secret values and the API token are not written to the server logs.
- **Which jobs see secrets:** Push builds and pull requests opened from a branch in the repository receive the secrets their steps ask for. Secrets limited with `branches` are given only to pushes on those branches. Pull requests from forks (`CISID_BUILD_FORKS`) get no secrets at all; steps that ask for secrets run without those variables.
- **Transport:** Secrets travel to the agent over WebSocket. Use TLS (`wss://`) between the agent and the server. Agents authenticate with a token ([agent.md](agent.md#authentication-and-tls)).
