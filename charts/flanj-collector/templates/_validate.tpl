{{/*
Install-time refusals.

values.schema.json catches these too — it runs first and it is what makes an
`--set`-only mistake fail before anything is created. These exist alongside it
for the message: the schema can say "specToken.value: String length must be
greater than or equal to 1", it cannot say why a store pod would otherwise exit
at startup. They also still fire under --skip-schema-validation and when this
chart is a subchart.

Every one of them refuses an install that would come up broken rather than
broken-looking: the tiered shape has two failure modes that are invisible from
`kubectl get pods` (a front reading contracts with the wrong token gets 401 and
detects nothing; a PVC on the postgres tier is a durability claim nothing
honours).
*/}}
{{- define "flanj-collector.validate" -}}

{{- if and .Values.specToken.value .Values.specToken.existingSecret -}}
{{- fail "flanj-collector: set specToken.value OR specToken.existingSecret, not both — two sources for one token is a value the fronts and the store pod can disagree about." -}}
{{- end -}}

{{- if not (or .Values.specToken.value .Values.specToken.existingSecret) -}}
{{- fail (printf "%s\n%s\n%s\n%s\n"
  "flanj-collector: specToken is required."
  "  The store pod serves the uploaded contracts to the fronts over an intra-cluster port and REFUSES TO START without a token guarding it; a front with no token starts fine, gets 401 on every contract read, and silently detects no REST drift at all."
  "  Set specToken.value=<a long random string> (the chart creates the Secret and gives the identical value to both roles),"
  "  or specToken.existingSecret=<name> if you manage the Secret yourself.") -}}
{{- end -}}

{{- if not (has .Values.store.backend (list "sqlite" "postgres")) -}}
{{- fail (printf "flanj-collector: store.backend must be \"sqlite\" or \"postgres\", got %q." .Values.store.backend) -}}
{{- end -}}

{{- if eq .Values.store.backend "sqlite" -}}
  {{- if ne (int .Values.store.replicas) 1 -}}
  {{- fail (printf "%s\n%s\n"
    (printf "flanj-collector: store.replicas must be 1 with store.backend=sqlite (got %v)." .Values.store.replicas)
    "  Exactly one pod may own a sqlite file. Scale the store tier by moving to store.backend=postgres; scale INGEST with collector.replicas or collector.autoscaling, which is the tier that is meant to grow.") -}}
  {{- end -}}
  {{- if .Values.store.sqliteImportPath -}}
  {{- fail "flanj-collector: store.sqliteImportPath is the one-shot import of a legacy sqlite file into postgres — it has no meaning with store.backend=sqlite. Use store.persistence.existingClaim to keep an existing volume." -}}
  {{- end -}}
{{- end -}}

{{- if eq .Values.store.backend "postgres" -}}
  {{- if .Values.store.persistence.enabled -}}
  {{- fail (printf "%s\n%s\n"
    "flanj-collector: store.persistence.enabled must be false with store.backend=postgres."
    "  The evidence lives in the database; a PVC here would be an empty volume that looks like durability. Set store.persistence.enabled=false.") -}}
  {{- end -}}
  {{- if and .Values.store.dsn.value .Values.store.dsn.existingSecret -}}
  {{- fail "flanj-collector: set store.dsn.value OR store.dsn.existingSecret, not both." -}}
  {{- end -}}
  {{- if and .Values.postgres.enabled (or .Values.store.dsn.value .Values.store.dsn.existingSecret) -}}
  {{- fail "flanj-collector: postgres.enabled bundles a database AND store.dsn points at one. Pick a source: drop store.dsn to use the bundled evaluation database, or set postgres.enabled=false to use yours." -}}
  {{- end -}}
  {{- if not (or .Values.store.dsn.value .Values.store.dsn.existingSecret .Values.postgres.enabled) -}}
  {{- fail (printf "%s\n%s\n"
    "flanj-collector: store.backend=postgres needs a database."
    "  Set store.dsn.value / store.dsn.existingSecret to your own, or postgres.enabled=true for the bundled evaluation one (one replica, no backups — not for production).") -}}
  {{- end -}}
{{- end -}}

{{- if .Values.postgres.enabled -}}
  {{- if ne .Values.store.backend "postgres" -}}
  {{- fail (printf "flanj-collector: postgres.enabled=true with store.backend=%q would run a database nothing writes to. Set store.backend=postgres (and store.persistence.enabled=false)." .Values.store.backend) -}}
  {{- end -}}
  {{- if not .Values.postgres.password -}}
  {{- fail (printf "%s\n%s\n"
    "flanj-collector: postgres.password is required when postgres.enabled=true."
    "  It is deliberately not generated: a password regenerated on the next `helm upgrade` locks the store out of its own database, and the failure looks like a database outage.") -}}
  {{- end -}}
{{- end -}}

{{- if and .Values.controlPlane.deployToken.value .Values.controlPlane.deployToken.existingSecret -}}
{{- fail "flanj-collector: set controlPlane.deployToken.value OR controlPlane.deployToken.existingSecret, not both." -}}
{{- end -}}

{{- if .Values.collector.autoscaling.enabled -}}
  {{- if and (eq (int .Values.collector.autoscaling.targetCPUUtilizationPercentage) 0) (eq (int .Values.collector.autoscaling.targetMemoryUtilizationPercentage) 0) -}}
  {{- fail "flanj-collector: collector.autoscaling.enabled needs at least one target — set targetCPUUtilizationPercentage and/or targetMemoryUtilizationPercentage above 0. An HPA with no metric never scales." -}}
  {{- end -}}
  {{- if gt (int .Values.collector.autoscaling.minReplicas) (int .Values.collector.autoscaling.maxReplicas) -}}
  {{- fail "flanj-collector: collector.autoscaling.minReplicas exceeds maxReplicas." -}}
  {{- end -}}
{{- end -}}

{{- end -}}
