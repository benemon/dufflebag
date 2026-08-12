{{- define "dufflebag.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dufflebag.labels" -}}
app.kubernetes.io/name: dufflebag
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "dufflebag.containerSecurityContext" -}}
allowPrivilegeEscalation: false
capabilities:
  drop:
    - ALL
{{- end -}}

{{/*
Root-start images (postgres, ceph-aio) drop privileges themselves via
setuid, which "drop ALL" forbids — they crash instantly on plain
Kubernetes. The openshift profile restores the strict posture because
restricted-v2 runs them at an arbitrary non-root UID where no setuid
happens.
*/}}
{{- define "dufflebag.postgresSecurityContext" -}}
{{- if .Values.security.openshift -}}
allowPrivilegeEscalation: false
capabilities:
  drop:
    - ALL
{{- else -}}
allowPrivilegeEscalation: false
capabilities:
  drop:
    - ALL
  add:
    - CHOWN
    - DAC_OVERRIDE
    - FOWNER
    - SETGID
    - SETUID
{{- end -}}
{{- end -}}

{{/*
ceph-aio's bootstrap chowns unconditionally, so it must run as root on every
platform; the openshift profile pairs this with an anyuid SCC RoleBinding for
its ServiceAccount rather than pretending drop-ALL could hold.
*/}}
{{- define "dufflebag.cephSecurityContext" -}}
allowPrivilegeEscalation: false
{{- end -}}

