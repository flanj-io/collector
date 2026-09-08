{{/*
Names. `flanj-collector.fullname` is the release-scoped prefix; the two roles
append `-front` and `-store`, which is also what the Services are called — the
fronts' store endpoint is rendered from them, never from the image's baked
default.
*/}}
{{- define "flanj-collector.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "flanj-collector.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 51 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 51 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 51 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "flanj-collector.front.fullname" -}}
{{- printf "%s-front" (include "flanj-collector.fullname" .) -}}
{{- end -}}

{{- define "flanj-collector.store.fullname" -}}
{{- printf "%s-store" (include "flanj-collector.fullname" .) -}}
{{- end -}}

{{/*
The StatefulSet's governing Service. Named through its own helper because
"<fullname>-store" + "-headless" is the longest name this chart makes, and a
Service name over 63 characters is rejected by the API server — after the
release is already half created.
*/}}
{{- define "flanj-collector.store.headlessName" -}}
{{- printf "%s-headless" (include "flanj-collector.store.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "flanj-collector.postgres.fullname" -}}
{{- printf "%s-postgres" (include "flanj-collector.fullname" .) -}}
{{- end -}}

{{- define "flanj-collector.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "flanj-collector.labels" -}}
helm.sh/chart: {{ include "flanj-collector.chart" . }}
{{ include "flanj-collector.selectorLabels" . }}
app.kubernetes.io/version: {{ include "flanj-collector.imageTag" . | trunc 63 | trimSuffix "-" | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: flanj
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "flanj-collector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "flanj-collector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "flanj-collector.front.selectorLabels" -}}
{{ include "flanj-collector.selectorLabels" . }}
app.kubernetes.io/component: front
{{- end -}}

{{- define "flanj-collector.store.selectorLabels" -}}
{{ include "flanj-collector.selectorLabels" . }}
app.kubernetes.io/component: store
{{- end -}}

{{- define "flanj-collector.postgres.selectorLabels" -}}
{{ include "flanj-collector.selectorLabels" . }}
app.kubernetes.io/component: postgres
{{- end -}}

{{- define "flanj-collector.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "flanj-collector.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "flanj-collector.imageTag" -}}
{{- default .Chart.AppVersion .Values.image.tag -}}
{{- end -}}

{{- define "flanj-collector.image" -}}
{{- printf "%s:%s" .Values.image.repository (include "flanj-collector.imageTag" .) -}}
{{- end -}}

{{/*
Secret plumbing. The chart owns one Secret for the values-supplied credentials
(spec token, CP deploy token, DSN, bundled-postgres password); anything given as
`existingSecret` is referenced in place and never copied.
*/}}
{{- define "flanj-collector.secretName" -}}
{{- printf "%s-secrets" (include "flanj-collector.fullname" .) -}}
{{- end -}}

{{- define "flanj-collector.specToken.secretName" -}}
{{- default (include "flanj-collector.secretName" .) .Values.specToken.existingSecret -}}
{{- end -}}

{{- define "flanj-collector.deployToken.secretName" -}}
{{- default (include "flanj-collector.secretName" .) .Values.controlPlane.deployToken.existingSecret -}}
{{- end -}}

{{- define "flanj-collector.dsn.secretName" -}}
{{- if .Values.store.dsn.existingSecret -}}
{{- .Values.store.dsn.existingSecret -}}
{{- else -}}
{{- include "flanj-collector.secretName" . -}}
{{- end -}}
{{- end -}}

{{/* Does the chart's own Secret hold anything? */}}
{{- define "flanj-collector.ownSecretNeeded" -}}
{{- $need := false -}}
{{- if and .Values.specToken.value (not .Values.specToken.existingSecret) }}{{ $need = true }}{{ end -}}
{{- if and .Values.controlPlane.deployToken.value (not .Values.controlPlane.deployToken.existingSecret) }}{{ $need = true }}{{ end -}}
{{- if and (eq .Values.store.backend "postgres") (not .Values.store.dsn.existingSecret) -}}
{{-   if or .Values.store.dsn.value .Values.postgres.enabled }}{{ $need = true }}{{ end -}}
{{- end -}}
{{- if $need }}true{{ end -}}
{{- end -}}

{{/*
The DSN of the bundled evaluation postgres. sslmode=disable: it is a pod in the
same namespace reached over the cluster network, with no certificate to verify.
*/}}
{{- define "flanj-collector.bundledDsn" -}}
{{- printf "postgres://%s:%s@%s:5432/%s?sslmode=disable" .Values.postgres.username .Values.postgres.password (include "flanj-collector.postgres.fullname" .) .Values.postgres.database -}}
{{- end -}}

{{- define "flanj-collector.storeIngestEndpoint" -}}
{{- printf "http://%s:4318" (include "flanj-collector.store.fullname" .) -}}
{{- end -}}

{{- define "flanj-collector.storeSpecEndpoint" -}}
{{- printf "http://%s:5337" (include "flanj-collector.store.fullname" .) -}}
{{- end -}}

{{/*
Environment shared by both roles. FLANJ_SPEC_TOKEN goes to BOTH, from the same
Secret key — the identical-value requirement is structural here, not something
an operator has to get right twice.
*/}}
{{- define "flanj-collector.specTokenEnv" -}}
- name: FLANJ_SPEC_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "flanj-collector.specToken.secretName" . }}
      key: {{ .Values.specToken.key }}
{{- end -}}
