#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cluster="dufflebag-$PPID-$$"
image="dufflebag-kind:$PPID-$$"
work=$(mktemp -d "${TMPDIR:-/tmp}/dufflebag-kind.XXXXXX")
port_forward_pid=
export KUBECONFIG="$work/kubeconfig"

cleanup() {
	if [ -n "$port_forward_pid" ]; then
		kill "$port_forward_pid" >/dev/null 2>&1 || true
		wait "$port_forward_pid" 2>/dev/null || true
	fi
	kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT HUP INT TERM

progress() {
	printf '[test-kind] %s\n' "$1"
}

fail() {
	printf '[test-kind] FAIL: %s\n' "$1" >&2
	exit 1
}

progress "creating KIND cluster $cluster"
kind create cluster --name "$cluster" --wait 120s

progress "building the real dufflebag image from Containerfile"
docker build -f "$repo/Containerfile" -t "$image" "$repo"

progress "loading dufflebag and PostgreSQL images into KIND"
kind load docker-image --name "$cluster" "$image"
# postgres:17-alpine is pulled by the node from the network: kind-loading a
# registry image trips containerd's multi-arch digest import under Docker
# Desktop ("content digest ... not found"); only the locally built dufflebag
# image, which exists in no registry, needs loading.

progress "starting PostgreSQL"
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: postgres
  labels:
    app: postgres
spec:
  containers:
    - name: postgres
      image: postgres:17-alpine
      imagePullPolicy: IfNotPresent
      env:
        - name: POSTGRES_PASSWORD
          value: postgres
      ports:
        - name: postgres
          containerPort: 5432
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
spec:
  selector:
    app: postgres
  ports:
    - name: postgres
      port: 5432
      targetPort: postgres
EOF
kubectl wait --for=condition=Ready pod/postgres --timeout=120s
# Pod Ready means the container runs, not that postgres accepts connections;
# poll pg_isready before bootstrapping roles.
for _ in $(seq 1 60); do
  kubectl exec postgres -- pg_isready -U postgres >/dev/null 2>&1 && break
  sleep 1
done
kubectl exec postgres -- pg_isready -U postgres

progress "creating the database-owner role"
kubectl exec postgres -- psql -v ON_ERROR_STOP=1 -U postgres -d postgres \
	-c 'CREATE DATABASE dufflebag' \
	-c "CREATE ROLE dufflebag LOGIN PASSWORD 'app' NOSUPERUSER NOBYPASSRLS" \
	-c 'ALTER DATABASE dufflebag OWNER TO dufflebag'

progress "applying generated secrets and the reference manifests"
kubectl create secret generic dufflebag \
	--from-literal=database-url='postgres://dufflebag:app@postgres/dufflebag?sslmode=disable' \
	--from-literal=token-signing-key='kind-validation-signing-key-at-least-32-bytes' \
	--dry-run=client -o yaml | kubectl apply -f -
sed "s|quay.io/benjamin_holmes/dufflebag:<tag>|$image|g" \
	"$repo/deploy/kubernetes/deployment.yaml" > "$work/deployment.yaml"
kubectl apply -f "$work/deployment.yaml" -f "$repo/deploy/kubernetes/service.yaml"

progress "waiting for the dufflebag pod to enter Running"
kubectl wait --for=jsonpath='{.status.phase}'=Running pod -l app=dufflebag --timeout=120s
pod=$(kubectl get pod -l app=dufflebag -o jsonpath='{.items[0].metadata.name}')
[ -n "$pod" ] || fail "no dufflebag pod was created"

progress "port-forwarding directly to $pod while its Service has no ready endpoint"
kubectl port-forward "pod/$pod" :8080 > "$work/port-forward.log" 2>&1 &
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

progress "proving an unclaimed instance answers 501 and is NotReady"
health_code=$(curl -sS -o "$work/health-before.json" -w '%{http_code}' "$base/sys/health")
[ "$health_code" = 501 ] || fail "/sys/health before initialization answered $health_code, want 501"
ready=$(kubectl get "pod/$pod" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
[ "$ready" = False ] || fail "unclaimed pod Ready condition is $ready, want False"

progress "initializing the instance through the real first-run endpoint"
init_code=$(curl -sS -o "$work/init.json" -w '%{http_code}' -X POST \
	-H 'content-type: application/json' -d '{}' "$base/sys/init")
[ "$init_code" = 200 ] || fail "POST /sys/init answered $init_code, want 200"

progress "waiting for /sys/health to answer 200"
health_code=
i=0
while [ "$i" -lt 60 ]; do
	health_code=$(curl -sS -o "$work/health-after.json" -w '%{http_code}' "$base/sys/health")
	[ "$health_code" = 200 ] && break
	i=$((i + 1))
	sleep 1
done
[ "$health_code" = 200 ] || fail "/sys/health after initialization answered $health_code, want 200"

progress "waiting for the pod to become Ready"
if ! kubectl wait --for=condition=Ready "pod/$pod" --timeout=90s; then
	kubectl describe "pod/$pod" >&2
	fail "pod did not become Ready after initialization"
fi

progress "PASS: Running, pre-init 501/NotReady, initialization, and post-init 200/Ready all verified"
