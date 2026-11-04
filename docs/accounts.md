# Accounts and roles

**English** · [Türkçe](tr/accounts.md)

cisid has user accounts. Every user has a **role**, and users sign in with a password, through a forge (GitHub, Gitea, Forgejo, GitLab, Bitbucket; [below](#signing-in-through-a-forge)) or with personal API tokens. The server's own tokens (`CISID_API_TOKEN`, `CISID_READ_TOKEN`) keep working as before; accounts are added alongside them.

## Roles

| Role | What it can do |
|---|---|
| `viewer` | Reads jobs, logs and metrics |
| `maintainer` | Also starts builds, cancels and reruns jobs; manages schedules, polled branches and secrets |
| `admin` | Also manages users |

Each role can do everything the roles before it can do.

## Who is recognized and how

| Method | Who | Note |
|---|---|---|
| `Authorization: Bearer <CISID_API_TOKEN>` | admin | The server's token; it does not belong to a user |
| `Authorization: Bearer <CISID_READ_TOKEN>` | viewer | |
| `Authorization: Bearer cisid_...` | The token's owner, with their own role | Personal token ([below](#personal-tokens)) |
| Session cookie (`cisid_session`) | The signed-in user | Issued by `POST /api/login` |
| Read cookie (`cisid_token`) | viewer | Issued when you sign in in the UI with the read or API token; read-only |

If a request carries a Bearer token that is not accepted, cookies are not checked. A request with no identity gets `401`; a request whose role is not enough gets `403`.

## The first admin

There are three ways to create the first account on a new server:

- **Sign up.** While a server has no users, the first person to sign up on the **Register** page (or with `POST /api/register`) becomes an admin. Until then anyone who reaches the server can be that person, so create the first account before the server is reachable from outside, or use one of the ways below.
- Set `CISID_ADMIN_PASSWORD`. At startup the server creates an admin named `CISID_ADMIN_USER` (default `admin`); if the user already exists, the server leaves it alone and does not change its password. Change the password in the UI afterwards.
- Or call the user API with `CISID_API_TOKEN`; the API token is an admin:

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" \
  -d '{"username":"ahmet","role":"admin","password":"at-least-10-characters"}' \
  http://localhost:8080/api/users
```

## Signing up

`POST /api/register` with `{"username":"...","password":"..."}` opens an account and signs the browser in (`201`, the user). The UI's **Register** page uses it.

- The first account on a server with no users is an admin, whatever else is set. Servers sharing the database agree on which account was first.
- After that, sign-up is closed (`403`) unless `CISID_SIGNUP_ROLE` is `viewer` or `maintainer`: then anyone who reaches the server can open an account with that role. Nobody signs up as an admin.
- Usernames are lowercased; the rules for names and passwords are those of the user API. A taken name gets `409`.
- `GET /api/me` tells the UI what to offer: `signup` is `first` while the server has no users, `open` when anyone can sign up, and absent otherwise; `provider_signup` says forge users without an account get one on their first sign-in through their forge (`CISID_OAUTH_SIGNUP_ROLE`).

With `CISID_READ_ACCESS=signed-in`, open sign-up as `viewer` lets anyone who can reach the server read its jobs and logs.

## Read access

| `CISID_READ_ACCESS` | Meaning |
|---|---|
| `signed-in` | Reading requires signing in or a token. The default: builds still run, from webhooks, polling and schedules, while nobody watches, but only people with an account see them. |
| `public` | Anyone can read jobs, logs and metrics. It cannot be combined with `CISID_READ_TOKEN`. |

Changes always require a maintainer. If there are no users and no `CISID_API_TOKEN`, endpoints that make changes return `403`, because nobody can be authorized.

## Sign-in and sessions

- The body of `POST /api/login` is `{"username":"...","password":"..."}` or `{"token":"..."}`. On success it returns `204` and a session cookie (HttpOnly, `Secure` over HTTPS, `SameSite=Lax`). Signing in with a personal token also opens a session; signing in with the server's tokens only gives a read cookie.
- A wrong password and a nonexistent user get the same response (`401 wrong username or password`) and take the same time.
- A session lasts `CISID_SESSION_TTL` (default `7d`). The server stores only the SHA-256 hash of the session. `POST /api/logout` deletes the session. The hourly cleanup deletes expired sessions.
- **CSRF:** A request identified by a cookie that changes something must send the `X-Cisid-Request` header, or it gets `403`. A form submitted from another site cannot add this header. The web UI sends it with every request; requests with a Bearer token do not need to send it.
- Passwords are at least 10 characters and are stored with PBKDF2-HMAC-SHA256 (600,000 iterations, salted). The API never returns hashes.
- `GET /api/me`: who is signed in (`user`, `role`, `via`), whether reading is open (`public_read`), whether accounts are enabled (`accounts`).

## Signing in through a forge

Users can sign in with their forge accounts (OAuth 2.0, authorization code flow). You register an OAuth app on each forge and give its client ID and secret to the server. The **Sign in** page shows a **Continue with ...** button for each forge, and the sign-in dialog a **Sign in with ...** button.

| Variable | Description |
|---|---|
| `CISID_GITHUB_OAUTH_CLIENT_ID`, `CISID_GITHUB_OAUTH_CLIENT_SECRET` | GitHub (or GitHub Enterprise Server, from `CISID_GITHUB_API_URL`) |
| `CISID_GITEA_OAUTH_CLIENT_ID`, `..._SECRET` | Gitea (requires `CISID_GITEA_URL`) |
| `CISID_FORGEJO_OAUTH_CLIENT_ID`, `..._SECRET` | Forgejo (requires `CISID_FORGEJO_URL`) |
| `CISID_GITLAB_OAUTH_CLIENT_ID`, `..._SECRET` | GitLab (`CISID_GITLAB_URL`, default GitLab.com) |
| `CISID_BITBUCKET_OAUTH_CLIENT_ID`, `..._SECRET` | Bitbucket Cloud (the OAuth consumer's key and secret) |
| `CISID_OAUTH_SIGNUP_ROLE` | `viewer` or `maintainer`: a forge user without an account gets an account with this role on their first sign-in. If empty, only users an admin has linked can sign in. |
| `CISID_PUBLIC_URL` | Required: the forge sends the browser back to `<CISID_PUBLIC_URL>/auth/<forge>/callback` |

**Registering the app.** On every forge the *callback* (redirect) URL is `https://<cisid>/auth/<forge>/callback`.

| Forge | Where | Permission |
|---|---|---|
| GitHub | Settings → Developer settings → **OAuth Apps** → New OAuth App | No permission needed (public profile) |
| Gitea, Forgejo | Settings → **Applications** → OAuth2 Applications (or from site administration) | `read:user` |
| GitLab | User settings → **Applications** (or a group or admin application) | `read_user` |
| Bitbucket | Workspace settings → **OAuth consumers** → Add consumer; with *This is a private consumer* checked | *Account: Read* |

**How accounts are matched.**

- An admin enters the user's username on the forge (`identities`, e.g. `{"gitea":"ahmet"}`; *Forge logins* in the UI). The first time that user signs in through the forge, the forge's **immutable user ID** is linked to the account. Later sign-ins match on this ID: the user can still sign in after changing their name on the forge, and someone else who takes the old name cannot.
- If `CISID_OAUTH_SIGNUP_ROLE` is set, cisid creates an account for a forge user who does not match. The username is derived from their name on the forge; if that is taken, the forge's name is appended (`ahmet-gitea`). This is useful on a Gitea or GitLab you host yourself where everyone is an employee. **Do not enable it on GitHub.com, GitLab.com or Bitbucket:** anyone with an account on that forge can sign in.
- A disabled user cannot sign in through a forge either.

**Security.** The sign-in is bound by the `state` parameter (in a short-lived HttpOnly cookie, `SameSite=Lax`). The return address can only be a path on this site (no open redirect). The access token the forge issues is used only to answer "who signed in" and is not stored. A failed sign-in returns to the **Sign in** page with the reason (such as `/login?signin=unknown`).

## Connecting forge accounts

Each user can give cisid their own access to a forge: on the **Account** page, under *Forge accounts*, by OAuth (**Connect**) or with a token of their own (**Use a token**). The repositories they connect from then on are looked up, polled, cloned and reported on (commit statuses) with their access instead of the server's `CISID_<FORGE>_TOKEN`, and the **Connect a repository** dialog lists their own repositories, private ones too. This is what lets a team share one server without one token that reaches everyone's repositories ([team-server.md](team-server.md)).

It needs `CISID_SECRET_KEY`: the tokens are sealed with it (AES-256-GCM, bound to the user and the forge), like the repository secrets, and no API returns them.

| Forge | By OAuth | With a token |
|---|---|---|
| GitHub | The sign-in OAuth app; connecting asks for the `repo` scope | A fine-grained personal access token: Contents and Metadata read, Commit statuses read and write, on the repositories to connect |
| GitLab | The sign-in application, which must allow the `api` scope (and `read_user` for signing in) | A personal access token with `api` |
| Gitea, Forgejo | No | An access token that reads repositories and the user, and writes commit statuses |

- Connecting by OAuth uses the same app and the same callback URL as signing in (`/auth/<forge>/callback`); it only asks for more. GitLab's tokens expire after two hours, and cisid renews them with their refresh token shortly before.
- A token is checked with the forge when it is given, and the login it belongs to is shown next to it. A token the forge does not take is refused (`400`).
- A repository is built with the access of the user who connected it; `GET /api/repos` names them in `via`. If they disconnect the forge, or their account is deleted, the repository goes back to the server's token for the forge, and private repositories stop building when there is none. If the forge stops taking a token (revoked, expired), polling shows the forge's error on the repository's *Polled branches* tab until the account is connected again.

| Endpoint | Description |
|---|---|
| `GET /api/me/forges` | `{"enabled":true,"forges":[{"forge":"github","oauth":true}],"accounts":[{"forge","login","kind","created_at"}]}`: the forges one can connect (and whether by OAuth), and those one did; `kind` is `oauth` or `token`. `enabled` is `false` without `CISID_SECRET_KEY`. |
| `PUT /api/me/forges/{forge}` | Connects the forge with `{"token":"..."}` and returns the account. `400` if the forge does not take the token, `404` for a forge that cannot be connected here, `409` without `CISID_SECRET_KEY`, `503` if the forge cannot be reached. |
| `DELETE /api/me/forges/{forge}` | Forgets the access: `204`, or `404` if it was not connected |
| `GET /auth/{forge}/connect?next=/account` | Connects by OAuth and comes back to `next` with `?connected=<forge>`, or with `?connect=failed`, `denied`, `unavailable` or `expired` |

## Personal tokens

Users create tokens for scripts and the CLI. A token works with its owner's role; if the role changes, so does what the token can do. If the user is disabled or deleted, their tokens stop working.

| Endpoint | Description |
|---|---|
| `GET /api/me/tokens` | Tokens: name, creation time and last-used time. The token itself is not listed. |
| `POST /api/me/tokens` | New token with `{"name":"deploy script"}`. The token appears only in this response: `{"id":3,"name":"...","token":"cisid_..."}` |
| `DELETE /api/me/tokens/{id}` | Revokes the token |
| `PUT /api/me/password` | Changes the password with `{"current":"...","new":"..."}`. A user without a password (who only signs in through a forge) sets one by leaving `current` empty. |

Tokens start with `cisid_`; this makes leaked tokens easier to find in code scans. The server stores only their hashes.

## User management (admin)

| Endpoint | Description |
|---|---|
| `GET /api/users` | Users: name, role, whether they have a password, whether they are disabled, forge accounts |
| `POST /api/users` | `{"username":"ahmet","role":"maintainer","password":"...","identities":{"gitea":"ahmet"}}`; `password` and `identities` are optional. `409` if the name is taken. |
| `PATCH /api/users/{username}` | Any of the fields `role`, `password` (`""` removes the password), `disabled`, `identities` |
| `DELETE /api/users/{username}` | Deletes the user, their sessions and their tokens |

- A username contains lowercase letters, digits, `.`, `_` and `-`, and is at most 39 characters long.
- An admin cannot lower their own role, disable themselves or delete themselves; another admin or the API token does that. This way nobody locks themselves out by mistake.
- A disabled user's sessions end immediately and their tokens stop working.
- `identities` holds the user's usernames on the forges.

In the UI: the person icon at the bottom of the sidebar opens the **Account** page (password, tokens, sign-out); admins also see the **Users** page.
