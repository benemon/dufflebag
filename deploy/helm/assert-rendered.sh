#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
chart="$repo/deploy/helm/dufflebag"
work=$(mktemp -d "${TMPDIR:-/tmp}/dufflebag-helm-assert.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM

fail() {
	printf '[helm-assert] FAIL: %s\n' "$1" >&2
	exit 1
}

helm template dufflebag "$chart" --namespace dufflebag > "$work/default.yaml"
helm template dufflebag "$chart" --namespace dufflebag --set route.enabled=true > "$work/route.yaml"

awk 'BEGIN { RS="---" } /kind: Deployment/ && /app.kubernetes.io\/component: dufflebag/ { print }' \
	"$work/default.yaml" > "$work/deployment.yaml"
[ -s "$work/deployment.yaml" ] || fail "dufflebag Deployment was not rendered"

grep -q 'name: migrate' "$work/deployment.yaml" || fail "migrate init container is absent"
grep -q 'key: admin-database-url' "$work/deployment.yaml" || fail "migrate does not use the admin DSN"
for variable in DFBG_KEY_PROVIDER VAULT_ADDR DFBG_VAULT_AUTH_METHOD DFBG_VAULT_K8S_ROLE DFBG_VAULT_K8S_MOUNT DFBG_VAULT_K8S_TOKEN_PATH; do
	grep -q -- "- name: $variable" "$work/deployment.yaml" || fail "$variable is absent from the dufflebag Deployment"
done
if grep -q -- '- name: VAULT_TOKEN' "$work/deployment.yaml"; then
	fail "dufflebag env contains VAULT_TOKEN"
fi

awk 'BEGIN { RS="---" } /kind: Job/ && /name: dufflebag-vault-bootstrap/ { print }' \
	"$work/default.yaml" > "$work/vault-job.yaml"
[ -s "$work/vault-job.yaml" ] || fail "Vault bootstrap Job was not rendered"
for contract in \
	'vault secrets enable -path=transit transit' \
	'vault auth enable -path=kubernetes kubernetes' \
	'path "transit/encrypt/dufflebag"' \
	'path "transit/decrypt/dufflebag"' \
	'path "transit/rewrap/dufflebag"' \
	'vault policy write dufflebag' \
	'vault write auth/kubernetes/role/dufflebag'; do
	grep -qF "$contract" "$work/vault-job.yaml" || fail "Vault bootstrap omits: $contract"
done

if grep -q '^kind: Route$' "$work/default.yaml"; then
	fail "Route rendered while route.enabled=false"
fi
grep -q '^kind: Route$' "$work/route.yaml" || fail "Route did not render while route.enabled=true"

if grep -Eq '(^|[[:space:]])(runAsUser|runAsGroup|fsGroup):' "$work/default.yaml" "$work/route.yaml"; then
	fail "rendered workload pins a UID or GID"
fi


# The openshift profile restores the strict restricted-v2 posture everywhere:
# no added capabilities may render, while the default profile is allowed the
# documented setuid set for root-start images.
helm template dufflebag "$chart" --namespace dufflebag --set security.openshift=true > "$work/openshift.yaml"
grep -q 'add:' "$work/openshift.yaml" && fail "openshift profile renders added capabilities"
grep -q 'SETUID' "$work/openshift.yaml" && fail "openshift profile renders SETUID"
grep -q 'add:' "$work/default.yaml" || fail "default profile lost the postgres capability adds"

printf '[helm-assert] PASS: migration, encrypted Vault auth, bootstrap, Route gating, UID and capability-profile contracts\n'
