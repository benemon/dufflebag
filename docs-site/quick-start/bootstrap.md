# Bootstrap

This page claims a fresh instance and sets up the tenancy Packer will publish
into: initialize, create an organisation and project, and mint a builder
credential. It assumes a serving instance from
[Installation](./installation.md).

## Initialize the instance

Prerequisites: A running dufflebag instance that has not been initialized.

::: warning
Complete initialization **before** exposing the instance publicly — an
uninitialised instance is claimable by whoever reaches it first; that is the
accepted bootstrap trade-off. `POST /sys/init` works once. Its response is
shown once and cannot be retrieved later.
:::

1. Open the console wizard.

![Dufflebag first-run initialization screen](/screenshots/first-run.png)

   For a headless setup, call the one-shot endpoint directly — both use the
   same endpoint, and it works exactly once:

```sh
curl -sX POST https://registry.example.com/sys/init \
  -H 'content-type: application/json' -d '{}'
```

2. Copy the initial root client ID and client secret, recovery shares, and
   recovery threshold from the response.

3. Put the credentials in a secret manager and store the recovery shares
   offline, separately, before continuing. The shares are the supported path
   back in if the root secret is lost (see
   [Encryption — recovery](../components/encryption.md#recovery)).

By default there is a single share. For a threshold ceremony — *k* of *n*
custodians must co-operate to recover — pass the parameters in the request
body; they are fixed at initialisation and cannot be changed later. The
bounds are `1 ≤ k ≤ n ≤ 255`; anything else answers 400:

```sh
curl -sX POST https://registry.example.com/sys/init \
  -H 'content-type: application/json' \
  -d '{"recovery_share_count": 5, "recovery_threshold": 3}'
```

## Create an organisation and project

Prerequisites: The initial root client ID and client secret from
initialization.

1. Sign in to the console with the initial root client credentials.

![Dufflebag service principal sign-in screen](/screenshots/login.png)

2. Continue through the console wizard to create an organisation and a
   project. For a headless setup, use the organisation and project operations
   in the [platform API reference](pathname:///platform-api.html).

## Mint a builder principal

Prerequisites: An organisation and project.

1. Create a project-scoped service principal with the `builder` role — the
   smallest role that completes a `packer build`, and what a CI build
   credential should hold. The console supports this operation; the
   equivalent principal operation is in the
   [platform API reference](pathname:///platform-api.html).

2. Issue the principal's first secret.

3. Retain the client ID and the one-time secret for the client environment.

The role ladder, scopes, secret rotation and recovery are covered in
[Principals](../administration/principals.md).

## Next

[Build an image with Packer](./build-with-packer.md) — point the stock
client at the instance and publish build metadata.
