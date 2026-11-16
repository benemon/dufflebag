# Backup and restore

A dufflebag backup is a backup of its PostgreSQL database, plus its object
storage bucket when one is configured. This page states what any backup must
capture and the constraints it runs under, then gives one tested procedure.
Use whichever backup mechanism your database platform provides, as long as it
meets these requirements.

## What a backup must capture

- **The whole database, across every tenant.** dufflebag forces row-level
  security on its tenant tables, owner included, so the
  [serving role](./database.md#the-role-model) sees one tenant at a time. A
  `pg_dump` run as that role stops with `query would be affected by row-level
  security policy` rather than write a partial dump. Back up as something
  row-level security does not bind: a PostgreSQL superuser, a role with
  `BYPASSRLS` used only for backups, or a physical backup that copies the
  database below SQL.
- **The object storage bucket, if one is configured.** SBOM bytes and scan
  transcripts live in the bucket, and the database holds only references to
  them. Back up the bucket together with the database, with no writes between
  the two. A database restored without its bucket references objects that are
  not there.

A database backup does not contain PostgreSQL roles, Kubernetes Secrets or
deployment configuration. On an encrypted deployment it also does not contain
the key service's key, without which the restored data cannot be decrypted
([Encryption](./encryption.md)). Keep all of these separately.

## Constraints

- Do not run backups as the serving role, and do not grant it superuser or
  `BYPASSRLS` to make backups work. The server refuses to start with either,
  because both disable the tenancy boundary.
- The database owner role must exist, with the same name, before a restore.
- Point-in-time recovery depends on the mechanism. A physical backup with
  continuous WAL archiving can restore to any moment it covers. A logical dump
  restores to the moment it was taken.
- A backup is proven only by restoring it. Restore into a scratch database on a
  schedule and run the checks in [Verify](#verify).

## Platform backups

Database platforms provide backups that run outside row-level security by
design. On Kubernetes, for example, CloudNativePG's
[backup and recovery](https://cloudnative-pg.io/documentation/current/backup/)
takes scheduled base backups and archives WAL to object storage, with
point-in-time recovery. Other operators, the automated backups of managed
PostgreSQL services, and pgBackRest, Barman, WAL-G or `pg_basebackup` on your
own hosts serve the same purpose. Configure them from their own documentation.
A managed service that withholds full superuser may not let any role bypass
forced row-level security, so check before relying on a logical dump there.

## A tested procedure: logical dump

This procedure takes a whole-database logical dump as the PostgreSQL superuser.
It is the one exercised by `make test-backup-restore` in the repository's KIND
lane. The reference Kubernetes manifests do not configure object storage, so in
that shape `/sys/health` reports object storage as `unconfigured` and there is
no bucket to back up.

### Back up

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

Custom format retains ownership metadata and lets `pg_restore` stop at the
first error. Protect the archive as production data.

### Restore

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

### Verify

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

This procedure restores the database to the moment of the dump.
