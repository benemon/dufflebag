# Getting started

This guide points stock Packer and `terraform-provider-hcp` at a running
dufflebag instance. If you do not have one yet, the
[installation guide](./installation.md) gets you there on Kubernetes,
OpenShift, or plain Docker/Podman.

## Prerequisites

You need:

- a dufflebag instance served at the root of an HTTPS hostname;
- PostgreSQL migrations applied and the server running; and
- stock Packer 1.15.4 or newer, plus Terraform with
  `terraform-provider-hcp` 0.84.0 or newer.

The [deployment guide](../deployment/index.md)
covers PostgreSQL, migrations, TLS and running the server.
For a self-contained alternative, use its [Helm section](../deployment/index.md#helm).

## Initialize the instance

Prerequisites: A running dufflebag instance that has not been initialized.

::: warning
Complete initialization before exposing the instance publicly. `POST /sys/init`
works once. Its response is shown once and cannot be retrieved later.
:::

1. Open the console wizard.

![Dufflebag first-run initialization screen](/screenshots/first-run.png)

   For a headless setup, call the one-shot endpoint directly:

```sh
curl -sX POST https://registry.example.com/sys/init \
  -H 'content-type: application/json' -d '{}'
```

2. Copy the initial root client ID and client secret, recovery shares, and
   recovery threshold from the response.

3. Put the credentials in a secret manager and store the recovery shares
   separately before continuing.

## Create an organisation and project

Prerequisites: The initial root client ID and client secret from initialization.

1. Sign in to the console with the initial root client credentials.

![Dufflebag service principal sign-in screen](/screenshots/login.png)

2. Continue through the console wizard to create an organisation and a project.
   For a headless setup, use the organisation and project operations in the
   [platform API reference](pathname:///platform-api.html).

## Mint a builder principal

Prerequisites: An organisation and project.

1. Create a project-scoped service principal with the `builder` role. The
   console supports this operation. The equivalent principal operation is in
   the [platform API reference](pathname:///platform-api.html).

2. Issue the principal's first secret. The equivalent secret operation is also
   in the [platform API reference](pathname:///platform-api.html).

3. Retain the client ID and the one-time secret for the client environment.

## Point Packer at dufflebag

Prerequisites: A selected organisation and project, and the builder principal's
client ID and secret.

::: warning
Use a client ID that has not been used against another service. The SDK caches
tokens in `~/.config/hcp/creds-cache.json`, keyed by client ID and geography.
Reusing an ID can cause a cached token to be sent to the wrong endpoint.
:::

1. Open the console's **Instance** screen after selecting the organisation and
   project.

2. Export the environment block that it generates for the selected tenancy:

```sh
export HCP_API_ADDRESS=registry.example.com
export HCP_AUTH_URL=https://registry.example.com
export HCP_CLIENT_ID='<client id>'
export HCP_CLIENT_SECRET='<client secret>'
export HCP_ORGANIZATION_ID='<organisation UUID>'
export HCP_PROJECT_ID='<project UUID>'
```

`HCP_API_ADDRESS` is `host[:port]` with no scheme. `HCP_AUTH_URL` must use
HTTPS. Set `HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` together for deterministic
selection; if they are omitted, the clients use discovery. For a disposable
self-signed deployment, `HCP_API_TLS=insecure` and `HCP_AUTH_TLS=insecure` skip
certificate verification.

::: info
`HCP_API_TLS=disabled` selects plain HTTP for API traffic, but authentication
still requires an HTTPS URL. `HCP_AUTH_TLS=disabled` does not disable
authentication TLS.
:::

3. Create an ordinary HCL2 template to publish registry metadata:

```hcl
packer {
  required_plugins {
    docker = {
      source  = "github.com/hashicorp/docker"
      version = "= 1.1.4"
    }
  }
}

hcp_packer_registry {
  bucket_name = "dufflebag-example"
}

source "docker" "example" {
  image  = "alpine:3.20"
  pull   = true
  commit = true
}

build {
  sources = ["source.docker.example"]
}
```

4. Run `packer init example.pkr.hcl`, then `packer build example.pkr.hcl` with
   the environment above.

## Point terraform-provider-hcp at dufflebag

The provider reads the same `HCP_*` environment variables.

Prerequisites: The same `HCP_*` environment variables used by Packer.

1. Configure the provider to skip the external status check, then resolve the
   managed `latest` channel:

```hcl
terraform {
  required_providers {
    hcp = {
      source  = "hashicorp/hcp"
      version = ">= 0.84.0"
    }
  }
}

provider "hcp" {
  skip_status_check = true
}

data "hcp_packer_version" "example" {
  bucket_name  = "dufflebag-example"
  channel_name = "latest"
}
```

Even with both tenancy IDs set, the provider resolves the selected project
through the resource-manager API.

## Where to go next

- [Compatibility reference](../reference/compatibility.md)
- [Deployment guide](../deployment/index.md)
