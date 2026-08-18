# Backup and restore

Back up the single PostgreSQL database as a whole-database logical dump. The
dump contains dufflebag's schema and data. It does not contain PostgreSQL
roles, Kubernetes Secrets or deployment configuration; retain those
separately. The `dufflebag` owner role must exist before a restore.

The reference Kubernetes manifests do not configure object storage. In that
deployment shape, `/sys/health` reports object storage as `unconfigured` and
there are no external SBOM payloads to include in the backup. If you add
[object storage](./object-storage.md), the PostgreSQL dump still does not
contain SBOM bytes or scan transcripts. Back up and restore the S3-compatible
bucket separately and keep it consistent with the database dump. Restoring
only PostgreSQL restores object references, not the objects they name.

## Back up

Stop dufflebag before taking the dump so no writes can land after the backup.
The repository's KIND lane uses the pod, database and role names below. Apply
the same command shape with the names, namespace and PostgreSQL superuser
credential from your deployment. Set `dump` to a protected local path first:

```sh
kubectl scale deployment/dufflebag --replicas=0
kubectl wait --for=delete pod -l app=dufflebag --timeout=120s

kubectl exec postgres -- env PGPASSWORD=postgres pg_dump \
  --host=127.0.0.1 --username=postgres --dbname=dufflebag \
  --format=custom > "$dump"
```

Use a PostgreSQL superuser for the dump. Dufflebag forces row-level security
on tenant tables, so the serving role is not a whole-database backup role.
Custom format retains ownership metadata and lets `pg_restore` stop at the
first error. Protect the archive as production data.

## Restore

Keep dufflebag stopped. Drop and recreate the database, then restore the
custom-format archive. The owner role must already exist:

```sh
kubectl exec postgres -- env PGPASSWORD=postgres psql \
  --host=127.0.0.1 --username=postgres --dbname=postgres \
  -v ON_ERROR_STOP=1 \
  -c 'DROP DATABASE dufflebag' \
  -c 'CREATE DATABASE dufflebag OWNER dufflebag'

kubectl exec -i postgres -- env PGPASSWORD=postgres pg_restore \
  --host=127.0.0.1 --username=postgres --dbname=dufflebag \
  --exit-on-error < "$dump"

kubectl scale deployment/dufflebag --replicas=1
kubectl wait --for=jsonpath='{.status.phase}'=Running pod -l app=dufflebag --timeout=120s
```

Starting a new pod also replaces the old database connection pool.

## Verify

Use an existing authenticated API session to fetch a bucket that was present
when the dump was taken. In the commands below, `registry` is the tenant's
Packer API base URL, `bucket_name` is the known bucket, `token` is a valid
access token and `base` is the dufflebag base URL. Set `restored_bucket` and
`restored_health` to local temporary paths:

```sh
curl -sSf -o "$restored_health" "$base/sys/health"
grep -Fq '"initialized":true' "$restored_health"
grep -Fq '"database":true' "$restored_health"

curl -sSf -o "$restored_bucket" "$registry/buckets/$bucket_name" \
  -H "authorization: Bearer $token"
grep -Fq "\"name\":\"$bucket_name\"" "$restored_bucket"
```

The health request must return HTTP 200. Wait for the dufflebag pod to become
Ready before returning traffic to it.

Point-in-time recovery is not provided. These backups are whole-database
logical dumps, and this is exactly the restore procedure exercised by
`make test-backup-restore` in the repository's KIND lane.
