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

{{/*
Image ref: digest wins over tag (§7.3 — pin digests in production).

The requireDigests GATE is not here: this helper receives an image dict, so it cannot see
.Values, and threading the chart root through every call site would touch a dozen templates to
check one flag. See stratt.requireDigests below — one pass, easier to audit, which is what a
supply-chain gate needs to be.
*/}}
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

{{/*
Refuse to render when any image is pinned by TAG rather than digest (ADR-0168, §7.3).

The chart has said "pin digests in production" since ADR-0013 — in a NOTES.txt warning. A warning
does not stop a deployment, and nothing has ever failed because an image was floating. This makes
the promise checkable by `helm template`, the one tool every install already runs.

Deliberately opt-IN: every dev floor and demo in this repo runs on floating tags by design, so a
default-on refusal would break them all — which is how a control ends up switched off globally and
left that way.
*/}}
{{- define "stratt.requireDigests" -}}
{{- if .Values.supplyChain.requireDigests -}}
{{- $unpinned := list -}}
{{- range $name, $img := dict "strattd" .Values.image "agent" (default (dict) .Values.agent).image "forwarder" (default (dict) .Values.forwarder).image -}}
{{- if and $img $img.repository (not $img.digest) -}}
{{- $unpinned = append $unpinned (printf "%s (%s:%s)" $name $img.repository (default "" $img.tag)) -}}
{{- end -}}
{{- end -}}
{{- range $p := .Values.plugins -}}
{{- if and $p.image $p.image.repository (not $p.image.digest) -}}
{{- $unpinned = append $unpinned (printf "plugin/%s (%s:%s)" $p.name $p.image.repository (default "" $p.image.tag)) -}}
{{- end -}}
{{- end -}}
{{- if $unpinned -}}
{{- fail (printf "supplyChain.requireDigests: %d image(s) pinned by TAG, not digest: %s. A tag is MUTABLE — the bytes behind it can change after the review that approved them (§7.3, ADR-0168). Pin .digest from a release you verified (task supply:verify), or drop values-production-supply-chain.yaml for a dev floor." (len $unpinned) (join ", " $unpinned)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
