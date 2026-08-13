# Audit and encryption

Two operator concerns, deliberately independent: the **audit trail** records
what the instance did, and **encryption at rest** protects what it stores.
Both are managed from the console's root-only **Audit** and **Encryption**
pages or the equivalent [platform API](/platform-api.html) endpoints; the
deeper operational contract lives in the
[deployment guide](../deployment/index.md).

## The audit trail

Every API request is audited as a request/response pair. The declared
exemptions are UI asset serving, the health probe, and admission refusals on
the anonymous surfaces (`/oauth2/token`, `/sys/recovery`), which are decided
before the audit seam.

Sensitive values never enter the trail directly — they are recorded as HMACs,
so entries can be correlated (the same secret produces the same HMAC) without
the trail holding a usable credential. Entries record the HMAC key version
that produced them.

### File targets

Audit entries go to **file targets**: up to three paths, created and removed
by `root`. Each target reports its health — `healthy` or `failing`, with
consecutive and cumulative failure counts, the last failure time, and the
last successful reopen.

An instance with **no targets configured does not audit at all** — the
console warns exactly that before letting you remove the last one.

### Fail-closed

Once auditing is enabled, it fails closed. While at least one configured
target still accepts writes, requests proceed and the failing target is
surfaced through its health. When **no** healthy target remains, the instance
stops serving requests rather than serving them unrecorded, and
`GET /sys/health` answers 503. Three targets on independent failure domains
is the posture that makes this a property rather than a liability.

### Log rotation

Send the process `SIGHUP` and it reopens every file target in write order —
the standard logrotate contract. A rename-then-reopen rotation is handled; a
failed reopen keeps writing to the previous descriptor rather than dropping
entries. `last_reopened_at` on the target tells you the rotation took.

Related: [Encryption at rest](./encryption.md).
