{{/*
Common labels for all resources.
*/}}
{{- define "workspace-operator.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end }}

{{/*
Selector labels for the operator.
*/}}
{{- define "workspace-operator.operatorSelectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
Selector labels for the gateway.
*/}}
{{- define "workspace-operator.gatewaySelectorLabels" -}}
app: workspace-gateway
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
