# cisid Helm chart

**English** · [Türkçe](README.tr.md)

Installs cisid on Kubernetes:

- **server:** Deployment and Service. Optionally, Ingress and ServiceMonitor.
- **agent:** StatefulSet. Each pod has a Docker-in-Docker sidecar that runs the steps.
- **PostgreSQL:** A single-replica StatefulSet for trying things out. It is optional; in production, use an external database.

Requires Kubernetes 1.29 or later: Docker-in-Docker runs as a native sidecar.

## First: images

For every tagged release, the images and the chart are published under `ghcr.io/sidumandr` ([release.md](../../../docs/release.md)). The chart's default values use these images, and you can install the published chart directly:

```bash
helm install ci oci://ghcr.io/sidumandr/charts/cisid --version 0.1.0 --namespace ci
```

If there is no published release yet, or you want to use your own changes, build the images from the project root and push them to your own registry:

```bash
docker build --target server -t ghcr.io/<account>/cisid-server:0.1.0 .
```

```bash
docker build --target agent -t ghcr.io/<account>/cisid-agent:0.1.0 .
```

```bash
docker push ghcr.io/<account>/cisid-server:0.1.0 && docker push ghcr.io/<account>/cisid-agent:0.1.0
```

The tag defaults to the chart's `appVersion` (`0.1.0`). If yours differs, set it with `server.image.tag` and `agent.image.tag`. For a private registry, use `imagePullSecrets`.

## Installation

Agent pods use a privileged container (in `hostSocket` mode, a hostPath volume), so the namespace's Pod Security level must be `privileged`:

```bash
kubectl create namespace ci && kubectl label namespace ci pod-security.kubernetes.io/enforce=privileged
```

```bash
helm install ci deployments/helm/cisid --namespace ci \
  --set server.image.repository=ghcr.io/<account>/cisid-server \
  --set agent.image.repository=ghcr.io/<account>/cisid-agent \
  --set-string secrets.webhookSecret=$(openssl rand -hex 32) \
  --set-string secrets.agentToken=$(openssl rand -hex 24) \
  --set-string postgresql.auth.password=$(openssl rand -hex 16)
```

Pass secrets with `--set-string`. With `--set`, a value made only of digits becomes a number and fails schema validation.

After installation, Helm prints the GitHub webhook URL and the command that reads the webhook secret. To check that the server reaches the database:

```bash
helm test ci --namespace ci
```

## Values

The full list with descriptions is in [values.yaml](values.yaml). `values.schema.json` rejects misspelled keys and wrong types at install time.

| Value | Default | Description |
|---|---|---|
| `secrets.webhookSecret` | — | GitHub webhook secret; enables `/webhooks/github` |
| `secrets.agentToken` | — | **Required.** Agent token, at least 16 characters |
| `secrets.apiToken` | — | Enables the cancel, rerun and secret endpoints |
| `secrets.readToken` | — | Requires a token to read jobs, logs and metrics. Without it, anyone who reaches the server can read them. |
| `secrets.secretKey` | none | Key that encrypts repository secrets and where notifications go (base64, 32 bytes); enables both. Keep a separate copy of it. |
| `secrets.githubToken` | — | For private repositories, rate limits and commit status |
| `secrets.giteaToken`, `secrets.giteaWebhookSecret` | — | Gitea's token and webhook secret (with `server.giteaURL`) |
| `secrets.forgejoToken`, `secrets.forgejoWebhookSecret` | — | Forgejo's token and webhook secret (with `server.forgejoURL`) |
| `secrets.gitlabToken`, `secrets.gitlabWebhookSecret` | — | GitLab's token and webhook secret |
| `secrets.bitbucketToken`, `secrets.bitbucketWebhookSecret` | — | Bitbucket Cloud's token and webhook secret |
| `secrets.oauthClientSecrets.<forge>` | — | Client secret of the OAuth app for signing in through a forge (`github`, `gitea`, `forgejo`, `gitlab`, `bitbucket`) |
| `secrets.adminPassword` | — | Creates `server.adminUser` as an admin on first start, at least 10 characters ([accounts](../../../docs/accounts.md)) |
| `secrets.smtpPassword` | none | The password of `server.smtp.username` |
| `secrets.existingSecret` | — | Reads the values above from a Secret you manage (see below) |
| `server.replicaCount` | `1` | Server replicas |
| `server.giteaURL`, `server.forgejoURL` | — | Addresses of the Gitea and Forgejo servers; setting them turns them on |
| `server.gitlabURL` | GitLab.com | Address of your own GitLab installation |
| `server.readAccess` | `signed-in` | `signed-in`: reading requires a sign-in or a token; `public`: anyone can read. A ServiceMonitor then needs `metrics.serviceMonitor.authorize` and `secrets.readToken`, unless reading is public. |
| `server.sessionTTL` | `7d` | How long a sign-in lasts |
| `server.adminUser` | `admin` | The account that `secrets.adminPassword` creates |
| `server.oauthClientIDs.<forge>` | — | Client ID of the OAuth app; needs its secret and `server.publicURL` (or an ingress) |
| `server.oauthSignupRole` | — | `viewer` or `maintainer`: forge users without an account get one on their first sign-in |
| `agent.labels` | `[]` | Labels these agents offer (`CISID_AGENT_LABELS`); install the chart once per kind of agent for different labels ([pipeline](../../../docs/pipeline.md#labels)) |
| `server.artifactMaxSize` | `100MB` | The largest artifact the server accepts; artifacts are kept in PostgreSQL ([pipeline](../../../docs/pipeline.md#artifacts)) |
| `server.publicBadges` | `false` | Lets anyone see build badges even when reading needs a sign-in ([notifications](../../../docs/notifications.md#badges)) |
| `server.smtp.host`, `.port`, `.username`, `.from`, `.tls` | none, `0` (587, or 465 with `tls`), none, none, `starttls` | The SMTP relay for email notifications; `from` is required with a host. The password goes in `secrets.smtpPassword` ([notifications](../../../docs/notifications.md#email)). |
| `server.pollInterval` | `1m` | Polling interval for watched branches; `0` turns polling off ([polling](../../../docs/polling.md)) |
| `server.publicURL` | first ingress host | External address for commit status links |
| `server.trustProxy` | `true` with an ingress | Takes the client IP from `X-Forwarded-For` |
| `server.tls.secretName` | — | HTTPS for the server, `wss://` for the agents |
| `server.buildForks` | `false` | Also builds pull requests from forks, without secrets |
| `server.approveForks` | `false` | With `buildForks`, a maintainer approves each fork's build before it runs ([webhook](../../../docs/webhook.md#pull-requests)) |
| `server.logRetention` | `90d` | How long logs of finished jobs are kept; `0` keeps them forever |
| `agent.replicaCount` | `1` | Number of agents. Each agent runs one job at a time. |
| `agent.stepMemory`, `agent.stepCPUs`, `agent.stepPids` | —, —, `4096` | Limits for step and service containers |
| `agent.docker.mode` | `dind` | `dind` or `hostSocket` |
| `agent.docker.dind.persistence.enabled` | `false` | Keeps pulled images and job caches in a volume per agent |
| `postgresql.enabled` | `true` | Built-in PostgreSQL. When off, `database.url` is required. |
| `postgresql.auth.password` | — | Password of the built-in PostgreSQL. It may contain only letters, digits and `. _ ~ -`. |
| `ingress.enabled`, `ingress.hosts`, `ingress.tls` | off | External access for webhooks and the UI |
| `tracing.otlpEndpoint` | — | OTLP/HTTP collector ([tracing.md](../../../docs/tracing.md)) |
| `metrics.serviceMonitor.enabled` | `false` | ServiceMonitor for the Prometheus Operator |
| `metrics.serviceMonitor.authorize` | `false` | Scrapes metrics with the read token; turn it on when `secrets.readToken` is set |

## An existing Secret

If you manage secrets with a secrets operator or a GitOps tool, pass the Secret's name with `secrets.existingSecret`. The chart then does not create its own Secret and reads these keys:

| Key | |
|---|---|
| `webhook-secret` | Optional |
| `agent-token` | Required |
| `postgres-password` | Required with the built-in PostgreSQL |
| `database-url` | Required with an external database (`postgres://...`) |
| `api-token`, `read-token`, `secret-key`, `github-token`, `gitea-token`, `gitea-webhook-secret`, `forgejo-token`, `forgejo-webhook-secret`, `gitlab-token`, `gitlab-webhook-secret`, `bitbucket-token`, `bitbucket-webhook-secret`, `admin-password`, `smtp-password` | Optional |

To rotate the agent token, first set the key to `new,old`: the server accepts both, and agents send the first one. After all agents restart, keep only `new`.

## How agents reach Docker

**`dind` (default):** Each agent pod runs a `docker:dind` sidecar. The agent and the steps see only this Docker. Docker listens only on a Unix socket shared within the pod and opens no TCP port. Because the sidecar is a native sidecar, it starts before the agent and stops after it. This way, the containers of a job canceled at shutdown can be cleaned up.

The sidecar runs `privileged`. A container escape can affect the node. Run the agents on nodes dedicated to CI:

```yaml
agent:
  nodeSelector:
    workload: ci
  tolerations:
    - key: workload
      value: ci
      effect: NoSchedule
```

By default, Docker's data (images, step containers) is kept in the pod's ephemeral storage, that is, on the node's disk, and is deleted when the pod restarts. You can set an `ephemeral-storage` limit with `agent.docker.dind.resources`. `agent.docker.dind.persistence.enabled=true` gives each agent a PersistentVolume that stores the images.

**`hostSocket`:** The agent uses the node's Docker socket. This only works on nodes that run Docker Engine (most clusters use containerd). Steps can manage every container on that node.

Because the agents are a StatefulSet, each one uses its pod name (such as `ci-cisid-agent-0`) as its agent name. An agent that restarts connects with the same name, and the server puts its unfinished job back in the queue without waiting for the lease to expire.

## TLS

Inside the cluster, agents send the token unencrypted over `ws://` by default and log this as a warning at startup. For TLS, provide a `kubernetes.io/tls` Secret. The certificate must cover the name of the server Service: `<fullname>-server.<namespace>.svc`. The post-install notes print the full name. With cert-manager:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: cisid-server
  namespace: ci
spec:
  secretName: cisid-server-tls
  dnsNames:
    - ci-cisid-server.ci.svc
  issuerRef:
    name: internal-ca
    kind: ClusterIssuer
```

```bash
helm upgrade ci deployments/helm/cisid --namespace ci --reuse-values --set server.tls.secretName=cisid-server-tls
```

Agents see only the Secret's `ca.crt` key, not the private key. If a self-signed certificate has no `ca.crt`, set `server.tls.caKey=tls.crt`. If you use an ingress, tell the controller that the backend is HTTPS. For ingress-nginx, the annotation is `nginx.ingress.kubernetes.io/backend-protocol: HTTPS`.

## Database

The built-in PostgreSQL is a single replica, has no backups and no high availability. It is fine for trying things out. In production, use a managed or replicated database:

```bash
--set postgresql.enabled=false --set-string database.url='postgres://cisid:...@db.example.com:5432/cisid?sslmode=require'
```

The chart installs a NetworkPolicy that lets only the server pods connect to the built-in PostgreSQL. This matters because the steps also run inside the cluster. Without a CNI that enforces NetworkPolicy, this rule has no effect.

## Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  hosts: [ci.example.com]
  tls:
    - secretName: ci-example-com-tls
      hosts: [ci.example.com]
```

With the ingress on, `server.publicURL` is derived from the first host (`https://ci.example.com`), and the server takes the client IP from `X-Forwarded-For`. This way, rate limiting applies to the client's address, not the ingress controller's. The GitHub webhook URL becomes `https://ci.example.com/webhooks/github`.

## Upgrading and uninstalling

- When the server is updated, the new pod becomes ready first, and then the old one shuts down. Connected agents reconnect to the new server, and jobs are not interrupted.
- Agents are updated one at a time. The job on an agent that shuts down is **canceled** (`canceled`). Rerun it if needed.
- `helm uninstall` does not delete the StatefulSets' PersistentVolumeClaims (database, image cache). To delete the data, delete them separately.

## Verification

The tests in the [charttest](../charttest) module verify the chart without a cluster:

```bash
cd deployments/helm/charttest && go test ./...
```

The tests render the chart with the Helm SDK and run the `helm lint` rules. They convert each object to its Kubernetes type the way the API server does: field names are case-sensitive and unknown fields count as errors. They also check the links between objects: Service selectors, Secret keys, volumes, probe ports, `$(VAR)` order, Ingress and NetworkPolicy targets. cisid's own CI runs these tests on every push.

The chart has not been installed on a real cluster yet. The pods' security settings were tested in Docker: a read-only file system, all capabilities dropped, uid 70 for PostgreSQL and uid 10001 for the server. With these settings, an agent in `hostSocket` mode successfully ran cisid's own CI over TLS.

The Docker-in-Docker sidecar was also tested in Docker with the chart's settings. The agent shared the sidecar's network, as in the pod. Docker in the sidecar listened only on its Unix socket and opened no TCP port. cisid's CI passed inside the sidecar: tests with the race detector, the PostgreSQL service on the job network and the chart tests. After the job finished, no container, network or volume was left in the sidecar. An agent stopped while a job was running reported the job as `canceled`. It cleaned up its containers through the still-running sidecar and exited in 16 seconds (the time limit is 60 seconds).
