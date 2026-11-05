# Running cisid for a team

**English** · [Türkçe](tr/team-server.md)

One Linux server inside the company network runs everything: PostgreSQL, the cisid server, an agent that runs the builds in Docker, and Caddy in front of them for HTTPS. Teammates open it from their own machines over the office network or the VPN, sign up, connect their GitHub or GitLab account and then their repositories. From then on cisid asks the forges for new commits and builds each one on the server.

Nothing has to be reachable from the internet: the server only calls out to the forges. That is also why it polls instead of waiting for webhooks.

```
teammate's browser ──https──▶ Caddy ──▶ cisid-server ──▶ PostgreSQL
                                          ▲     │
                                   agent ─┘     └──▶ GitHub, GitLab (polls, clones, commit statuses)
                                     │
                               Docker on the host (the build steps)
```

The files are in [`deployments/team/`](../deployments/team): `compose.yaml`, the `Caddyfile` and `env.example`.

## What you need

- A Linux machine with Docker Engine and the Compose plugin, used only for CI if you can: the agent starts the build containers on its Docker, which is root-equivalent access to it ([deployment.md](deployment.md#agents-and-the-docker-socket)). 4 CPUs and 8 GB of memory is a comfortable start.
- A name for it in the company DNS, such as `ci.company.internal`. The examples below use this name.
- Outbound HTTPS to the forges (`github.com`, `api.github.com`, `gitlab.com` or the company's GitLab) and to the image registries the pipelines pull from.

## 1. Get the code

```bash
git clone https://github.com/sidumandr/cisid.git
```

```bash
cd cisid
```

The images are built from this checkout. To stay on a release, `git checkout v0.x.y`.

## 2. Register the OAuth apps

Teammates sign in and connect their accounts through these. The callback URL only has to open in the teammates' browsers, so a name inside the network works.

| Forge | Where | Settings |
|---|---|---|
| GitHub | Settings → Developer settings → **OAuth Apps** → New OAuth App; or the organization's settings, so that the company owns it | Homepage URL `https://ci.company.internal`, callback URL `https://ci.company.internal/auth/github/callback` |
| GitLab | User settings → **Applications**; or the group's, or the admin area on the company's own GitLab | Redirect URI `https://ci.company.internal/auth/gitlab/callback`, *Confidential* checked, scopes `api` and `read_user` |

Skip a forge you do not use. Without its OAuth app, teammates can still connect it with a token of their own; Gitea and Forgejo connect only that way (with `CISID_GITEA_URL` or `CISID_FORGEJO_URL` set, [forges.md](forges.md)).

If the repositories belong to a GitHub organization that restricts OAuth apps, an owner of the organization approves the app once (Organization settings → Third-party access), or the app is the organization's own.

## 3. Fill in the settings

```bash
cp deployments/team/env.example deployments/team/.env
```

```bash
chmod 600 deployments/team/.env
```

Then fill in `deployments/team/.env`. The important ones:

| Setting | |
|---|---|
| `CISID_HOST`, `CISID_PUBLIC_URL` | The name teammates open, and the same as a URL. The forges send the browser back to `<CISID_PUBLIC_URL>/auth/<forge>/callback`. |
| `CISID_DB_PASSWORD`, `CISID_AGENT_TOKEN`, `CISID_API_TOKEN` | Long random values: `openssl rand -hex 32` |
| `CISID_SECRET_KEY` | `openssl rand -base64 32`. It seals the repositories' secrets and the teammates' forge tokens. Keep a copy apart from the database backups (in a password manager): without it neither can be read again. |
| `CISID_SIGNUP_ROLE=maintainer` | Anyone who reaches the server may open an account as a maintainer, who connects repositories and runs builds. |
| `CISID_OAUTH_SIGNUP_ROLE=maintainer` | The same for **Continue with GitHub** and **Continue with GitLab**. |
| `CISID_POLL_INTERVAL=1m` | How often cisid asks the forges for new commits ([polling.md](polling.md)) |
| `CISID_*_OAUTH_CLIENT_ID`, `..._SECRET` | The OAuth apps from step 2 |
| `CISID_STEP_MEMORY`, `CISID_STEP_CPUS` | Limits of each build container |
| `CISID_ALLOW_FROM` | The addresses that reach cisid at all, separated by spaces, such as the office's public address or the VPN's range; everyone else gets `403`. Empty lets everyone in. |
| `CISID_PROXY_HTTPS_PORT`, `CISID_PROXY_HTTP_PORT` | Caddy's ports on the server, `443` and `80` unless set |

Open sign-up is only safe because nobody outside the company network reaches the server. If it ever becomes reachable from outside, empty both sign-up roles first.

**A server on the internet.** If the machine has a public address and no VPN, let only the office in: `CISID_ALLOW_FROM=<the office's public address>`. Caddy then answers everyone else with `403`, before anything reaches cisid. Docker publishes Caddy's ports past the host's firewall (ufw), so this setting is what keeps others out.

**Ports 80 and 443 taken.** If another web server already holds them, give Caddy other ports: `CISID_PROXY_HTTPS_PORT=9443` and `CISID_PROXY_HTTP_PORT=127.0.0.1:8089` (plain HTTP stays on the server), with the port in the URL: `CISID_PUBLIC_URL=https://ci.company.internal:9443`, and in the OAuth apps' callback URLs. Nothing of the other web server changes.

**No name in DNS yet.** Teammates can open the server by its address meanwhile. List the address after the name, `CISID_HOST=ci.company.internal, 203.0.113.7`, and set `CISID_DEFAULT_SNI=203.0.113.7`: a browser sends no name when it opens an address, and this tells Caddy which certificate to show. Put the address in `CISID_PUBLIC_URL` and add `https://203.0.113.7/auth/<forge>/callback` to the OAuth apps (both forges take several callback URLs). Once the name works, `CISID_PUBLIC_URL` goes back to it.

Every other server setting ([server.md](server.md)) can go into the same file: the server reads all of it.

## 4. Start

```bash
docker compose -f deployments/team/compose.yaml up -d --build --wait
```

The server applies its database migrations as it starts. Caddy listens on ports 80 and 443; the server, the agent and PostgreSQL are not published.

## 5. Trust the certificate

Let's Encrypt cannot reach a server inside the network, so Caddy signs the certificate with a CA of its own. Copy its root out of the container:

```bash
docker compose -f deployments/team/compose.yaml cp caddy:/data/caddy/pki/authorities/local/root.crt cisid-root.crt
```

Each teammate trusts it once, or the IT department hands it out with the other company certificates:

- Windows: open the file → **Install Certificate** → Local Machine → *Trusted Root Certification Authorities*.
- macOS: Keychain Access → System → drag the file in → open it → Trust → *Always Trust*.
- Linux: copy it to `/usr/local/share/ca-certificates/cisid-root.crt` and run `sudo update-ca-certificates`. Firefox keeps its own list: Settings → Certificates → Import.

If the company has its own CA, put its certificate and key in `deployments/team/certs/` as `cert.pem` and `key.pem`, change `tls internal` in the `Caddyfile` to `tls /certs/cert.pem /certs/key.pem`, and restart Caddy. Teammates then trust nothing new.

## 6. The first account

Open `https://ci.company.internal` and **Create an account**: the first account on a new server becomes its admin ([accounts.md](accounts.md#the-first-admin)). Do this before telling the team.

## 7. What each teammate does

1. Opens `https://ci.company.internal` and creates an account, or continues with GitHub or GitLab.
2. **Account** → *Forge accounts* → **Connect** next to GitHub or GitLab, and allows cisid on the forge. Or **Use a token**, to give a token of their own instead, such as a GitHub fine-grained token that reaches only some repositories.
3. **Repositories** → **Connect a repository**: picks one of their repositories (private ones too) and keeps *Poll the default branch*. cisid builds the head of the default branch, then every new commit on it.
4. The repository needs a `.cisid.yml` on its default branch ([pipeline.md](pipeline.md)); the dialog says so when it has none. More branches are polled from the repository's *Polled branches* tab.

Each repository is built with the access of the teammate who connected it: cisid clones it, reads its `.cisid.yml` and reports the result to the forge as a commit status with their token. The repository list shows whose access it is. If that teammate disconnects their forge account, the repository falls back to the server's token for the forge; with none set, private repositories stop building until someone connects them again.

## Running it

| Task | Command (from the checkout) |
|---|---|
| Logs | `docker compose -f deployments/team/compose.yaml logs -f server agent` |
| More builds at once | `docker compose -f deployments/team/compose.yaml up -d --scale agent=3`: an agent runs one job at a time |
| Update | `git pull`, then the start command from step 4 again |
| Back up the database | `docker compose -f deployments/team/compose.yaml exec -T postgres pg_dump -U cisid cisid \| gzip > cisid-$(date +%F).sql.gz` |
| Stop (the data stays) | `docker compose -f deployments/team/compose.yaml down` |

Run the backup daily from cron, and copy it off the machine. Keep the Caddy volume (`caddy-data`) too: without it Caddy makes a new root, and everyone has to trust it again.

Agents on other machines connect to `wss://ci.company.internal/agent/ws` with the same `CISID_AGENT_TOKEN`, and with `CISID_SERVER_CA_FILE` pointing at `cisid-root.crt` ([agent.md](agent.md)).

## Security in short

- Keep the server off the internet. Open sign-up, signing up through the forges and the agent token over the network are fine only inside it.
- Whoever can push a `.cisid.yml` to a connected repository runs code on the server, with the agent's access to Docker. Connect only the team's own repositories, and keep the CI machine apart from other services.
- The forge tokens are sealed with `CISID_SECRET_KEY` and never shown again. Connecting GitHub by OAuth gives cisid the `repo` scope, which reaches every repository the teammate can; whoever wants less connects a fine-grained token instead.
- If an OAuth client secret was ever shared in a chat or a ticket, make a new one on the forge and put it in `.env`.

More: [security.md](security.md).
