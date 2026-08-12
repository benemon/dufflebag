# Getting started

This guide points stock Packer and `terraform-provider-hcp` at a running
dufflebag instance.

## Prerequisites

You need:

- a dufflebag instance served at the root of an HTTPS hostname;
- PostgreSQL migrations applied and the server running; and
- stock Packer 1.15.4 or newer, plus Terraform with
  `terraform-provider-hcp` 0.84.0 or newer.

The [deployment guide](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md)
covers PostgreSQL, migrations, TLS and running the server.
For a self-contained alternative, use its [Helm section](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md#helm).

## Initialize the instance

Complete initialization before exposing the instance publicly. Use the console
wizard, or call the one-shot endpoint directly:

```sh
curl -sX POST https://registry.example.com/sys/init \
  -H 'content-type: application/json' -d '{}'
```

`POST /sys/init` works once. Its response contains the initial root client ID
and client secret, recovery shares, and the recovery threshold. They are shown
once and cannot be retrieved later. Put the credentials in a secret manager and
store the recovery shares separately before continuing.

## Create an organisation and project

Continue through the console wizard to create an organisation and a project.
For a headless setup, use the organisation and project operations in the
[platform API reference](/platform-api.html).

## Mint a builder principal

Create a project-scoped service principal with the `builder` role, then issue
its first secret. Retain the client ID and the one-time secret for the client
environment. The console supports this flow; the equivalent principal and
secret operations are in the [platform API reference](/platform-api.html).

## Point Packer at dufflebag

Open the console's **Instance** screen after selecting the organisation and
project. It generates this environment block for the selected tenancy:

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
certificate verification. `HCP_API_TLS=disabled` selects plain HTTP for API
traffic, but authentication still requires an HTTPS URL;
`HCP_AUTH_TLS=disabled` does not disable authentication TLS.

Use a client ID that has not been used against another service. The SDK caches
tokens in `~/.config/hcp/creds-cache.json`, keyed by client ID and geography,
so reusing an ID can cause a cached token to be sent to the wrong endpoint.

An ordinary HCL2 template can publish registry metadata:

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

Run `packer init example.pkr.hcl`, then `packer build example.pkr.hcl` with the
environment above.

## Point terraform-provider-hcp at dufflebag

The provider reads the same `HCP_*` environment variables. Configure it to skip
the external status check, then resolve the managed `latest` channel:

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

- [Compatibility reference](https://github.com/benemon/dufflebag/blob/main/docs/compatibility.md)
- [Deployment guide](https://github.com/benemon/dufflebag/blob/main/docs/deployment.md)
