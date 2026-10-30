{{/* Labels and selectors. Called with (dict "root" $ "component" "backd"). */}}
{{- define "backd.selector" -}}
app.kubernetes.io/name: backd
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "backd.labels" -}}
{{ include "backd.selector" . }}
app.kubernetes.io/part-of: backd
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" }}
{{- end }}

{{/* Pods of a component: its own number, else replicas.default. (dict "root" $ "key" "backdAdmin") */}}
{{- define "backd.replicas" -}}
{{- $r := .root.Values.replicas -}}
{{- $v := index $r .key -}}
{{- if kindIs "invalid" $v -}}{{ $r.default }}{{- else -}}{{ $v }}{{- end -}}
{{- end }}

{{/* Images. */}}
{{- define "backd.configImage" -}}
{{ required "image.repository is required: the configuration image, <registry>/<name>" .Values.image.repository }}:{{ required "image.tag is required: the release of the configuration image" .Values.image.tag }}
{{- end }}

{{- define "backd.releaseTag" -}}
{{ .Values.releaseImages.tag | default .Chart.AppVersion }}
{{- end }}

{{- define "backd.egressImage" -}}
{{ .Values.releaseImages.backd }}:{{ include "backd.releaseTag" . }}
{{- end }}

{{- define "backd.executorImage" -}}
{{ .Values.releaseImages.executor }}:{{ include "backd.releaseTag" . }}
{{- end }}

{{/* Restricted pod security: non-root, read-only root filesystem, no capabilities. (dict "uid" 65532) */}}
{{- define "backd.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: {{ .uid }}
runAsGroup: {{ .uid }}
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "backd.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end }}

{{/* Scheduling shared by every pod. (dict "root" $ "component" "worker") */}}
{{- define "backd.scheduling" -}}
{{- with .root.Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .root.Values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- if .root.Values.spreadOverNodes }}
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        {{- include "backd.selector" . | nindent 8 }}
{{- end }}
{{- end }}

{{/* A value from the install ConfigMap. (dict "root" $ "key" "BACKD_URL") */}}
{{- define "backd.fromInstall" -}}
valueFrom:
  configMapKeyRef:
    name: {{ .root.Values.install }}
    key: {{ .key }}
{{- end }}

{{/* A value from a Secret. (dict "name" "backd-keys" "key" "BACKD_SECRETS_KEY") */}}
{{- define "backd.fromSecret" -}}
valueFrom:
  secretKeyRef:
    name: {{ .name }}
    key: {{ .key }}
{{- end }}

{{/* Environment of every backd process (serve, worker, provision). */}}
{{- define "backd.env.common" -}}
- name: CONFIG_DIR
  value: /config
- name: LOG_LEVEL
  value: {{ .Values.logLevel | quote }}
- name: BACKD_URL
  {{- include "backd.fromInstall" (dict "root" . "key" "BACKD_URL") | nindent 2 }}
- name: BACKD_ENV
  {{- include "backd.fromInstall" (dict "root" . "key" "BACKD_ENV") | nindent 2 }}
- name: BACKD_DEPLOYMENT_ID
  {{- include "backd.fromInstall" (dict "root" . "key" "BACKD_DEPLOYMENT_ID") | nindent 2 }}
- name: METRICS_ADDR
  value: ":9090"
- name: METRICS_TOKEN
  {{- include "backd.fromSecret" (dict "name" .Values.secrets.keys "key" "BACKD_METRICS_TOKEN") | nindent 2 }}
{{- range $k, $v := .Values.env }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end }}
{{- end }}

{{/* The database for the pods that serve: the application user, and verify mode (they never change the schema). */}}
{{- define "backd.env.mongo" -}}
- name: MONGO_URI
  {{- include "backd.fromSecret" (dict "name" .Values.secrets.mongo "key" "MONGO_URI") | nindent 2 }}
- name: PROVISION_MODE
  value: verify
{{- end }}

{{/* The managed MongoDB's CA, which MONGO_URI names (tlsCAFile=/etc/backd/tls/ca.pem). */}}
{{- define "backd.mongoCAVolume" -}}
- name: mongo-ca
  secret:
    secretName: {{ .Values.secrets.mongo }}
    items:
      - key: ca.pem
        path: ca.pem
{{- end }}

{{- define "backd.mongoCAMount" -}}
- name: mongo-ca
  mountPath: /etc/backd/tls
  readOnly: true
{{- end }}
