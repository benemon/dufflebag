#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cluster="dufflebag-helm-$PPID-$$"
namespace=dufflebag
tag="kind-$PPID-$$"
image="dufflebag-kind:$tag"
work=$(mktemp -d "${TMPDIR:-/tmp}/dufflebag-helm-kind.XXXXXX")
port_forward_pid=
export KUBECONFIG="$work/kubeconfig"

cleanup() {
	if [ -n "$port_forward_pid" ]; then
		kill "$port_forward_pid" >/dev/null 2>&1 || true
		wait "$port_forward_pid" 2>/dev/null || true
	fi
	if [ -n "${KEEP_CLUSTER:-}" ]; then
		printf '[test-helm-kind] KEEP_CLUSTER set: cluster %s and %s retained
' "$cluster" "$work" >&2
		return
	fi
	kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT HUP INT TERM

progress() {
	printf '[test-helm-kind] %s\n' "$1"
}

fail() {
	printf '[test-helm-kind] FAIL: %s\n' "$1" >&2
	kubectl get pods,jobs -n "$namespace" -o wide >&2 2>/dev/null || true
	kubectl describe pods -n "$namespace" >&2 2>/dev/null || true
	for pod in $(kubectl get pods -n "$namespace" -o name 2>/dev/null); do
		printf '=== logs %s ===\n' "$pod" >&2
		kubectl logs -n "$namespace" "$pod" --all-containers --prefix --tail=40 >&2 2>/dev/null || true
		printf '=== previous logs %s ===\n' "$pod" >&2
		kubectl logs -n "$namespace" "$pod" --all-containers --prefix --tail=40 --previous >&2 2>/dev/null || true
	done
	exit 1
}

json_field() {
	python3 -c 'import json,sys; value=json.load(open(sys.argv[1])); [value:=value[p] for p in sys.argv[2].split(".")]; print(value)' "$1" "$2"
}

for command in kind docker kubectl helm curl python3; do
	command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

progress "creating disposable KIND cluster $cluster"
kind create cluster --name "$cluster" --wait 120s

progress "building and loading the current dufflebag image"
docker build -f "$repo/Containerfile" -t "$image" "$repo"
kind load docker-image --name "$cluster" "$image"

progress "installing the self-contained Helm chart"
helm install dufflebag "$repo/deploy/helm/dufflebag" \
	--namespace "$namespace" --create-namespace \
	--set dufflebag.image.repository=dufflebag-kind \
	--set dufflebag.image.tag="$tag"

progress "waiting for Vault bootstrap and the dufflebag pod to run"
kubectl wait -n "$namespace" --for=condition=Complete job/dufflebag-vault-bootstrap --timeout=10m || \
	fail "Vault bootstrap Job did not complete"
kubectl wait -n "$namespace" --for=condition=Ready pod \
	-l app.kubernetes.io/component=dufflebag --timeout=10m || fail "dufflebag pod did not become Ready"
pod=$(kubectl get pod -n "$namespace" -l app.kubernetes.io/component=dufflebag \
	-o jsonpath='{.items[0].metadata.name}')
[ -n "$pod" ] || fail "no dufflebag pod was created"

progress "port-forwarding to $pod for first run"
kubectl port-forward -n "$namespace" "pod/$pod" :8080 > "$work/port-forward.log" 2>&1 &
port_forward_pid=$!
port=
i=0
while [ "$i" -lt 100 ]; do
	port=$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9][0-9]*\) .*/\1/p' "$work/port-forward.log" | head -1)
	[ -n "$port" ] && break
	if ! kill -0 "$port_forward_pid" 2>/dev/null; then
		cat "$work/port-forward.log" >&2
		fail "kubectl port-forward exited before becoming usable"
	fi
	i=$((i + 1))
	sleep 1
done
[ -n "$port" ] || fail "timed out waiting for kubectl port-forward"
base="http://127.0.0.1:$port"

progress "proving health serves and claiming the fresh instance"
# The pod can enter Running seconds before the listener is up (the keyring
# unwrap round-trips to Vault first) — retry health until the server answers.
health_code=
for attempt in $(seq 1 60); do
	health_code=$(curl -sS -o "$work/health-before.json" -w '%{http_code}' "$base/sys/health" 2>/dev/null) && break
	sleep 2
done
[ -n "$health_code" ] || fail "/sys/health never answered through the port-forward"
[ "$health_code" = 501 ] || fail "/sys/health before initialization answered $health_code, want 501"
init_code=$(curl -sS -o "$work/init.json" -w '%{http_code}' -X POST \
	-H 'content-type: application/json' -d '{}' "$base/sys/init")
[ "$init_code" = 200 ] || fail "POST /sys/init answered $init_code, want 200"
client_id=$(json_field "$work/init.json" client_id)
client_secret=$(json_field "$work/init.json" client_secret)
[ -n "$client_id" ] && [ -n "$client_secret" ] || fail "/sys/init returned no credentials"
claim_code=$(curl -sS -o "$work/init-again.json" -w '%{http_code}' -X POST \
	-H 'content-type: application/json' -d '{}' "$base/sys/init")
[ "$claim_code" != 200 ] || fail "a second /sys/init call reclaimed the instance"

curl -sSf -o "$work/token.json" -X POST "$base/oauth2/token" \
	-u "$client_id:$client_secret" -H 'content-type: application/x-www-form-urlencoded' \
	-d 'grant_type=client_credentials&audience=https://api.hashicorp.cloud'
token=$(json_field "$work/token.json" access_token)
[ -n "$token" ] || fail "root token exchange returned no access token"

progress "creating an organisation, project, and project-scoped principal"
curl -sSf -o "$work/org.json" -X POST "$base/api/v1/organizations" \
	-H "authorization: Bearer $token" -H 'content-type: application/json' \
	-d '{"name":"helm-kind"}'
org=$(json_field "$work/org.json" id)
curl -sSf -o "$work/project.json" -X POST "$base/api/v1/organizations/$org/projects" \
	-H "authorization: Bearer $token" -H 'content-type: application/json' \
	-d '{"name":"helm-kind"}'
project=$(json_field "$work/project.json" id)
curl -sSf -o "$work/principal.json" -X POST "$base/api/v1/principals" \
	-H "authorization: Bearer $token" -H 'content-type: application/json' \
	-d "{\"name\":\"helm-kind-builder\",\"role\":\"builder\",\"organization_id\":\"$org\",\"project_id\":\"$project\"}"
principal=$(json_field "$work/principal.json" id)
principal_client=$(json_field "$work/principal.json" client_id)
curl -sSf -o "$work/principal-secret.json" -X POST "$base/api/v1/principals/$principal/secrets" \
	-H "authorization: Bearer $token"
principal_secret=$(json_field "$work/principal-secret.json" secret)
[ -n "$principal_client" ] && [ -n "$principal_secret" ] || fail "principal credential issuance was incomplete"
curl -sSf -o "$work/principal-token.json" -X POST "$base/oauth2/token" \
	-u "$principal_client:$principal_secret" -H 'content-type: application/x-www-form-urlencoded' \
	-d 'grant_type=client_credentials&audience=https://api.hashicorp.cloud'
compat_token=$(json_field "$work/principal-token.json" access_token)
[ -n "$compat_token" ] || fail "project principal token exchange returned no access token"

progress "creating compatibility bucket, version, build, and SBOM"
registry="$base/packer/2023-01-01/organizations/$org/projects/$project"
curl -sSf -o "$work/bucket.json" -X PUT "$registry/buckets" \
	-H "authorization: Bearer $compat_token" -H 'content-type: application/json' \
	-d '{"name":"helm-kind"}'
curl -sSf -o "$work/version.json" -X POST "$registry/buckets/helm-kind/versions" \
	-H "authorization: Bearer $compat_token" -H 'content-type: application/json' \
	-d '{"fingerprint":"helm-kind-v1","template_type":"HCL2"}'
curl -sSf -o "$work/build.json" -X POST "$registry/buckets/helm-kind/versions/helm-kind-v1/builds" \
	-H "authorization: Bearer $compat_token" -H 'content-type: application/json' \
	-d '{"component_type":"docker","status":"BUILD_RUNNING","artifacts":[]}'
build=$(json_field "$work/build.json" build.id)
compressed='KLUv/QRYLQYAggwqJCCNVgdYFOsDpR9vR5URnKrPi1vFyjitjozJV3pjUDEAIQC6AhNHQy/5sRC28WniuALQ1814Qy+jTnfQtRhMQg0sJcAs5vrGEn07qID9zOWILW9ZP+WxD/yci7RlURnnZBslQ7uYkKEI6/Z01x1F1eADUdBjiOsZWvh0xoLAYKE5cJTjBEeRvpSEWk1zPUvI7CnYue3pAQqjGhwEhethPzJihogW5SUBCQBiFAEN5RM1o+6GM0MdYBeYVNtPuSZGUHuRM6+w'
sbom_base="$registry/buckets/helm-kind/versions/helm-kind-v1/builds/$build/sboms"
curl -sSf -o "$work/sbom.json" -X PUT "$sbom_base" \
	-H "authorization: Bearer $compat_token" -H 'content-type: application/json' \
	-d "{\"compressed_sbom\":\"$compressed\",\"format\":\"SPDX\",\"name\":\"helm-kind-sbom\"}"
curl -sSf -o "$work/sbom-get.json" "$sbom_base/helm-kind-sbom" \
	-H "authorization: Bearer $compat_token"
download_path=$(python3 -c 'import json,sys,urllib.parse; print(urllib.parse.urlparse(json.load(open(sys.argv[1]))["download_url"]).path)' "$work/sbom-get.json")
curl -sSf -o "$work/sbom-downloaded.json" "$base$download_path" \
	-H "authorization: Bearer $compat_token"
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["SPDXID"] == "SPDXRef-DOCUMENT"' \
	"$work/sbom-downloaded.json" || fail "downloaded SBOM did not match the uploaded document"

progress "proving the Vault-backed keyring reports healthy"
curl -sSf -o "$work/encryption.json" "$base/api/v1/encryption" \
	-H "authorization: Bearer $token"
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["state"] == "ok" and len(d["keyring"]) == 4' \
	"$work/encryption.json" || fail "Vault-backed encryption state was not healthy"

progress "waiting for the initialized Deployment to become Available"
kubectl wait -n "$namespace" --for=condition=Available deployment/dufflebag --timeout=2m || \
	fail "dufflebag Deployment did not become Available after initialization"

progress "PASS: health/init, tenancy, principal, registry, Ceph SBOM, and Vault keyring verified"
