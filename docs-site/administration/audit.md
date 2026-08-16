# Audit trail

The **audit trail** records what the instance did. **Encryption at rest**
protects what it stores. These operator concerns are independent. Roots manage
both from the console's **Audit** and **Encryption** pages or the equivalent
[platform API](pathname:///platform-api.html) endpoints. The
[installation page](../quick-start/installation.md#configuration-reference) defines the operational contract.

![Dufflebag Audit screen showing a healthy file target](/screenshots/audit.png)

## The audit trail

Every API request is audited as a request and response pair. UI asset serving,
the health probe, and admission refusals on the anonymous surfaces
(`/oauth2/token`, `/sys/recovery`) are exempt. Those refusals are decided before
the audit seam.

Sensitive values never enter the trail directly. They are recorded as HMACs,
so entries can be correlated without the trail holding a usable credential.
The same secret produces the same HMAC. Each entry records the HMAC key version
that produced it.

### File targets

Audit entries go to **file targets**. A `root` can create and remove up to three
paths. Each target reports a `healthy` or `failing` status, consecutive and
cumulative failure counts, the last failure time, and the last successful
reopen.

::: warning
An instance with no targets configured does not audit. The console warns before
allowing you to remove the last target.
:::

### Fail-closed behavior

Once auditing is enabled, it fails closed. Requests proceed while at least one
configured target accepts writes. The health report identifies any failing
target.

::: warning
When no healthy target remains, the instance stops serving requests instead of
serving them unrecorded. `GET /sys/health` returns 503.
:::

Configure three targets on independent storage, so that a single failure does not stop the instance from serving.

### Rotate audit logs

1. Send the process `SIGHUP`.

The process reopens every file target in write order. This is the standard
logrotate contract. Rename-then-reopen rotation is supported. If a reopen
fails, the process continues writing to the previous descriptor instead of
dropping entries. The target's `last_reopened_at` value confirms that the
rotation took place.

Related: [Encryption at rest](./encryption.md).
