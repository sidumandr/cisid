# Web UI

**English** · [Türkçe](tr/ui.md)

The server serves a web UI for watching and managing jobs (`internal/web`). The UI is embedded in the binary. It loads no CDN, library or font from outside; it gets all its data from the [Job API](api.md).

## Screens

| Page | Content |
|---|---|
| `/` | The last 200 jobs. At the top, one sentence sums up the state: how many jobs are running, how many failed in the last 24 hours, and at which step the latest one failed. Tabs (All, Running, Failed, Pull requests) with their counts; filter by repository, branch or commit. Refreshes every 5 seconds. |
| `/jobs/{id}` | The job's status in one sentence ("Failed at test with exit code 1 after 1m 48s."), the triggering event, commit, agent, start time and duration. Below that, the step strip and the live log of the selected step. |
| `/repos` | The repositories that appear in recent jobs: their latest builds, statuses and build counts. |
| `/repos/{owner}/{repo}` | A repository's builds, schedules, polled branches ([polling](polling.md)), notifications ([notifications](notifications.md)) and secrets, in tabs. The **Badge** button gives the Markdown of the repository's build badge. Repositories on other forges open with their prefix, e.g. `/repos/gitea:office/app`, or `/repos/gitlab:group%2Fsub/project` for nested GitLab groups. |

Dialogs: **Run build** (repository and branch), API token, sign-in for reading, new schedule, new notification, new secret, the badge and delete confirmations.

## Step strip

A job's steps (clone, test, chart…) are drawn as segments side by side. A segment's color shows the step's status: passed (green), failed (red), running (orange, blinks slowly), canceled (gray), did not run or not reached yet (dark gray).

- In the list, every row has a small strip, so you see at a glance which job failed where.
- On the job page the strip is large and clickable. The UI shows the log of the step you click. When the page opens, it selects the running step; if there is none, the failed step; if there is none either, the last step. While the job runs, the selection follows the currently running step until you click a step.
- The API knows which step a running job is on from the step whose log arrived last (`current_step`, [api.md](api.md)).

## Logs

- Logs arrive through an SSE stream ([api.md](api.md#live-stream)). ANSI color codes are stripped, and stderr lines are shown dimmer.
- The log panel grows with the output and scrolls on its own once it fills the screen. When you are at the bottom, it scrolls automatically as new lines arrive; if you have scrolled up, it stays where it is.
- For logs past their retention period, the UI shows when they were deleted.

## Appearance

- **Colors:** Orange on a black background in the dark theme, a white background in the light theme. The theme follows the operating system's setting; the button at the bottom of the sidebar changes it, and the browser remembers the choice.
- **Color only on data:** Orange appears on the primary button, on the running step and on the 1px line that marks the selected item (tab, sidebar, step). Statuses are shown as text in the same hue on a faint background.
- **Type:** Text is set in [Authentic Sans](https://authentic.website/sans). Numbers, commit SHAs, durations and logs use the system's monospace font, so numbers in columns stay aligned. The font files are under `internal/web/static/fonts/`; the license note is in [LICENSE.md](../internal/web/static/fonts/LICENSE.md).
- **Phone:** The sidebar turns into a labeled bar at the bottom. Rows turn into cards of two or three lines, and the details on the job page turn into a list.
- The UI text never uses the em dash (U+2014); a test in `internal/web` checks this.

The design follows the app-screen rules of the `design-taste-frontend` skill (data terminal style, seed 1222882761): measurements were taken in a browser at 1440, 768 and 390 pixels wide.

## Actions and tokens

- **Run build**, **Cancel**, **Rerun**, schedule, polling, notification and secret actions require `maintainer` ([accounts.md](accounts.md)). If you are not signed in, a sign-in dialog opens on first use: a username and password, or a token. The UI keeps the server's API token only in that tab's session (`sessionStorage`); a personal token or a password opens a session in the browser.
- The person icon at the bottom of the sidebar opens the **Account** page: role, password change, personal tokens, sign-out. On the **Users** page, admins add users, change their roles and forge logins, disable them and delete them.
- A running job shows only **Cancel**, and a finished job shows only **Rerun**.
- If `CISID_PUBLIC_URL` is set, GitHub commit status links go to the job page (`/jobs/{id}`).

## Security

- Pages are served with `Content-Security-Policy: default-src 'self'`: there are no inline scripts, and nothing is loaded from another origin, fonts included. The server also sends `frame-ancestors 'none'` and `X-Content-Type-Options: nosniff`.
- Every value from the API (log lines included) is inserted into the page with `textContent` and is never interpreted as HTML. A `<script>` in a step's output shows up as text.
- If reading requires sign-in (`CISID_READ_ACCESS=signed-in`), the UI asks you to sign in on the first read. A user session lasts `CISID_SESSION_TTL`; a session opened with a read token lasts until the browser closes ([api.md](api.md#read-access)). Otherwise the pages are public.
- Every change made with a cookie carries the `X-Cisid-Request` header; another site's form cannot add it (CSRF protection).
- The UI never shows secret values. It lists only their names, which builds get them and when they were updated.
