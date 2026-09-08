{{/*
The store pod's pod template, shared by the two workload kinds it can take:
a StatefulSet with backend=sqlite (one pod, one db file, replicas fixed at 1)
and a Deployment with backend=postgres (the store tier may then scale).

Call with (dict "ctx" $ "claimTemplate" <bool>) — claimTemplate says the
StatefulSet provides the `data` volume itself, so this template must not
declare one.
*/}}
{{- define "flanj-collector.store.podTemplate" -}}
{{- $ := .ctx -}}
metadata:
  annotations:
    checksum/config: {{ include (print $.Template.BasePath "/configmap-store.yaml") $ | sha256sum }}
    {{- with $.Values.commonAnnotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
    {{- with $.Values.store.podAnnotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  labels:
    {{- include "flanj-collector.store.selectorLabels" $ | nindent 4 }}
    {{- with $.Values.commonLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
    {{- with $.Values.store.podLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  serviceAccountName: {{ include "flanj-collector.serviceAccountName" $ }}
  {{- with $.Values.image.pullSecrets }}
  imagePullSecrets:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $.Values.podSecurityContext }}
  securityContext:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $.Values.store.priorityClassName }}
  priorityClassName: {{ . }}
  {{- end }}
  {{- with $.Values.store.nodeSelector }}
  nodeSelector:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $.Values.store.affinity }}
  affinity:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $.Values.store.tolerations }}
  tolerations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  containers:
    - name: collector
      image: {{ include "flanj-collector.image" $ | quote }}
      imagePullPolicy: {{ $.Values.image.pullPolicy }}
      args:
        - --config
        - /etc/flanj/chart/store.yaml
      ports:
        # Intra-cluster ingest from the fronts.
        - name: otlp-http
          containerPort: 4318
          protocol: TCP
        # The contract channel the fronts read. Read-only, contracts-only,
        # bearer-authenticated. NOT the UI — the UI stays on loopback and is
        # deliberately not a container port.
        - name: spec
          containerPort: 5337
          protocol: TCP
      env:
        {{- include "flanj-collector.specTokenEnv" $ | nindent 8 }}
        {{- if or $.Values.controlPlane.deployToken.value $.Values.controlPlane.deployToken.existingSecret }}
        - name: CP_DEPLOY_TOKEN
          valueFrom:
            secretKeyRef:
              name: {{ include "flanj-collector.deployToken.secretName" $ }}
              key: {{ $.Values.controlPlane.deployToken.key }}
        {{- end }}
        {{- if eq $.Values.store.backend "postgres" }}
        - name: FLANJ_PG_DSN
          valueFrom:
            secretKeyRef:
              name: {{ include "flanj-collector.dsn.secretName" $ }}
              key: {{ $.Values.store.dsn.key }}
        {{- end }}
        {{- with $.Values.store.extraEnv }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      # The UI binds 127.0.0.1 by design, so kubelet cannot reach it: probe the
      # OTLP receiver's socket instead (docs/DEPLOYMENT.md).
      startupProbe:
        tcpSocket:
          port: otlp-http
        periodSeconds: 2
        failureThreshold: 30
      readinessProbe:
        tcpSocket:
          port: otlp-http
        periodSeconds: 10
      livenessProbe:
        tcpSocket:
          port: otlp-http
        periodSeconds: 20
        failureThreshold: 3
      {{- with $.Values.securityContext }}
      securityContext:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $.Values.store.resources }}
      resources:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      volumeMounts:
        - name: config
          mountPath: /etc/flanj/chart
          readOnly: true
        - name: tmp
          mountPath: /tmp
        {{- if eq $.Values.store.backend "sqlite" }}
        - name: data
          mountPath: /data
        {{- end }}
        {{- with $.Values.store.extraVolumeMounts }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
  volumes:
    - name: config
      configMap:
        name: {{ include "flanj-collector.store.fullname" $ }}
    - name: tmp
      emptyDir: {}
    {{- if and (eq $.Values.store.backend "sqlite") (not .claimTemplate) }}
    - name: data
      {{- if $.Values.store.persistence.existingClaim }}
      persistentVolumeClaim:
        claimName: {{ $.Values.store.persistence.existingClaim }}
      {{- else }}
      # persistence disabled: the eval tier. The window dies with the pod.
      emptyDir: {}
      {{- end }}
    {{- end }}
    {{- with $.Values.store.extraVolumes }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
{{- end -}}
