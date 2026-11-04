{{/* The chart name, or nameOverride. */}}
{{- define "cisid.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
The prefix of resource names: the release name, plus the chart name unless
the release name already contains it.
*/}}
{{- define "cisid.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
The name of a component's resources, e.g. "ci-cisid-server". Short enough
for StatefulSet pod labels, which append a revision hash.
*/}}
{{- define "cisid.componentName" -}}
{{- printf "%s-%s" (include "cisid.fullname" .root | trunc 40 | trimSuffix "-") .component }}
{{- end }}

{{- define "cisid.serverName" -}}
{{- include "cisid.componentName" (dict "root" . "component" "server") }}
{{- end }}

{{- define "cisid.agentName" -}}
{{- include "cisid.componentName" (dict "root" . "component" "agent") }}
{{- end }}

{{- define "cisid.postgresqlName" -}}
{{- include "cisid.componentName" (dict "root" . "component" "postgresql") }}
{{- end }}

{{/* Labels on every resource. */}}
{{- define "cisid.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "cisid.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Labels selecting a component's pods; takes (dict "root" . "component" name). */}}
{{- define "cisid.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cisid.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/* A component's labels: the common ones plus its selector labels. */}}
{{- define "cisid.componentLabels" -}}
{{ include "cisid.labels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/* An image reference; takes (dict "root" . "image" .Values.x.image). */}}
{{- define "cisid.image" -}}
{{- printf "%s:%s" .image.repository (default .root.Chart.AppVersion .image.tag | toString) }}
{{- end }}

{{- define "cisid.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "cisid.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* The Secret holding tokens, keys and the database password or URL. */}}
{{- define "cisid.secretName" -}}
{{- default (include "cisid.fullname" .) .Values.secrets.existingSecret }}
{{- end }}

{{- define "cisid.tls" -}}
{{- if .Values.server.tls.secretName }}true{{ end }}
{{- end }}

{{/* The server's address inside the cluster, e.g. http://ci-cisid-server.ci.svc:8080. */}}
{{- define "cisid.serverURL" -}}
{{- printf "%s://%s.%s.svc:%v" (ternary "https" "http" (eq (include "cisid.tls" .) "true")) (include "cisid.serverName" .) .Release.Namespace .Values.server.service.port }}
{{- end }}

{{/* The endpoint agents connect to. */}}
{{- define "cisid.agentServerURL" -}}
{{- if .Values.agent.serverURL }}
{{- .Values.agent.serverURL }}
{{- else }}
{{- printf "%s/agent/ws" (include "cisid.serverURL" . | replace "https://" "wss://" | replace "http://" "ws://") }}
{{- end }}
{{- end }}

{{/* Where users reach the server: server.publicURL or the first ingress host. */}}
{{- define "cisid.publicURL" -}}
{{- if .Values.server.publicURL }}
{{- .Values.server.publicURL }}
{{- else if and .Values.ingress.enabled .Values.ingress.hosts }}
{{- printf "%s://%s" (ternary "https" "http" (not (empty .Values.ingress.tls))) (first .Values.ingress.hosts) }}
{{- end }}
{{- end }}

{{/* Whether to trust X-Forwarded-For: server.trustProxy, else whether an ingress is in front. */}}
{{- define "cisid.trustProxy" -}}
{{- if kindIs "bool" .Values.server.trustProxy }}
{{- .Values.server.trustProxy }}
{{- else }}
{{- .Values.ingress.enabled }}
{{- end }}
{{- end }}

{{/* The DOCKER_HOST of the agents. */}}
{{- define "cisid.dockerHost" -}}
{{- if eq .Values.agent.docker.mode "dind" }}
{{- print "unix:///var/run/dind/docker.sock" }}
{{- else }}
{{- printf "unix://%s" .Values.agent.docker.hostSocket.path }}
{{- end }}
{{- end }}

{{/* The forges' tokens and webhook secrets, by key in the Secret. */}}
{{- define "cisid.forgeSecrets" -}}
webhook-secret: {{ .Values.secrets.webhookSecret | quote }}
github-token: {{ .Values.secrets.githubToken | quote }}
gitea-token: {{ .Values.secrets.giteaToken | quote }}
gitea-webhook-secret: {{ .Values.secrets.giteaWebhookSecret | quote }}
forgejo-token: {{ .Values.secrets.forgejoToken | quote }}
forgejo-webhook-secret: {{ .Values.secrets.forgejoWebhookSecret | quote }}
gitlab-token: {{ .Values.secrets.gitlabToken | quote }}
gitlab-webhook-secret: {{ .Values.secrets.gitlabWebhookSecret | quote }}
bitbucket-token: {{ .Values.secrets.bitbucketToken | quote }}
bitbucket-webhook-secret: {{ .Values.secrets.bitbucketWebhookSecret | quote }}
admin-password: {{ .Values.secrets.adminPassword | quote }}
smtp-password: {{ .Values.secrets.smtpPassword | quote }}
{{- range $forge, $secret := .Values.secrets.oauthClientSecrets }}
{{ $forge }}-oauth-client-secret: {{ $secret | quote }}
{{- end }}
{{- end }}

{{/* The server's settings read from optional Secret keys: env name to key. */}}
{{- define "cisid.optionalSecretEnv" -}}
CISID_API_TOKEN: api-token
CISID_READ_TOKEN: read-token
CISID_SECRET_KEY: secret-key
CISID_GITHUB_TOKEN: github-token
CISID_GITHUB_WEBHOOK_SECRET: webhook-secret
CISID_GITEA_TOKEN: gitea-token
CISID_GITEA_WEBHOOK_SECRET: gitea-webhook-secret
CISID_FORGEJO_TOKEN: forgejo-token
CISID_FORGEJO_WEBHOOK_SECRET: forgejo-webhook-secret
CISID_GITLAB_TOKEN: gitlab-token
CISID_GITLAB_WEBHOOK_SECRET: gitlab-webhook-secret
CISID_BITBUCKET_TOKEN: bitbucket-token
CISID_BITBUCKET_WEBHOOK_SECRET: bitbucket-webhook-secret
CISID_ADMIN_PASSWORD: admin-password
CISID_SMTP_PASSWORD: smtp-password
{{- range $forge := list "github" "gitea" "forgejo" "gitlab" "bitbucket" "google" }}
CISID_{{ upper $forge }}_OAUTH_CLIENT_SECRET: {{ $forge }}-oauth-client-secret
{{- end }}
{{- end }}

{{/* Stops rendering with a clear message when a required value is missing. */}}
{{- define "cisid.validate" -}}
{{- if not .Values.secrets.existingSecret }}
{{- if lt (len .Values.secrets.agentToken) 16 }}
{{- fail "secrets.agentToken must be at least 16 characters (or name a Secret in secrets.existingSecret)" }}
{{- end }}
{{- if and .Values.secrets.apiToken (lt (len .Values.secrets.apiToken) 16) }}
{{- fail "secrets.apiToken must be at least 16 characters" }}
{{- end }}
{{- if and .Values.secrets.adminPassword (lt (len .Values.secrets.adminPassword) 10) }}
{{- fail "secrets.adminPassword must be at least 10 characters" }}
{{- end }}
{{- if and .Values.secrets.readToken (lt (len .Values.secrets.readToken) 16) }}
{{- fail "secrets.readToken must be at least 16 characters" }}
{{- end }}
{{- if .Values.postgresql.enabled }}
{{- if not .Values.postgresql.auth.password }}
{{- fail "postgresql.auth.password is required (or set postgresql.enabled=false and database.url)" }}
{{- end }}
{{- if not (regexMatch "^[A-Za-z0-9._~-]+$" .Values.postgresql.auth.password) }}
{{- fail "postgresql.auth.password may only contain letters, digits and . _ ~ - since it goes into a URL" }}
{{- end }}
{{- else if not .Values.database.url }}
{{- fail "database.url is required when postgresql.enabled is false" }}
{{- end }}
{{- end }}
{{- if and .Values.server.smtp.host (not .Values.server.smtp.from) }}
{{- fail "server.smtp.from is required with server.smtp.host" }}
{{- end }}
{{- if and .Values.ingress.enabled (not .Values.ingress.hosts) }}
{{- fail "ingress.hosts needs at least one host when ingress.enabled is true" }}
{{- end }}
{{- end }}
