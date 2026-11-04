# Web UI

**English** · [Türkçe](tr/ui.md)

The server serves a web UI that introduces cisid and is used to watch and manage jobs. Its source is a React and TypeScript app in [`web/`](../web), built with Vite; the build is committed under `internal/web/static` and embedded in the binary, so running the server needs no Node.js. The UI loads no CDN, library or font from outside; it gets all its data from the [Job API](api.md).

## Screens

| Page | Content |
|---|---|
| `/` | For visitors who have not signed in, an introduction to cisid: what it is, the four moves of a build, the repository's own `.cisid.yml`, what comes with it, and the ways in (**Sign in**, **Create an account**, and **Browse the builds** where reading is public). Where reading is public, it shows the server's latest builds, live; otherwise an example build plays over and over: its strip fills, lint and test run side by side, its log arrives line by line, and it passes. Someone signed in goes straight to `/jobs`. |
| `/login` | **Sign in**: a **Continue with ...** button, with the forge's mark, for each forge set up for sign-in ([accounts.md](accounts.md#signing-in-through-a-forge)), a username and password, or a token instead. `?next=` names the page to return to (only a path on this site); a failed forge sign-in comes back with `?signin=` and says why. |
| `/register` | **Create an account** ([accounts.md](accounts.md#signing-up)). On a server with no accounts the page is **Set up cisid**, and the account it creates is the admin. When sign-up is closed it says so and, if forges create accounts on first sign-in, offers them. |
| `/jobs` | The last 200 jobs. At the top, one sentence sums up the state: how many jobs are running, how many failed in the last 24 hours, and at which step the latest one failed. Tabs (All, Running, Failed, Pull requests) with their counts; filter by repository, branch or commit. Refreshes every 5 seconds. |
| `/jobs/{id}` | The job's status in one sentence ("Failed at test with exit code 1 after 1m 48s."), the triggering event, commit, agent, start time and duration. Below that, the step strip and the live log of the selected step. |
| `/repos` | The repositories that appear in recent jobs: their latest builds, statuses and build counts. |
| `/repos/{owner}/{repo}` | A repository's builds, schedules, polled branches ([polling](polling.md)), notifications ([notifications](notifications.md)) and secrets, in tabs. The **Badge** button gives the Markdown of the repository's build badge. Repositories on other forges open with their prefix, e.g. `/repos/gitea:office/app`, or `/repos/gitlab:group%2Fsub/project` for nested GitLab groups. |
| `/account`, `/users` | The signed-in user's account, and for admins the users ([below](#actions-and-tokens)). |

Dialogs: **Run build** (repository and branch), API token, sign-in, new schedule, new notification, new secret, the badge and delete confirmations.

## Step strip

A job's steps (clone, test, chart…) are drawn as segments side by side. A segment's color shows the step's status: passed (green), failed (red), running (orange, blinks slowly), canceled (gray), did not run or not reached yet (dark gray).

- In the list, every row has a small strip, so you see at a glance which job failed where.
- On the job page the strip is large and clickable. The UI shows the log of the step you click. When the page opens, it selects the running step; if there is none, the failed step; if there is none either, the last step. While the job runs, the selection follows the currently running step until you click a step.
- Agents report when each step starts and ends (`step_results`, [api.md](api.md)), so the strip shows steps that run at the same time ([parallel steps](pipeline.md#parallel-steps)) and, on the job page, how long each step took. For jobs run by older agents, the running step is the one whose log arrived last (`current_step`).

## Artifacts

Below the log, the job page lists the files the build kept ([pipeline.md](pipeline.md#artifacts)) with their sizes; clicking one downloads it.

## Logs

- Logs arrive through an SSE stream ([api.md](api.md#live-stream)). ANSI color codes are stripped, and stderr lines are shown dimmer.
- The log panel grows with the output and scrolls on its own once it fills the screen. When you are at the bottom, it scrolls automatically as new lines arrive; if you have scrolled up, it stays where it is.
- For logs past their retention period, the UI shows when they were deleted.

## Appearance

- **Languages:** English and Turkish. The UI starts in Turkish when the browser's language is Turkish, else in English. The EN/TR switch in the introduction's navbar and on the sign-in pages, and the language button at the bottom of the sidebar, change it, and the browser remembers the choice. A message from the server that has no translation shows in English.
- **The introduction's navbar** floats over the page as a capsule of glass: the mark, the sections in a track with the one in view lit, the language switch, a theme button and the ways in. It draws in once the page slides under it; on narrow screens its links, the language and the theme move into a menu.
- **Colors:** The logo's orange wherever orange appears (buttons, the running step, the selection line, the running status, the badge): `#F26B1D` on the dark theme's black, `#E0560B` on the light theme's white. Orange text on white is the same orange, darker (`#B84508`), so small text reads at 5.4:1. The theme follows the operating system's setting; the button at the bottom of the sidebar (or **Light**/**Dark** on the introduction) changes it, and the browser remembers the choice.
- **Color only on data:** Orange appears on the primary button, on the running step and on the 1px line that marks the selected item (tab, sidebar, step). Statuses are shown as text in the same hue on a faint background.
- **Type:** Text is set in [Authentic Sans](https://authentic.website/sans). On the working screens, numbers, commit SHAs, durations and logs use the system's monospace font, so numbers in columns stay aligned; the introduction and the sign-in pages are set one step larger and keep monospace for code. The font files are under `web/public/fonts/`; the license note is in [LICENSE.md](../web/public/fonts/LICENSE.md).
- **Logo:** The mark alone, two eyes behind crossed lenses, sits at the top of the sidebar; the wordmark (the mark and the name) on the introduction, the sign-in pages, the phone's top bar and the footer. Both are drawn inline in the logo files' orange and ink for the theme's ground (`web/src/ui/Logo.tsx`); the browser tab shows the favicon set from `web/public/`.
- **The owl:** Behind the glass of the introduction's builds and its call to action, the mark floats, blinks now and then, and its eyes follow the pointer. The pointer itself is the owl, hollow over the page and filled over what can be clicked (`web/public/cursor-owl*.svg`); text fields keep the usual text cursor. With reduced motion turned on, the owl keeps still.
- **Glass:** what floats over the page lets it show through, blurred: the introduction's nav once the page slides under it, its menu on narrow screens, the phone's top and bottom bars, and every dropdown. On the introduction the latest builds sit on the same glass, over the cisid mark set large. Dialogs blur the page behind them.
- **Dropdowns** (a role, a notification's kind, the repositories Run build suggests) are drawn by the UI instead of the browser, so their lists can sit on glass. They work from the keyboard: the arrows move, Enter picks, Escape closes the list and leaves the dialog open.
- **The introduction** uses the step strip as its one motif: the four moves of a build are a strip that fills as you scroll, where the browser ties animation to scrolling. With reduced motion turned on, nothing moves.
- **Phone:** The sidebar turns into a labeled bar at the bottom. Rows turn into cards of two or three lines, and the details on the job page turn into a list. The sign-in pages drop their side panel.
- The UI text never uses the em dash (U+2014); a test in `internal/web` checks this.

The design follows the `design-taste-frontend` skill (data terminal style, seed 1222882761): app-screen rules for the working screens, page rules for the introduction and the sign-in pages; measurements were taken in a browser at 1440, 768 and 390 pixels wide.

## Actions and tokens

- **Run build**, **Cancel**, **Rerun**, schedule, polling, notification and secret actions require `maintainer` ([accounts.md](accounts.md)). If you are not signed in, a sign-in dialog opens on first use: a username and password, or a token. The UI keeps the server's API token only in that tab's session (`sessionStorage`); a personal token or a password opens a session in the browser.
- The person icon at the bottom of the sidebar opens the **Account** page (or **Sign in** when you are not signed in): role, password change, personal tokens, sign-out. On the **Users** page, admins add users, change their roles and forge logins, disable them and delete them.
- A running job shows only **Cancel**, and a finished job shows only **Rerun**. A build from a fork that waits for approval also shows **Approve**.
- If `CISID_PUBLIC_URL` is set, GitHub commit status links go to the job page (`/jobs/{id}`).

## Security

- Pages are served with `Content-Security-Policy: default-src 'self'`: there are no inline scripts or styles, and nothing is loaded from another origin, fonts included. The server also sends `frame-ancestors 'none'` and `X-Content-Type-Options: nosniff`.
- Every value from the API (log lines included) is rendered by React as text and is never interpreted as HTML. A `<script>` in a step's output shows up as text. A test in `internal/web` keeps `dangerouslySetInnerHTML`, `innerHTML` and the like out of the source.
- Reading requires a sign-in unless the server sets `CISID_READ_ACCESS=public`: someone who has not signed in sees the introduction, and any other page asks them to sign in. A user session lasts `CISID_SESSION_TTL`; a session opened with a read token lasts until the browser closes ([api.md](api.md#read-access)). With `public`, anyone can read the pages.
- Every change made with a cookie carries the `X-Cisid-Request` header; another site's form cannot add it (CSRF protection).
- The UI never shows secret values. It lists only their names, which builds get them and when they were updated.

## Changing the UI

Node.js 20.19+ or 22.12+ is needed only to change the UI. From `web/`:

```bash
npm ci
```

```bash
npm run dev
```

The dev server on http://localhost:5173 serves the pages at their real addresses, reloads on every change and proxies `/api` and `/auth` to a cisid server, by default `http://localhost:8080` (another with `CISID_DEV_SERVER`).

```bash
npm run typecheck
```

```bash
npm test
```

```bash
npm run build
```

Every text the UI shows is written in English inside `t("...")` (or `N_("...")` in a table translated where it is shown), with `{name}` for the values put in; its Turkish goes in `web/src/lib/i18n.tr.ts`. `npm test` fails while a text has no Turkish, or its Turkish puts in other values.

`npm run build` checks the types and writes the build to `internal/web/static`, which `go build` embeds. Commit the build with the source, so the server is built without Node.js.

| Path | Content |
|---|---|
| `web/src/pages/` | One file per screen: `Landing`, `Auth` (sign in, sign up), `Jobs`, `Job`, `Repos`, `Repo`, `Account`, `Users`, `NotFound` |
| `web/src/ui/` | The sidebar shell, the dialog host, icons and shared parts (strip, pills, notes) |
| `web/src/lib/` | API calls and sign-in, the signed-in user, dialogs, wording and formatting (with tests); `i18n.ts` and the Turkish dictionary `i18n.tr.ts` |
| `web/src/styles/` | `app.css` for the working screens, `site.css` for the introduction and the sign-in pages |
