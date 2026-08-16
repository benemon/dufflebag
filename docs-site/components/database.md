# Database

Dufflebag stores everything except SBOM payloads in PostgreSQL. Tenancy
isolation is enforced in the database itself with row-level security, which
is why the role model below is a requirement, not a recommendation.

## The role model

Provide a database owned by a role that is neither a superuser nor holds
`BYPASSRLS`. That is the whole requirement — the server creates its schema at
first boot and applies pending migrations itself on every start, so a
single-role setup has no migration step:

```sql
CREATE ROLE dufflebag LOGIN PASSWORD '<password>' NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE dufflebag OWNER dufflebag;
```

The serving role is refused at startup if it is a superuser or holds
`BYPASSRLS`, because either would disable row-level security — the tenancy
boundary — without any error. Table owners are subject to the same policies:
the schema enforces `FORCE ROW LEVEL SECURITY`.

## Hardened: two roles

Splitting migration from serving keeps schema-altering privileges out of the
serving process entirely. With the split, migrations run under the privileged
role in an init container or pre-deploy step of your own deployment — never
by hand. On PostgreSQL 15 and later, the database ownership below is what
confers `CREATE` on schema `public` (earlier versions granted it to every
role by default). Create one role that owns the schema and one that uses it:

```sql
CREATE DATABASE dufflebag;
CREATE ROLE dufflebag_migrate LOGIN PASSWORD '<migration password>';
ALTER DATABASE dufflebag OWNER TO dufflebag_migrate;

CREATE ROLE dufflebag_app LOGIN PASSWORD '<serving password>'
    NOSUPERUSER NOBYPASSRLS;
```

Then, connected to the `dufflebag` database as `dufflebag_migrate` (after the
first migration run, or before it — default privileges cover tables that do
not exist yet):

```sql
GRANT USAGE ON SCHEMA public TO dufflebag_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public
    TO dufflebag_app;
ALTER DEFAULT PRIVILEGES FOR ROLE dufflebag_migrate IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dufflebag_app;
```

The `ALTER DEFAULT PRIVILEGES` line is what makes upgrades routine: tables
added by future migrations are usable by the serving role without a manual
grant step.

## Migrations

The server applies pending schema migrations at startup, under an advisory
lock, so concurrently starting replicas serialize. On a single-role setup
that is the whole story: first boot creates the schema, and an upgraded image
migrates the database before it serves. There is no migrate command to run.

On a [two-role setup](#hardened-two-roles) the serving role cannot alter
schema, so the same image is the migration tool. Run it with the privileged
role in an init container or pre-deploy step:

```sh
docker run --rm \
  -e DFBG_DATABASE_URL='postgres://dufflebag_migrate:<migration password>@db/dufflebag' \
  quay.io/benjamin_holmes/dufflebag:<tag> migrate
```

It applies any pending schema migrations and exits zero; running it again is
a no-op, so it is safe on every deploy. A schema change the serving role
cannot apply fails startup rather than serving a schema the server does not
understand.

## What database access does and does not grant

On an unencrypted deployment, anyone holding `DFBG_DATABASE_URL` can mint a
root principal directly — the break-glass procedure under
[Encryption — recovery](./encryption.md#recovery) writes that down. Treat
database access accordingly. On a deployment with
[encryption at rest](./encryption.md), identity rows carry integrity MACs and
a principal inserted directly into the database cannot authenticate:
database write access is not administration.
