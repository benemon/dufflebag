# Build an image with Packer

This page points stock Packer at the instance and publishes build metadata.
It assumes the tenancy and builder credential from [Bootstrap](./bootstrap.md)
and stock Packer 1.15.4 or newer.

Prerequisites: A selected organisation and project, and the builder
principal's client ID and secret.

::: warning
Use a client ID that has not been used against another service. The SDK
caches tokens in `~/.config/hcp/creds-cache.json`, keyed by client ID and
geography. Reusing an ID can cause a cached token to be sent to the wrong
endpoint.
:::

1. Open the console's **Instance** screen after selecting the organisation
   and project.

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
HTTPS. Set `HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` together for
deterministic selection; if they are omitted, the clients use discovery. For
a disposable self-signed deployment, `HCP_API_TLS=insecure` and
`HCP_AUTH_TLS=insecure` skip certificate verification.

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

4. Run `packer init example.pkr.hcl`, then `packer build example.pkr.hcl`
   with the environment above.

The build creates the bucket, an incomplete version, and its builds. On
completion the version becomes active and the managed `latest` channel moves
to it.

::: tip
Bucket creation is the organisation- and project-scoped behaviour. A
[bucket-scoped principal](../administration/principals.md#scopes) publishes
into a bucket that already exists - name any other bucket and the build fails
with not-found, and Packer's create-if-missing step is refused rather than
minting one.
::: To also upload an SBOM during the build, see
[Builds - SBOMs and findings](../operations/builds.md).

## Next

[Manage dufflebag with Terraform](./manage-with-terraform.md) - look up the
published image and manage the registry declaratively.
