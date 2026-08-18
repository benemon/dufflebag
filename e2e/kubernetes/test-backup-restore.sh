#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cluster="dufflebag-backup-$PPID-$$"
image="dufflebag-kind-backup:$PPID-$$"
work=$(mktemp -d "${TMPDIR:-/tmp}/dufflebag-kind-backup.XXXXXX")
dump="$work/dufflebag.dump"
restored_bucket="$work/restored-bucket.json"
restored_health="$work/health-restored.json"
port_forward_pid=
export KUBECONFIG="$work/kubeconfig"

stop_port_forward() {
	if [ -n "$port_forward_pid" ]; then
		kill "$port_forward_pid" >/dev/null 2>&1 || true
		wait "$port_forward_pid" 2>/dev/null || true
		port_forward_pid=
	fi
}

cleanup() {
	stop_port_forward
	kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT HUP INT TERM

progress() {
	printf '[test-backup-restore] %s\n' "$1"
}

fail() {
	printf '[test-backup-restore] FAIL: %s\n' "$1" >&2
	exit 1
}

json_field() {
	python3 -c 'import json,sys; value=json.load(open(sys.argv[1])); [value:=value[p] for p in sys.argv[2].split(".")]; print(value)' "$1" "$2"
}

start_port_forward() {
	progress "port-forwarding directly to $pod"
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
start_port_forward

progress "proving an unclaimed instance answers 501 and is NotReady"
health_code=$(curl -sS -o "$work/health-before.json" -w '%{http_code}' "$base/sys/health")
[ "$health_code" = 501 ] || fail "/sys/health before initialization answered $health_code, want 501"
ready=$(kubectl get "pod/$pod" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
[ "$ready" = False ] || fail "unclaimed pod Ready condition is $ready, want False"

progress "initializing the instance through the real first-run endpoint"
init_code=$(curl -sS -o "$work/init.json" -w '%{http_code}' -X POST \
	-H 'content-type: application/json' -d '{}' "$base/sys/init")
[ "$init_code" = 200 ] || fail "POST /sys/init answered $init_code, want 200"
client_id=$(json_field "$work/init.json" client_id)
client_secret=$(json_field "$work/init.json" client_secret)
[ -n "$client_id" ] && [ -n "$client_secret" ] || fail "/sys/init returned no credentials"

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

progress "creating an organisation, project, and identifiable bucket through the API"
curl -sSf -o "$work/token.json" -X POST "$base/oauth2/token" \
	-u "$client_id:$client_secret" -H 'content-type: application/x-www-form-urlencoded' \
	-d 'grant_type=client_credentials&audience=https://api.hashicorp.cloud'
token=$(json_field "$work/token.json" access_token)
[ -n "$token" ] || fail "root token exchange returned no access token"
curl -sSf -o "$work/org.json" -X POST "$base/api/v1/organizations" \
	-H "authorization: Bearer $token" -H 'content-type: application/json' \
	-d '{"name":"backup-restore-kind"}'
org=$(json_field "$work/org.json" id)
curl -sSf -o "$work/project.json" -X POST "$base/api/v1/organizations/$org/projects" \
	-H "authorization: Bearer $token" -H 'content-type: application/json' \
	-d '{"name":"backup-restore-kind"}'
project=$(json_field "$work/project.json" id)
bucket_name=backup-restore-kind
registry="$base/packer/2023-01-01/organizations/$org/projects/$project"
curl -sSf -o "$work/bucket.json" -X PUT "$registry/buckets" \
	-H "authorization: Bearer $token" -H 'content-type: application/json' \
	-d "{\"name\":\"$bucket_name\"}"
grep -F "\"name\":\"$bucket_name\"" "$work/bucket.json" >/dev/null || \
	fail "created bucket response did not contain $bucket_name"

progress "stopping dufflebag for a quiescent backup and restore"
stop_port_forward
kubectl scale deployment/dufflebag --replicas=0
kubectl wait --for=delete pod -l app=dufflebag --timeout=120s

progress "backing up the dufflebag database in custom format"
kubectl exec postgres -- env PGPASSWORD=postgres pg_dump \
	--host=127.0.0.1 --username=postgres --dbname=dufflebag \
	--format=custom > "$dump"
[ -s "$dump" ] || fail "pg_dump produced an empty archive"

progress "dropping and recreating the dufflebag database"
kubectl exec postgres -- env PGPASSWORD=postgres psql \
	--host=127.0.0.1 --username=postgres --dbname=postgres \
	-v ON_ERROR_STOP=1 \
	-c 'DROP DATABASE dufflebag' \
	-c 'CREATE DATABASE dufflebag OWNER dufflebag'

progress "verifying the recreated database has lost the seeded data"
lost=$(kubectl exec postgres -- env PGPASSWORD=postgres psql \
	--host=127.0.0.1 --username=postgres --dbname=dufflebag \
	--tuples-only --no-align \
	-c "SELECT to_regclass('public.buckets') IS NULL")
[ "$lost" = t ] || fail "buckets table still exists after database recreation"

progress "restoring the custom-format database dump"
kubectl exec -i postgres -- env PGPASSWORD=postgres pg_restore \
	--host=127.0.0.1 --username=postgres --dbname=dufflebag \
	--exit-on-error < "$dump"

progress "starting dufflebag against the restored database"
kubectl scale deployment/dufflebag --replicas=1
kubectl wait --for=jsonpath='{.status.phase}'=Running pod -l app=dufflebag --timeout=120s
pod=$(kubectl get pod -l app=dufflebag -o jsonpath='{.items[0].metadata.name}')
[ -n "$pod" ] || fail "no dufflebag pod was created after restore"
start_port_forward
registry="$base/packer/2023-01-01/organizations/$org/projects/$project"

progress "verifying restored health and readiness"
health_code=
i=0
while [ "$i" -lt 60 ]; do
	health_code=$(curl -sS -o "$restored_health" -w '%{http_code}' "$base/sys/health")
	[ "$health_code" = 200 ] && break
	i=$((i + 1))
	sleep 1
done
[ "$health_code" = 200 ] || fail "/sys/health after restore answered $health_code, want 200"
curl -sSf -o "$restored_health" "$base/sys/health"
if ! grep -Fq '"initialized":true' "$restored_health"; then
	fail "/sys/health after restore did not report initialized"
fi
if ! grep -Fq '"database":true' "$restored_health"; then
	fail "/sys/health after restore did not report a healthy database"
fi
if ! kubectl wait --for=condition=Ready "pod/$pod" --timeout=90s; then
	kubectl describe "pod/$pod" >&2
	fail "pod did not become Ready after restore"
fi

progress "verifying the restored bucket through the API"
curl -sSf -o "$restored_bucket" "$registry/buckets/$bucket_name" \
	-H "authorization: Bearer $token"
if ! grep -Fq "\"name\":\"$bucket_name\"" "$restored_bucket"; then
	fail "restored API response did not contain $bucket_name"
fi

progress "PASS: seeded bucket loss, database restore, API recovery, health, and readiness all verified"
