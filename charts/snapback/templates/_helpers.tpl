{{/*
Copyright 2024 Defense Unicorns
SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial
*/}}

{{- define "snapback.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fullname is role-agnostic — each role lives in its own namespace, so there is no
collision risk for namespace-scoped resources.
*/}}
{{- define "snapback.fullname" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Node ID passed to peat-node as PEAT_NODE_APP_ID. Includes the role so that source
and destination derive distinct iroh endpoint IDs from the same shared key
(endpoint_id = derive-id(sharedKey, nodeId) — must differ across the two roles).
*/}}
{{- define "snapback.nodeId" -}}
{{- printf "%s-%s" (default .Chart.Name .Values.nameOverride) .Values.role | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
ClusterRole / ClusterRoleBinding name. Cluster-scoped, so it must be unique even
when both roles deploy to the same cluster. Append the release namespace (which
is per-role with namespace overrides) to guarantee uniqueness.
*/}}
{{- define "snapback.clusterRoleName" -}}
{{- printf "%s-manager" .Release.Namespace | trunc 63 | trimSuffix "-" -}}
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
Name of the Secret holding the destination object-store credentials.
*/}}
{{- define "snapback.destCredsSecretName" -}}
{{- if .Values.destination.objectStore.credentials.existingSecret -}}
{{ .Values.destination.objectStore.credentials.existingSecret }}
{{- else -}}
{{ include "snapback.name" . }}-dest-creds
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
