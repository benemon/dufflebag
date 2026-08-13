# Webhooks

Webhooks send project registry changes to your own HTTP endpoint. They are
operational configuration. A project `maintainer` can create, verify, update,
or delete them and inspect their delivery history.

## Create and activate a webhook

Prerequisites: The `maintainer` role on the project and an HTTP or HTTPS
receiver.

1. Open **Webhooks** in the console and select **Create webhook**.

2. Provide a name and an HTTP or HTTPS URL.

3. Optionally provide a description and HMAC key.

4. Select individual event operations, or leave every checkbox clear to
   receive all events.

5. Create the webhook.

Dufflebag immediately sends a signed verification payload. The webhook remains
`pending` until the endpoint returns a 2xx status. Pending webhooks receive no
project events.

If verification fails, fix the receiver and select **Verify** to repeat the
same handshake. Changing a webhook URL also returns it to `pending` and repeats
the handshake. There is no separate ping operation.

## Verify a request

When an HMAC key is configured, every request includes
`X-Dufflebag-Signature: sha512=<hex>`. The signature is an HMAC-SHA512 over the
exact raw request body. `X-Dufflebag-Event` names the operation and
`X-Dufflebag-Delivery` contains its ULID event ID.

Prerequisites: A webhook with an HMAC key and the request's raw body.

1. Compute the HMAC over the raw body before parsing the JSON.

2. Compare the computed HMAC with `X-Dufflebag-Signature` in constant time.

3. Parse the JSON after the signature comparison succeeds.

A webhook without an HMAC key omits the signature header.

The event envelope identifies the organisation and project, the operation and
target, and the acting principal. Its payload uses the same resource vocabulary
as the Packer-compatible API. A `channel.assigned` event also contains the
previous assignment fields, including unassignment.

## Choose events

Subscriptions use the audit-vocabulary operation names shown by the console.
They cover version creation, completion, revocation scheduling, revocation,
restore, and deletion; channel creation, assignment, and deletion; and bucket
creation and deletion. An empty event list means all operations.

## Follow delivery

1. Expand a webhook row to see its newest 100 delivery records.

Each row shows the operation, response code, attempt count, timestamps, and a
bounded response or failure detail.

::: warning
Delivery is at least once. Make the receiver idempotent on
`X-Dufflebag-Delivery`.
:::

Failures retry up to five times over roughly fifteen minutes and then become
`failed`. A target refused by the outbound-address policy becomes `refused`
without retry.

The operator controls egress and credential sealing. See the
[deployment guide](../deployment/operations.md#webhooks) for the default SSRF
protections, the lab-only private-network escape hatch, timeouts, response
bounds, and `DFBG_CREDENTIAL_KEY` migration rules.

## Where to go next

- [Deployment guide: Webhooks](../deployment/operations.md#webhooks): egress
  protection and credential keys.
- [Platform API reference](/platform-api.html): create, update, verify, and
  delivery-history wire shapes.
