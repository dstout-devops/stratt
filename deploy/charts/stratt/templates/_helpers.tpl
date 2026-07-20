{{/* Standard names */}}
{{- define "stratt.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "stratt.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "stratt.labels" -}}
app.kubernetes.io/name: {{ include "stratt.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "stratt.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stratt.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Image ref: digest wins over tag (§7.3 — pin digests in production). */}}
{{- define "stratt.image" -}}
{{- if .digest -}}
{{- printf "%s@%s" .repository .digest -}}
{{- else -}}
{{- printf "%s:%s" .repository .tag -}}
{{- end -}}
{{- end -}}

{{/* OpenFGA URL: subchart service when enabled, else the external value. */}}
{{- define "stratt.openfgaURL" -}}
{{- if .Values.openfga.enabled -}}
{{- printf "http://%s-openfga:8080" .Release.Name -}}
{{- else -}}
{{- .Values.externalOpenfga.url -}}
{{- end -}}
{{- end -}}

{{/* Database env (STRATT_DATABASE_URL) — one definition shared by the serving
     Deployment and the UPG-1 pre-upgrade migration Job, so they never drift. */}}
{{- define "stratt.databaseEnv" -}}
{{- if .Values.database.existingSecret.name }}
- name: STRATT_DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.database.existingSecret.name }}
      key: {{ .Values.database.existingSecret.key }}
{{- else if .Values.database.url }}
- name: STRATT_DATABASE_URL
  value: {{ .Values.database.url | quote }}
{{- end }}
{{- end -}}
