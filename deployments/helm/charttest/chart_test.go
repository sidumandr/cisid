package charttest

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/chart/v2/lint"
	"helm.sh/helm/v4/pkg/chart/v2/lint/support"
	corev1 "k8s.io/api/core/v1"
)

func TestLint(t *testing.T) {
	for name, vals := range map[string]map[string]any{
		"defaults": required(),
		"everything": with(with(with(with(required(),
			"ingress.enabled", true), "ingress.hosts", []any{"ci.example.com"}),
			"server.tls.secretName", "cisid-tls"), "metrics.serviceMonitor.enabled", true),
	} {
		t.Run(name, func(t *testing.T) {
			linter := lint.RunAll(chartDir, vals, namespace, lint.WithKubeVersion(&kubeVersion))
			for _, msg := range linter.Messages {
				t.Log(msg.Error())
			}
			assert.Less(t, linter.HighestSeverity, support.WarningSev, "helm lint reports warnings or errors")
		})
	}
}

func TestDefaultInstall(t *testing.T) {
	m := render(t, required())

	assert.ElementsMatch(t, []string{"ci-cisid-server"}, slices.Collect(maps.Keys(m.deployments)))
	assert.ElementsMatch(t, []string{"ci-cisid-agent", "ci-cisid-postgresql"}, slices.Collect(maps.Keys(m.statefulSets)))
	assert.ElementsMatch(t, []string{"ci-cisid-server", "ci-cisid-agent", "ci-cisid-postgresql"}, slices.Collect(maps.Keys(m.services)))
	assert.ElementsMatch(t, []string{"ci-cisid"}, slices.Collect(maps.Keys(m.secrets)))
	assert.ElementsMatch(t, []string{"ci-cisid-postgresql"}, slices.Collect(maps.Keys(m.policies)))
	assert.ElementsMatch(t, []string{"ci-cisid-server-test"}, slices.Collect(maps.Keys(m.pods)))
	assert.Empty(t, m.ingresses)
	assert.Empty(t, m.others, "no ServiceMonitor unless asked for")

	secret := m.secrets["ci-cisid"]
	assert.Equal(t, "test-webhook-secret", string(secret.Data["webhook-secret"]))
	assert.Equal(t, "pg-pass_0123", string(secret.Data["postgres-password"]))
	assert.NotContains(t, secret.Data, "api-token", "unset tokens are left out")
	assert.NotContains(t, secret.Data, "database-url")

	server := m.server(t)
	c := container(t, server.Spec.Template.Spec, "server")
	assert.Equal(t, "ghcr.io/sidumandr/cisid-server:0.1.0", c.Image, "the tag defaults to appVersion")
	assert.Equal(t, "postgres://cisid:$(CISID_PG_PASSWORD)@ci-cisid-postgresql:5432/cisid?sslmode=disable",
		env(c, "CISID_DATABASE_URL").Value)
	assert.Equal(t, "false", env(c, "CISID_TRUST_PROXY").Value, "no proxy in front without an ingress")
	assert.Equal(t, "90d", env(c, "CISID_LOG_RETENTION").Value)
	assert.Equal(t, "false", env(c, "CISID_BUILD_FORKS").Value)
	assert.Nil(t, env(c, "CISID_PUBLIC_URL"))
	assert.Nil(t, env(c, "CISID_TLS_CERT_FILE"))
	assert.Nil(t, env(c, "OTEL_EXPORTER_OTLP_ENDPOINT"))
	apiToken := env(c, "CISID_API_TOKEN").ValueFrom.SecretKeyRef
	assert.True(t, *apiToken.Optional, "optional settings may be missing from the Secret")
	assert.Equal(t, "/healthz", c.LivenessProbe.HTTPGet.Path)
	assert.Equal(t, "/readyz", c.ReadinessProbe.HTTPGet.Path)
	assert.Equal(t, []string{"sleep", "5"}, c.Lifecycle.PreStop.Exec.Command)
	assert.Contains(t, server.Spec.Template.Annotations, "checksum/secret")

	agent := m.agent(t)
	a := container(t, agent.Spec.Template.Spec, "agent")
	assert.Equal(t, "ghcr.io/sidumandr/cisid-agent:0.1.0", a.Image)
	assert.Equal(t, "ws://ci-cisid-server.build.svc:8080/agent/ws", env(a, "CISID_SERVER_URL").Value)
	assert.Equal(t, "agent-token", env(a, "CISID_AGENT_TOKEN").ValueFrom.SecretKeyRef.Key)
	assert.Equal(t, "4096", env(a, "CISID_STEP_PIDS").Value)
	assert.Nil(t, env(a, "CISID_STEP_MEMORY"))

	test := m.pods["ci-cisid-server-test"]
	assert.Equal(t, "test", test.Annotations["helm.sh/hook"])
	assert.Equal(t, []string{"wget", "-q", "-O-", "http://ci-cisid-server.build.svc:8080/readyz"}, test.Spec.Containers[0].Command)

	assert.Contains(t, m.notes, "kubectl get secret ci-cisid --namespace build")
	assert.Contains(t, m.notes, "kubectl port-forward --namespace build svc/ci-cisid-server 8080:8080")
}

// TestPodSecurity checks that only the Docker-in-Docker sidecar is
// privileged and everything else runs locked down.
func TestPodSecurity(t *testing.T) {
	m := render(t, required())

	for _, w := range m.workloads() {
		assert.False(t, *w.spec.AutomountServiceAccountToken, "%s needs no API token", w.name)
		for _, c := range slices.Concat(w.spec.InitContainers, w.spec.Containers) {
			sc := c.SecurityContext
			require.NotNil(t, sc, "%s/%s", w.name, c.Name)
			if c.Name == "dind" {
				assert.True(t, *sc.Privileged)
				continue
			}
			assert.Nil(t, sc.Privileged, "%s/%s", w.name, c.Name)
			assert.False(t, *sc.AllowPrivilegeEscalation, "%s/%s", w.name, c.Name)
			assert.True(t, *sc.ReadOnlyRootFilesystem, "%s/%s", w.name, c.Name)
			assert.Equal(t, []corev1.Capability{"ALL"}, sc.Capabilities.Drop, "%s/%s", w.name, c.Name)
		}
	}

	server := m.server(t).Spec.Template.Spec.SecurityContext
	assert.True(t, *server.RunAsNonRoot)
	assert.Equal(t, int64(10001), *server.RunAsUser, "the image's cisid user")
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, server.SeccompProfile.Type)

	pg := m.statefulSets["ci-cisid-postgresql"].Spec.Template.Spec.SecurityContext
	assert.Equal(t, int64(70), *pg.RunAsUser, "the Alpine image's postgres user")
	assert.Equal(t, int64(70), *pg.FSGroup, "so the volume is writable")
}

func TestDockerInDockerSidecar(t *testing.T) {
	m := render(t, required())
	spec := m.agent(t).Spec.Template.Spec

	require.Len(t, spec.InitContainers, 1)
	dind := spec.InitContainers[0]
	assert.Equal(t, "docker:29.8.2-dind", dind.Image)
	require.NotNil(t, dind.RestartPolicy)
	assert.Equal(t, corev1.ContainerRestartPolicyAlways, *dind.RestartPolicy, "a native sidecar outlives the agent")
	assert.Equal(t, []string{"dockerd", "--host=unix:///var/run/dind/docker.sock"}, dind.Args, "no TCP listener")
	assert.NotNil(t, dind.StartupProbe, "the agent starts once Docker answers")

	agent := container(t, spec, "agent")
	assert.Equal(t, "unix:///var/run/dind/docker.sock", env(agent, "DOCKER_HOST").Value)
	assert.NotNil(t, volume(spec, "docker-socket").EmptyDir)
	assert.NotNil(t, volume(spec, "docker-data").EmptyDir)
	assert.Empty(t, m.agent(t).Spec.VolumeClaimTemplates)

	cached := render(t, with(required(), "agent.docker.dind.persistence.enabled", true)).agent(t)
	assert.Nil(t, volume(cached.Spec.Template.Spec, "docker-data"), "the claim template provides it")
	require.Len(t, cached.Spec.VolumeClaimTemplates, 1)
	assert.Equal(t, "docker-data", cached.Spec.VolumeClaimTemplates[0].Name)
	assert.Equal(t, "20Gi", cached.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String())
}

func TestHostDockerSocket(t *testing.T) {
	m := render(t, with(required(), "agent.docker.mode", "hostSocket"))
	spec := m.agent(t).Spec.Template.Spec

	assert.Empty(t, spec.InitContainers, "no Docker-in-Docker")
	agent := container(t, spec, "agent")
	assert.Equal(t, "unix:///var/run/docker.sock", env(agent, "DOCKER_HOST").Value)
	socket := volume(spec, "docker-socket")
	require.NotNil(t, socket.HostPath)
	assert.Equal(t, "/var/run/docker.sock", socket.HostPath.Path)
	assert.Equal(t, corev1.HostPathSocket, *socket.HostPath.Type)
	assert.Contains(t, m.notes, "/var/run/docker.sock")
}

func TestRequiredValues(t *testing.T) {
	for name, tc := range map[string]struct {
		vals map[string]any
		want string
	}{
		"short agent token":     {with(required(), "secrets.agentToken", "short"), "secrets.agentToken must be at least 16"},
		"short API token":       {with(required(), "secrets.apiToken", "short"), "secrets.apiToken must be at least 16"},
		"no database password":  {with(required(), "postgresql.auth.password", ""), "postgresql.auth.password is required"},
		"password breaks a URL": {with(required(), "postgresql.auth.password", "p@ss:word"), "may only contain"},
		"no external database":  {with(required(), "postgresql.enabled", false), "database.url is required"},
		"ingress without hosts": {with(required(), "ingress.enabled", true), "ingress.hosts needs at least one host"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderFiles(tc.vals)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestSchemaRejectsMistakes(t *testing.T) {
	for name, vals := range map[string]map[string]any{
		"misspelt key":     with(required(), "server.replica", 2),
		"unknown mode":     with(required(), "agent.docker.mode", "podman"),
		"bad log level":    with(required(), "server.logLevel", "verbose"),
		"bad duration":     with(required(), "server.shutdownTimeout", "15"),
		"bad retention":    with(required(), "server.logRetention", "3 months"),
		"negative limit":   with(required(), "server.rateLimit", -1),
		"wrong URL scheme": with(required(), "agent.serverURL", "http://ci.example.com/agent/ws"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderFiles(vals)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "values don't meet the specifications of the schema")
		})
	}
}

func TestExistingSecret(t *testing.T) {
	vals := map[string]any{"secrets": map[string]any{"existingSecret": "cisid-credentials"}}
	m := render(t, vals)

	assert.Empty(t, m.secrets, "the chart creates no Secret")
	c := container(t, m.server(t).Spec.Template.Spec, "server")
	for _, name := range []string{"CISID_GITHUB_WEBHOOK_SECRET", "CISID_AGENT_TOKEN", "CISID_PG_PASSWORD"} {
		assert.Equal(t, "cisid-credentials", env(c, name).ValueFrom.SecretKeyRef.Name, name)
	}
	assert.NotContains(t, m.server(t).Spec.Template.Annotations, "checksum/secret")
	pg := container(t, m.statefulSets["ci-cisid-postgresql"].Spec.Template.Spec, "postgresql")
	assert.Equal(t, "postgres-password", env(pg, "POSTGRES_PASSWORD").ValueFrom.SecretKeyRef.Key)
}

func TestExternalDatabase(t *testing.T) {
	vals := with(with(required(), "postgresql.enabled", false),
		"database.url", "postgres://cisid:s3cret@db.example.com:5432/cisid")
	m := render(t, vals)

	assert.NotContains(t, m.statefulSets, "ci-cisid-postgresql")
	assert.NotContains(t, m.services, "ci-cisid-postgresql")
	assert.Empty(t, m.policies)
	assert.Equal(t, "postgres://cisid:s3cret@db.example.com:5432/cisid", string(m.secrets["ci-cisid"].Data["database-url"]))
	assert.NotContains(t, m.secrets["ci-cisid"].Data, "postgres-password")

	c := container(t, m.server(t).Spec.Template.Spec, "server")
	assert.Equal(t, "database-url", env(c, "CISID_DATABASE_URL").ValueFrom.SecretKeyRef.Key)
	assert.Nil(t, env(c, "CISID_PG_PASSWORD"))
}

func TestTLS(t *testing.T) {
	vals := with(with(required(), "server.tls.secretName", "cisid-tls"), "metrics.serviceMonitor.enabled", true)
	m := render(t, vals)

	spec := m.server(t).Spec.Template.Spec
	c := container(t, spec, "server")
	assert.Equal(t, "/etc/cisid/tls/tls.crt", env(c, "CISID_TLS_CERT_FILE").Value)
	assert.Equal(t, "/etc/cisid/tls/tls.key", env(c, "CISID_TLS_KEY_FILE").Value)
	assert.Equal(t, corev1.URISchemeHTTPS, c.ReadinessProbe.HTTPGet.Scheme)
	assert.Equal(t, corev1.URISchemeHTTPS, c.LivenessProbe.HTTPGet.Scheme)
	assert.Equal(t, "cisid-tls", volume(spec, "tls").Secret.SecretName)

	agentSpec := m.agent(t).Spec.Template.Spec
	a := container(t, agentSpec, "agent")
	assert.Equal(t, "wss://ci-cisid-server.build.svc:8080/agent/ws", env(a, "CISID_SERVER_URL").Value)
	assert.Equal(t, "/etc/cisid/server-ca/ca.crt", env(a, "CISID_SERVER_CA_FILE").Value)
	ca := volume(agentSpec, "server-ca").Secret
	assert.Equal(t, []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}, ca.Items, "agents get the CA, not the private key")

	test := m.pods["ci-cisid-server-test"].Spec.Containers[0]
	assert.Contains(t, test.Command, "--no-check-certificate")
	assert.Contains(t, test.Command, "https://ci-cisid-server.build.svc:8080/readyz")

	monitor := m.others["ServiceMonitor/ci-cisid-server"]
	require.NotNil(t, monitor)
	endpoint := monitor["spec"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
	assert.Equal(t, "https", endpoint["scheme"])
	assert.Equal(t, "ci-cisid-server.build.svc", endpoint["tlsConfig"].(map[string]any)["serverName"])

	selfSigned := render(t, with(with(required(), "server.tls.secretName", "cisid-tls"), "server.tls.caKey", "tls.crt"))
	ca = volume(selfSigned.agent(t).Spec.Template.Spec, "server-ca").Secret
	assert.Equal(t, []corev1.KeyToPath{{Key: "tls.crt", Path: "ca.crt"}}, ca.Items)
}

func TestIngress(t *testing.T) {
	vals := with(with(with(required(),
		"ingress.enabled", true),
		"ingress.hosts", []any{"ci.example.com", "ci.example.org"}),
		"ingress.tls", []any{map[string]any{"secretName": "ci-tls", "hosts": []any{"ci.example.com"}}})
	m := render(t, vals)

	ing := m.ingresses["ci-cisid-server"]
	require.Len(t, ing.Spec.Rules, 2)
	assert.Equal(t, "ci.example.com", ing.Spec.Rules[0].Host)
	assert.Equal(t, "ci-tls", ing.Spec.TLS[0].SecretName)

	c := container(t, m.server(t).Spec.Template.Spec, "server")
	assert.Equal(t, "https://ci.example.com", env(c, "CISID_PUBLIC_URL").Value, "links use the first host")
	assert.Equal(t, "true", env(c, "CISID_TRUST_PROXY").Value, "the ingress controller sets X-Forwarded-For")
	assert.Contains(t, m.notes, "https://ci.example.com/webhooks/github")

	explicit := render(t, with(with(vals, "server.trustProxy", false), "server.publicURL", "https://cisid.example.net"))
	c = container(t, explicit.server(t).Spec.Template.Spec, "server")
	assert.Equal(t, "false", env(c, "CISID_TRUST_PROXY").Value)
	assert.Equal(t, "https://cisid.example.net", env(c, "CISID_PUBLIC_URL").Value)
}

func TestSettingsReachTheProcesses(t *testing.T) {
	vals := required()
	for path, value := range map[string]any{
		"tracing.otlpEndpoint":       "http://otel-collector.observability:4318",
		"secrets.apiToken":           "api-token-0123456789",
		"secrets.secretKey":          "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"server.logLevel":            "debug",
		"server.rateLimit":           2.5,
		"server.githubAPIURL":        "https://ghe.example.com/api/v3",
		"server.image.tag":           "2", // as "--set" passes it: a number
		"agent.stepMemory":           "2g",
		"agent.stepCPUs":             1.5,
		"agent.extraEnv":             []any{map[string]any{"name": "HTTPS_PROXY", "value": "http://proxy:3128"}},
		"server.extraEnv":            []any{map[string]any{"name": "TZ", "value": "Europe/Istanbul"}},
		"imagePullSecrets":           []any{map[string]any{"name": "regcred"}},
		"agent.replicaCount":         3,
		"server.preStopSleepSeconds": 0,
		"server.pollInterval":        "5m",
	} {
		vals = with(vals, path, value)
	}
	m := render(t, vals)

	spec := m.server(t).Spec.Template.Spec
	c := container(t, spec, "server")
	assert.Equal(t, "ghcr.io/sidumandr/cisid-server:2", c.Image)
	assert.Equal(t, "http://otel-collector.observability:4318", env(c, "OTEL_EXPORTER_OTLP_ENDPOINT").Value)
	assert.Equal(t, "debug", env(c, "CISID_LOG_LEVEL").Value)
	assert.Equal(t, "2.5", env(c, "CISID_RATE_LIMIT").Value)
	assert.Equal(t, "https://ghe.example.com/api/v3", env(c, "CISID_GITHUB_API_URL").Value)
	assert.Equal(t, "5m", env(c, "CISID_POLL_INTERVAL").Value)
	assert.Equal(t, "Europe/Istanbul", env(c, "TZ").Value)
	assert.Nil(t, c.Lifecycle, "no preStop sleep when set to 0")
	assert.Equal(t, "regcred", spec.ImagePullSecrets[0].Name)
	assert.Equal(t, "api-token-0123456789", string(m.secrets["ci-cisid"].Data["api-token"]))
	assert.Contains(t, m.secrets["ci-cisid"].Data, "secret-key")

	agent := m.agent(t)
	assert.Equal(t, int32(3), *agent.Spec.Replicas)
	a := container(t, agent.Spec.Template.Spec, "agent")
	assert.Equal(t, "http://otel-collector.observability:4318", env(a, "OTEL_EXPORTER_OTLP_ENDPOINT").Value)
	assert.Equal(t, "2g", env(a, "CISID_STEP_MEMORY").Value)
	assert.Equal(t, "1.5", env(a, "CISID_STEP_CPUS").Value)
	assert.Equal(t, "http://proxy:3128", env(a, "HTTPS_PROXY").Value)
}

func TestForgeSettingsReachTheServer(t *testing.T) {
	vals := required()
	for path, value := range map[string]any{
		"server.giteaURL":                "https://git.example.com",
		"server.gitlabURL":               "https://gitlab.example.com",
		"secrets.giteaToken":             "gitea-token",
		"secrets.giteaWebhookSecret":     "gitea-hook",
		"secrets.bitbucketToken":         "bb-token",
		"secrets.gitlabWebhookSecret":    "gitlab-hook",
		"secrets.forgejoWebhookSecret":   "",
		"secrets.bitbucketWebhookSecret": "",
	} {
		vals = with(vals, path, value)
	}
	m := render(t, vals)

	c := container(t, m.server(t).Spec.Template.Spec, "server")
	assert.Equal(t, "https://git.example.com", env(c, "CISID_GITEA_URL").Value)
	assert.Equal(t, "https://gitlab.example.com", env(c, "CISID_GITLAB_URL").Value)
	assert.Nil(t, env(c, "CISID_FORGEJO_URL"), "an unset URL is left out")
	for name, key := range map[string]string{
		"CISID_GITEA_TOKEN": "gitea-token", "CISID_GITEA_WEBHOOK_SECRET": "gitea-webhook-secret",
		"CISID_BITBUCKET_TOKEN": "bitbucket-token", "CISID_GITLAB_WEBHOOK_SECRET": "gitlab-webhook-secret",
		"CISID_FORGEJO_TOKEN": "forgejo-token",
	} {
		ref := env(c, name).ValueFrom.SecretKeyRef
		assert.Equal(t, key, ref.Key, name)
		assert.True(t, *ref.Optional, "%s is optional", name)
	}
	data := m.secrets["ci-cisid"].Data
	assert.Equal(t, "gitea-hook", string(data["gitea-webhook-secret"]))
	assert.Equal(t, "bb-token", string(data["bitbucket-token"]))
	assert.NotContains(t, data, "forgejo-webhook-secret", "empty values stay out of the Secret")
}

func TestAccountSettingsReachTheServer(t *testing.T) {
	vals := with(required(), "secrets.adminPassword", "a-long-admin-password")
	vals = with(vals, "server.readAccess", "signed-in")
	vals = with(vals, "server.sessionTTL", "12h")
	m := render(t, vals)

	c := container(t, m.server(t).Spec.Template.Spec, "server")
	assert.Equal(t, "signed-in", env(c, "CISID_READ_ACCESS").Value)
	assert.Equal(t, "12h", env(c, "CISID_SESSION_TTL").Value)
	assert.Equal(t, "admin", env(c, "CISID_ADMIN_USER").Value)
	assert.Equal(t, "admin-password", env(c, "CISID_ADMIN_PASSWORD").ValueFrom.SecretKeyRef.Key)
	assert.Equal(t, "a-long-admin-password", string(m.secrets["ci-cisid"].Data["admin-password"]))

	assert.Nil(t, env(container(t, render(t, required()).server(t).Spec.Template.Spec, "server"), "CISID_READ_ACCESS"),
		"the server picks the default")
	_, err := renderFiles(with(required(), "secrets.adminPassword", "short"))
	assert.ErrorContains(t, err, "secrets.adminPassword must be at least 10")
}

func TestOAuthSettingsReachTheServer(t *testing.T) {
	vals := with(required(), "server.oauthClientIDs.gitea", "gitea-client")
	vals = with(vals, "secrets.oauthClientSecrets.gitea", "gitea-client-secret")
	vals = with(vals, "server.oauthSignupRole", "viewer")
	m := render(t, vals)

	c := container(t, m.server(t).Spec.Template.Spec, "server")
	assert.Equal(t, "gitea-client", env(c, "CISID_GITEA_OAUTH_CLIENT_ID").Value)
	assert.Nil(t, env(c, "CISID_GITHUB_OAUTH_CLIENT_ID"), "an unset client id is left out")
	assert.Equal(t, "gitea-oauth-client-secret", env(c, "CISID_GITEA_OAUTH_CLIENT_SECRET").ValueFrom.SecretKeyRef.Key)
	assert.Equal(t, "viewer", env(c, "CISID_OAUTH_SIGNUP_ROLE").Value)
	data := m.secrets["ci-cisid"].Data
	assert.Equal(t, "gitea-client-secret", string(data["gitea-oauth-client-secret"]))
	assert.NotContains(t, data, "github-oauth-client-secret")
}

func TestNotificationSettingsReachTheServer(t *testing.T) {
	vals := with(required(), "server.smtp.host", "smtp.example.com")
	vals = with(vals, "server.smtp.from", "cisid <ci@example.com>")
	vals = with(vals, "server.smtp.username", "ci")
	vals = with(vals, "secrets.smtpPassword", "smtp-secret")
	vals = with(vals, "server.publicBadges", true)
	m := render(t, vals)

	c := container(t, m.server(t).Spec.Template.Spec, "server")
	assert.Equal(t, "smtp.example.com", env(c, "CISID_SMTP_HOST").Value)
	assert.Equal(t, "cisid <ci@example.com>", env(c, "CISID_SMTP_FROM").Value)
	assert.Equal(t, "starttls", env(c, "CISID_SMTP_TLS").Value)
	assert.Equal(t, "ci", env(c, "CISID_SMTP_USERNAME").Value)
	assert.Nil(t, env(c, "CISID_SMTP_PORT"), "the server picks the port for the TLS mode")
	assert.Equal(t, "smtp-password", env(c, "CISID_SMTP_PASSWORD").ValueFrom.SecretKeyRef.Key)
	assert.Equal(t, "smtp-secret", string(m.secrets["ci-cisid"].Data["smtp-password"]))
	assert.Equal(t, "true", env(c, "CISID_PUBLIC_BADGES").Value)
	assert.Equal(t, "100MB", env(c, "CISID_ARTIFACT_MAX_SIZE").Value, "the default")

	m = render(t, with(vals, "server.smtp.port", 2525))
	assert.Equal(t, "2525", env(container(t, m.server(t).Spec.Template.Spec, "server"), "CISID_SMTP_PORT").Value)

	plain := container(t, render(t, required()).server(t).Spec.Template.Spec, "server")
	assert.Nil(t, env(plain, "CISID_SMTP_HOST"), "email is off by default")
	assert.Nil(t, env(plain, "CISID_SMTP_TLS"))
	assert.Equal(t, "false", env(plain, "CISID_PUBLIC_BADGES").Value)

	_, err := renderFiles(with(required(), "server.smtp.host", "smtp.example.com"))
	assert.ErrorContains(t, err, "server.smtp.from is required")
	_, err = renderFiles(with(required(), "server.smtp.tls", "ssl"))
	assert.ErrorContains(t, err, "values don't meet the specifications of the schema")
	_, err = renderFiles(with(required(), "server.artifactMaxSize", "a lot"))
	assert.ErrorContains(t, err, "values don't meet the specifications of the schema")
}

func TestAgentLabels(t *testing.T) {
	m := render(t, with(required(), "agent.labels", []any{"arm64", "gpu"}))
	c := container(t, m.agent(t).Spec.Template.Spec, "agent")
	assert.Equal(t, "arm64,gpu", env(c, "CISID_AGENT_LABELS").Value)

	plain := container(t, render(t, required()).agent(t).Spec.Template.Spec, "agent")
	assert.Nil(t, env(plain, "CISID_AGENT_LABELS"), "no labels by default")

	_, err := renderFiles(with(required(), "agent.labels", []any{"has space"}))
	assert.ErrorContains(t, err, "values don't meet the specifications of the schema")
}

func TestNoWebhookSecretIsFine(t *testing.T) {
	m := render(t, with(required(), "secrets.webhookSecret", ""))
	assert.NotContains(t, m.secrets["ci-cisid"].Data, "webhook-secret", "a server that polls needs no webhook")
}

func TestLongReleaseNamesFit(t *testing.T) {
	chrt := strings.Repeat("x", 63)
	m := render(t, with(required(), "fullnameOverride", chrt))
	// render checks every name; the components keep their suffix.
	for name := range m.statefulSets {
		assert.True(t, strings.HasSuffix(name, "-agent") || strings.HasSuffix(name, "-postgresql"), name)
	}
	for name := range m.services {
		assert.LessOrEqual(t, len(name), 63, name)
	}
}
