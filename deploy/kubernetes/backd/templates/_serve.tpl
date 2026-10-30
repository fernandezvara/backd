{{/*
One Deployment and one Service of `backd serve`. The three differ by a few settings:

  backd           the public API: no admin API, no change stream
  backd-realtime  the only instances that read the change stream (realtime) and hold the trigger lease
  backd-admin     the admin API and interface

(dict "root" $ "component" "backd" "key" "backd" "admin" false "ui" false "events" false "triggers" false)
*/}}
{{- define "backd.serve" -}}
{{- $root := .root -}}
{{- $ctx := dict "root" $root "component" .component -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .component }}
  labels:
    {{- include "backd.labels" $ctx | nindent 4 }}
spec:
  replicas: {{ include "backd.replicas" (dict "root" $root "key" .key) }}
  revisionHistoryLimit: 5
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  selector:
    matchLabels:
      {{- include "backd.selector" $ctx | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "backd.labels" $ctx | nindent 8 }}
        backd.fernandezvara.dev/serves: "true"
    spec:
      automountServiceAccountToken: false
      terminationGracePeriodSeconds: {{ $root.Values.terminationGracePeriodSeconds }}
      {{- with $root.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        {{- include "backd.podSecurityContext" (dict "uid" $root.Values.runAsUser) | nindent 8 }}
      {{- include "backd.scheduling" $ctx | nindent 6 }}
      containers:
        - name: backd
          image: {{ include "backd.configImage" $root }}
          imagePullPolicy: {{ $root.Values.image.pullPolicy }}
          args: [serve]
          ports:
            - {name: http, containerPort: 8080}
            - {name: internal, containerPort: 8081}
            - {name: metrics, containerPort: 9090}
          env:
            {{- include "backd.env.common" $root | nindent 12 }}
            {{- include "backd.env.mongo" $root | nindent 12 }}
            - name: TRUSTED_PROXIES
              value: {{ $root.Values.trustedProxies | quote }}
            - name: MAX_BODY_BYTES
              value: {{ $root.Values.maxBodyBytes | quote }}
            # Functions: the executor, and the listener it and the functions call back on.
            - name: BACKD_EXECUTOR_URL
              value: http://executor:9100
            - name: BACKD_EXECUTOR_TOKEN
              {{- include "backd.fromSecret" (dict "name" $root.Values.secrets.keys "key" "BACKD_EXECUTOR_TOKEN") | nindent 14 }}
            - name: BACKD_INTERNAL_ADDR
              value: ":8081"
            - name: BACKD_CALLBACK_URL
              value: http://{{ .component }}:8081
            - name: BACKD_CALLBACK_KEY
              {{- include "backd.fromSecret" (dict "name" $root.Values.secrets.keys "key" "BACKD_CALLBACK_KEY") | nindent 14 }}
            - name: BACKD_SECRETS_KEY
              {{- include "backd.fromSecret" (dict "name" $root.Values.secrets.keys "key" "BACKD_SECRETS_KEY") | nindent 14 }}
            - name: BACKD_ADMIN_API
              value: {{ .admin | quote }}
            - name: BACKD_ADMIN_UI
              value: {{ .ui | quote }}
            - name: BACKD_EVENTS
              value: {{ .events | quote }}
            - name: BACKD_TRIGGERS
              value: {{ .triggers | quote }}
          startupProbe:
            httpGet: {path: /healthz, port: http}
            periodSeconds: 2
            failureThreshold: 60
          livenessProbe:
            httpGet: {path: /healthz, port: http}
            periodSeconds: 10
            timeoutSeconds: 3
          readinessProbe:
            httpGet: {path: /readyz, port: http}
            periodSeconds: 5
            timeoutSeconds: 3
          resources:
            {{- toYaml (index $root.Values.resources .key) | nindent 12 }}
          securityContext:
            {{- include "backd.containerSecurityContext" . | nindent 12 }}
          volumeMounts:
            {{- include "backd.mongoCAMount" . | nindent 12 }}
      volumes:
        {{- include "backd.mongoCAVolume" $root | nindent 8 }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .component }}
  labels:
    {{- include "backd.labels" $ctx | nindent 4 }}
spec:
  type: ClusterIP
  selector:
    {{- include "backd.selector" $ctx | nindent 4 }}
  ports:
    - {name: http, port: 8080, targetPort: http}
    # The internal listener: bundles for the executor and the data routes for functions. Never
    # published (the ingress routes only the http port; a network policy limits who connects).
    - {name: internal, port: 8081, targetPort: internal}
{{- if gt (int (include "backd.replicas" (dict "root" $root "key" .key))) 1 }}{{ if $root.Values.podDisruptionBudget.enabled }}
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ .component }}
  labels:
    {{- include "backd.labels" $ctx | nindent 4 }}
spec:
  maxUnavailable: 1
  selector:
    matchLabels:
      {{- include "backd.selector" $ctx | nindent 6 }}
{{- end }}{{ end }}
{{- end }}
