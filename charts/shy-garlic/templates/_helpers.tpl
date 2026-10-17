{{- define "shy-garlic.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "shy-garlic.fullname" -}}
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

{{- define "shy-garlic.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "shy-garlic.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "shy-garlic.selectorLabels" -}}
app.kubernetes.io/name: {{ include "shy-garlic.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "shy-garlic.image" -}}
{{ .Values.image.repository }}:{{ default (printf "%s-git" .Chart.AppVersion) .Values.image.tag }}
{{- end }}

{{- define "shy-garlic.adminSecretName" -}}
{{- if .Values.admin.existingSecret }}{{ .Values.admin.existingSecret }}
{{- else if and .Values.admin.username .Values.admin.password }}{{ include "shy-garlic.fullname" . }}-admin
{{- end }}
{{- end }}
