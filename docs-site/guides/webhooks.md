# Webhooks

Webhooks send project registry changes to your own HTTP endpoint. They are
operational configuration: a project `maintainer` can create, verify, update,
or delete them and inspect their delivery history.

## Create and activate a webhook

Open **Webhooks** in the console, select **Create webhook**, and provide a name
and an HTTP or HTTPS URL. A description and HMAC key are optional. Choose
individual event operations, or leave every checkbox clear to receive all
events.

Dufflebag immediately sends a signed verification payload. The webhook remains
`pending` until the endpoint answers with a 2xx status; pending webhooks receive
no project events. Fix the receiver and choose **Verify** to repeat the same
handshake. Changing a webhook URL also returns it to pending and re-runs the
handshake. There is no separate ping operation.

## Verify a request

When an HMAC key is configured, every request includes
`X-Dufflebag-Signature: sha512=<hex>`, computed as HMAC-SHA512 over the exact raw
request body. `X-Dufflebag-Event` names the operation and
`X-Dufflebag-Delivery` carries its ULID event ID. Compute the HMAC over the body
before parsing JSON and compare it in constant time. A webhook without an HMAC
key omits the signature header.

The event envelope identifies the organisation and project, the operation and
target, and the acting principal. Its payload uses the same resource vocabulary
as the Packer-compatible API. A `channel.assigned` event also carries the
previous assignment fields, including unassignment.

## Choose events

Subscriptions use the audit-vocabulary operation names shown by the console:
version create, completion, revocation scheduling, revocation, restore and
delete; channel create, assignment and delete; and bucket create and delete.
An empty event list means all operations.

## Follow delivery

Expand a webhook row to see its newest 100 delivery records. Each row shows the
operation, response code, attempt count, timestamps and a bounded response or
failure detail. Delivery is at least once: make the receiver idempotent on
`X-Dufflebag-Delivery`. Failures retry up to five times over roughly fifteen
minutes and then become `failed`; a target refused by the outbound-address
policy becomes `refused` without retry.

The operator controls egress and credential sealing. See the
[deployment guide](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md#webhooks)
for the default SSRF protections, the lab-only private-network escape hatch,
timeouts, response bounds, and `DFBG_CREDENTIAL_KEY` migration rules.

## Where to go next

- [Deployment guide — Webhooks](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md#webhooks)
  — egress protection and credential keys.
- [Platform API reference](/platform-api.html) — create, update, verify and
  delivery-history wire shapes.
