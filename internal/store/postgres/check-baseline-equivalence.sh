#!/bin/sh
set -eu

repo=$(git rev-parse --show-toplevel)
work=$(mktemp -d)
container="dufflebag-baseline-equivalence-$$"
cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

old_ref=${DUFFLEBAG_OLD_MIGRATIONS_REF:-}
if [ -z "$old_ref" ]; then
	for candidate in HEAD HEAD^; do
		if git cat-file -e "$candidate:internal/store/postgres/migrations/000029_bucket_scope_enforcement.up.sql" 2>/dev/null; then
			old_ref=$candidate
			break
		fi
	done
fi
if [ -z "$old_ref" ]; then
	echo "cannot find the pre-0.1.0 migration chain; set DUFFLEBAG_OLD_MIGRATIONS_REF" >&2
	exit 1
fi

mkdir "$work/old-source"
git archive "$old_ref" | tar -x -C "$work/old-source"
(
	cd "$work/old-source"
	go build -o "$work/dufflebag-old" ./cmd/dufflebag
)
(
	cd "$repo"
	go build -o "$work/dufflebag-baseline" ./cmd/dufflebag
)

docker run -d --name "$container" \
	-e POSTGRES_PASSWORD=postgres -p 127.0.0.1::5432 postgres:17-alpine >/dev/null
for _ in $(seq 1 60); do
	if docker exec "$container" pg_isready -U postgres -d postgres >/dev/null 2>&1; then
		break
	fi
	sleep 1
done
docker exec "$container" pg_isready -U postgres -d postgres >/dev/null
docker exec "$container" createdb -U postgres old_chain
docker exec "$container" createdb -U postgres baseline

port=$(docker port "$container" 5432/tcp | sed 's/.*://')
DFBG_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:$port/old_chain?sslmode=disable" \
	"$work/dufflebag-old" migrate
DFBG_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:$port/baseline?sslmode=disable" \
	"$work/dufflebag-baseline" migrate

for database in old_chain baseline; do
	docker exec "$container" pg_dump --schema-only --no-owner --no-privileges \
		--no-comments --username=postgres --dbname="$database" \
		--file="/tmp/$database.sql"
	docker cp "$container:/tmp/$database.sql" "$work/$database.sql" >/dev/null
	# PostgreSQL 17 emits a fresh psql safety token for every dump. It has no
	# schema meaning and is the only normalized difference.
	sed '/^\\restrict /d; /^\\unrestrict /d' "$work/$database.sql" >"$work/$database.normalized.sql"
done

echo "equivalence command: diff -u old_chain.normalized.sql baseline.normalized.sql"
if ! diff -u "$work/old_chain.normalized.sql" "$work/baseline.normalized.sql"; then
	echo "baseline schema differs from $old_ref" >&2
	exit 1
fi
echo "equivalence diff: empty (old migrations from $old_ref)"
