{{- define "miudinho-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "miudinho-agent.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- include "miudinho-agent.name" . -}}
{{- end -}}
{{- end -}}

{{- define "miudinho-agent.labels" -}}
app.kubernetes.io/name: {{ include "miudinho-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "miudinho-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "miudinho-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "miudinho-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "miudinho-agent.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "miudinho-agent.secretName" -}}
{{- if .Values.secret.name -}}
{{- .Values.secret.name -}}
{{- else -}}
{{- printf "%s-secrets" (include "miudinho-agent.fullname" .) -}}
{{- end -}}
{{- end -}}
