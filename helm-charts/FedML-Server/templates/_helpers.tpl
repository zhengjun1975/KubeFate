{{/*
Expand the name of the chart.
*/}}
{{- define "fedml-edge-server.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "fedml-edge-server.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "fedml-edge-server.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "fedml-edge-server.labels" -}}
helm.sh/chart: {{ include "fedml-edge-server.chart" . }}
{{ include "fedml-edge-server.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
owner: kubefate
cluster: fedml-server
heritage: {{ .Release.Service }}
release: {{ .Release.Name }}
chart: {{ .Chart.Name }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "fedml-edge-server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "fedml-edge-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
name: {{ .Release.Name | quote  }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "fedml-edge-server.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "fedml-edge-server.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
