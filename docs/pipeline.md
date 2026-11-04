# Pipeline (`.cisid.yml`)

**English** · [Türkçe](tr/pipeline.md)

For every push, cisid first clones the repository, then runs the steps defined in the `.cisid.yml` file at the repository root, in order.

## Example

```yaml
steps:
  - name: lint
    image: golang:1.27
    commands:
      - go vet ./...
    environment:
      CGO_ENABLED: 0
    timeout: 5m

  - name: test
    image: golang:1.27
    commands:
      - go test ./...
```

cisid runs its own tests this way too: [/.cisid.yml](../.cisid.yml).

## Fields

| Field | Required | Description |
|---|---|---|
| `steps` | yes | 1 to 50 steps, run in the order they are written, or by `depends_on` ([Parallel steps](#parallel-steps)) |
| `services` | no | Up to 10 services started before the steps ([Services](#services)) |
| `matrix` | no | Runs the pipeline as a separate job for each combination of values ([Matrix](#matrix)) |
| `cache` | no | Up to 10 directories to keep between the repository's jobs, as absolute paths ([Cache](#cache)) |
| `artifacts` | no | Up to 20 files or directories to keep once the steps pass, relative to the workspace, wildcards allowed ([Artifacts](#artifacts)) |
| `steps[].name` | yes | Letters, digits, `.`, `_`, `-`. At most 63 characters, must be unique. The name `clone` is reserved. |
| `steps[].image` | yes | The Docker image the step runs in. Must contain `/bin/sh`. |
| `steps[].commands` | yes | Shell commands |
| `steps[].environment` | no | Environment variables. Numbers and booleans are converted to strings. The variables cisid sets itself (table below) are reserved. |
| `steps[].timeout` | no | In Go duration format (`90s`, `10m`, `1h30m`). Default `1h`, at most `24h`. |
| `steps[].secrets` | no | Repository secrets to give the step as environment variables, masked in logs ([secrets.md](secrets.md)) |
| `steps[].when.branch` | no | Runs the step only on these branches; for pull requests, the PR's own branch is checked. Can be a single value or a list (see [Conditions](#conditions)). |
| `steps[].when.event` | no | Runs the step only on these events: `push`, `pull_request`, `manual`, `schedule`. If omitted, the step runs on every event. |
| `steps[].depends_on` | no | The steps written before this one that must pass before it starts; a single name or a list. Using it in any step makes the pipeline run steps in parallel ([Parallel steps](#parallel-steps)). |

Unknown fields are an error. For example, if you write `command:`, the job becomes `failed` right away and the error message points out the typo. All problems are listed in a single message.

## Conditions

```yaml
steps:
  - name: test
    image: golang:1.27
    commands: [go test ./...]

  - name: deploy
    image: alpine:3.22
    commands: [./deploy.sh]
    when:
      event: push
      branch: [main, "release/*"]
```

`when.branch` patterns match the **whole** branch name:

| Pattern | Matches | Does not match |
|---|---|---|
| `main` | `main` | `mainline` |
| `release/*` | `release/1.0` | `release/1.0/rc1` (`*` stays within one segment) |
| `hotfix/**` | `hotfix/a/b` | `hot/fix` |
| `v?.x` | `v1.x` | `v10.x` |

`when.event` limits a step to certain events: `push`, `pull_request`, and `manual` (manually started) and `schedule` (scheduled) builds ([api.md](api.md#manual-and-scheduled-builds)). For example, a long test can run only in the nightly build with `event: schedule`. If you give more than one condition, all of them must hold.

For pull requests, `when.branch` checks the PR's **own branch**, not its target branch. Limit steps such as deploy with `event: push`, as above; otherwise they also run in a PR opened from a branch named `main`.

Steps that do not match are not added to the job at all, and the following steps run in their normal order. `when` supports `branch` and `event`. Any other key makes the file invalid.

## Parallel steps

Without `depends_on`, steps run one after another in the order they are written. As soon as one step has `depends_on`, every step waits only for the steps it names, and steps that do not need each other run at the same time:

```yaml
steps:
  - name: lint
    image: golang:1.27
    commands: [go vet ./...]

  - name: build
    image: golang:1.27
    commands: [go build ./...]
    depends_on: []

  - name: unit
    image: golang:1.27
    commands: [go test ./...]
    depends_on: [build]

  - name: report
    image: alpine:3.22
    commands: [./report.sh]
    depends_on: [lint, unit]
```

Here `lint` and `build` start together right after the clone, `unit` starts once `build` passes, and `report` once both `lint` and `unit` have passed.

- A step can depend only on steps **written before it**, which rules out cycles. `clone` is always done first and may be named too.
- A step without `depends_on` waits for the clone only; `depends_on: []` says the same thing explicitly.
- If a step it depends on is left out by `when`, the step waits for that step's own dependencies instead.
- **The first failure stops the job:** steps still running are stopped (`canceled`), and steps that have not started do not run (`skipped`). The job's `failed_step` is the step that failed.
- Steps running at the same time share `/workspace` and the services: two steps writing the same file will get in each other's way.
- On the job page every step shows its own state and how long it took ([ui.md](ui.md#step-strip)).

## Services

```yaml
services:
  - name: postgres
    image: postgres:18-alpine
    environment:
      POSTGRES_PASSWORD: test

steps:
  - name: test
    image: golang:1.27
    environment:
      DATABASE_URL: postgres://postgres:test@postgres:5432/postgres?sslmode=disable
    commands:
      - go test ./...
```

- Services start before the job's first step, in a separate Docker network created for each job. The steps also run in this network and reach a service **by its name** (`postgres:5432`).
- A service name must be a host name: lowercase letters, digits and `-`, at most 63 characters.
- `environment` sets environment variables, and `command` replaces the image's default command (e.g. `[redis-server, --save, ""]`).
- cisid does not wait for a service to be ready. A step that connects right away should wait first, for example: `until pg_isready -h postgres; do sleep 1; done`.
- When the job ends, the services, the network and the workspace are removed, even if the job is canceled or fails.
- If a service cannot start (e.g. its image is not found), no step runs. The job becomes `failed`, and the error message names the service.
- Service output is not added to the job logs. Services cannot receive secrets.

cisid's own CI also runs its database tests against a postgres service: [/.cisid.yml](../.cisid.yml).

## Matrix

```yaml
matrix:
  GO_VERSION: ["1.26", "1.27"]
  POSTGRES: ["17", "18"]

services:
  - name: postgres
    image: postgres:${POSTGRES}-alpine
    environment:
      POSTGRES_PASSWORD: test

steps:
  - name: test
    image: golang:${GO_VERSION}
    commands:
      - go test ./...
```

- Each combination (2 × 2 = 4 above) runs as a **separate job**. The jobs can run in parallel on different agents, and each one gets its own workspace and services.
- The job created by the push takes the first combination. The other combinations are created as sibling jobs when this job is planned (`parent_id` points to the first job). Combinations are ordered by axis name, and values are used in the order they are written.
- Each axis is given to steps and services as an environment variable (`$GO_VERSION`). You can write `${AXIS}` in `image` and `environment` values. If an image refers to an axis that does not exist, the file is invalid.
- A value that contains `${...}` must be quoted when it is written in flow style: `{image: "golang:${GO_VERSION}"}`.
- There can be at most 8 axes and 16 combinations. Axis names must be environment variable names, cannot be reserved names, and cannot clash with a step's `environment` field.
- Each combination gets its own commit status on GitHub, for example `cisid (GO_VERSION=1.27, POSTGRES=18)` ([commit-status.md](commit-status.md)).
- When a matrix job is rerun, only its own combination runs again.

## Artifacts

```yaml
steps:
  - name: build
    image: golang:1.27
    commands: [go build -o dist/ ./cmd/...]
  - name: test
    image: golang:1.27
    commands: [go test -coverprofile=coverage.out ./...]

artifacts:
  - dist
  - coverage.out
  - reports/*.xml
```

Once every step has passed, cisid runs one more step, `artifacts`, which collects the listed files from `/workspace` and uploads them to the server. They are listed on the job page, with a link to download each ([ui.md](ui.md)), and in the API ([api.md](api.md#artifacts)).

- Paths are relative to `/workspace`. `*`, `?` and `[...]` match within a path segment (`reports/*.xml`); a directory keeps every file in it. A path that matches nothing is noted in the step's log and does not fail the job.
- Each file is one artifact, named by its path, e.g. `dist/app`. A job keeps at most 100 files, each up to `CISID_ARTIFACT_MAX_SIZE` (100 MB by default, [server.md](server.md)). A file over the limit fails the `artifacts` step, and so the job.
- Artifacts are not collected when a step fails or the job is canceled.
- The step runs in the clone image (`alpine/git`), so its log shows each file it uploaded. The name `artifacts` is reserved for it.
- Artifacts are stored in PostgreSQL and deleted with the job's logs when `CISID_LOG_RETENTION` runs out. Plan the database's size for them.
- Pull requests from forks keep artifacts too; whoever can read the job can download them.

## Cache

```yaml
cache:
  - /go/pkg/mod
  - /root/.cache/go-build
```

Each directory in the list is mounted into every step as a named Docker volume that belongs to the repository. The volume is not removed when the job ends; the next job that runs on the same Docker host finds it ready. Dependencies do not have to be downloaded again in every job.

- Paths must be clean and absolute. `/` and `/workspace` cannot be cached, but a directory inside the workspace (e.g. `/workspace/node_modules`) can.
- The cache is per repository and is kept separately on each agent's Docker host. Concurrent jobs on the same host share the same volume; Go's module and build caches handle this well.
- Pull requests from forks see the cache as read-only. This way they cannot leave files behind for the repository's own builds.
- Volumes carry the label `cisid.cache=<key>`. To remove them all: `docker volume prune -a --filter label=cisid.cache` ([agent.md](agent.md#cache-volumes)).

## How it runs

- **Clone:** The first step is always `clone`. The commit being built is fetched into `/workspace` with `alpine/git:2.52.0` (`--depth=1`): on pushes, the pushed commit; on pull requests, the PR's latest commit. The branch name is set up as a local branch.
  - On a git server that does not serve a commit by its SHA, the last 50 commits of the branch are fetched (for pull requests, of the PR ref, e.g. `refs/pull/7/head`, or `refs/merge-requests/7/head` on GitLab); the commit being built must be among them.
  - If the forge's token is set, private repositories are cloned with that token. The token is given only to the clone step, as a secret, is masked in the output, and is not written to `.git/config` ([forges.md](forges.md#private-repositories)).
- **Workspace:** All steps share the same job volume at `/workspace` and run there. Files that one step produces are visible to the steps that run after it.
- **Commands:** A step's commands run as a single `/bin/sh` script (`set -ex`). The step stops at the first failing command, and each command is written to stderr before it runs.
- **Order:** Steps run one after another, or by `depends_on` several at a time ([Parallel steps](#parallel-steps)).
- **Failure:** If a step fails, the steps that have not started do not run, and steps running at the same time are stopped.
- **Environment variables:** Each step has these variables set:

| Variable | Value |
|---|---|
| `CI`, `CISID` | `true` |
| `CISID_JOB_ID` | Job number |
| `CISID_STEP` | Step name |
| `CISID_REPO` | Repository key: `owner/name` on GitHub, e.g. `gitlab:group/project` on other forges ([forges.md](forges.md#repository-names)) |
| `CISID_EVENT` | `push`, `pull_request`, `manual` or `schedule` |
| `CISID_BRANCH` | The pushed branch; for pull requests, the PR's own branch |
| `CISID_COMMIT_SHA` | The commit being built |
| `CISID_PULL_REQUEST` | PR number (pull requests only) |
| `CISID_TARGET_BRANCH` | The branch the PR merges into (pull requests only) |

## Reading the file

Right before it gives the job to an agent, the server reads `.cisid.yml` **from the commit being built**, through the API of the repository's forge (`internal/forge`). The agent never deals with the file.

| Case | Result |
|---|---|
| File missing | Job `failed`: `no .cisid.yml in repository` |
| File invalid | Job `failed`, all errors in the `error` field |
| Forge unreachable, 5xx or rate limit | The job **does not count as failed**: it goes back to the queue and is retried on the next round (within 5 s) |
| Server shut down in the middle of the read | The job goes back to the queue |

Public repositories are read without a token. But forges put a low limit on unauthenticated requests (on GitHub, 60 requests per hour per IP). For private repositories or heavy use, set the forge's token: `CISID_GITHUB_TOKEN`, `CISID_GITEA_TOKEN`, `CISID_FORGEJO_TOKEN`, `CISID_GITLAB_TOKEN` or `CISID_BITBUCKET_TOKEN` ([forges.md](forges.md)). The logs only show whether each token is set.

The file can be at most 64 KB.
