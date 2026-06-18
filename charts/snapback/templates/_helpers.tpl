{{/*
Copyright 2024 Defense Unicorns
SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial
*/}}

{{- define "snapback.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fullname is role-suffixed so a source and a destination release can coexist in a
namespace (and so the stable pod DNS reflects the role).
*/}}
{{- define "snapback.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s-%s" $name .Values.role | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "snapback.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "snapback.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: {{ .Values.role }}
{{- end -}}

{{- define "snapback.selectorLabels" -}}
app.kubernetes.io/name: {{ include "snapback.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: {{ .Values.role }}
{{- end -}}

{{- define "snapback.serviceAccountName" -}}
{{ include "snapback.fullname" . }}
{{- end -}}

{{/*
Name of the Secret holding the peat Iroh shared key.
*/}}
{{- define "snapback.sharedKeySecretName" -}}
{{- if .Values.peat.existingSharedKeySecret -}}
{{ .Values.peat.existingSharedKeySecret }}
{{- else -}}
{{ include "snapback.name" . }}-peat-shared-key
{{- end -}}
{{- end -}}

{{/*
Validate role early with a clear message.
*/}}
{{- define "snapback.validateRole" -}}
{{- if not (or (eq .Values.role "source") (eq .Values.role "destination")) -}}
{{- fail (printf "snapback: .Values.role must be \"source\" or \"destination\", got %q" .Values.role) -}}
{{- end -}}
{{- end -}}
