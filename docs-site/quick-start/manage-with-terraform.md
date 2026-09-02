# Manage dufflebag with Terraform

`terraform-provider-hcp` is the automation interface for a dufflebag
registry. It includes resources, not only data sources, so it can manage
buckets, channels, and channel assignments declaratively. The dufflebag registry supports
version 0.84.0 or newer. The end-to-end suite runs version 0.112.0.

This page starts with the consumption lookup - resolving a published image -
then covers declarative management.

## Configure the provider

Prerequisites: The `HCP_*` environment variables from
[Build an image with Packer](./build-with-packer.md) and
`terraform-provider-hcp` 0.84.0 or newer.

1. Set `skip_status_check = true` in the provider configuration. The HCP
   status page is outside dufflebag's surface. Skipping the check prevents
   the run from calling the HCP service.

2. Run Terraform with the `HCP_*` environment variables set.

::: info
The provider fetches the selected project through the resource-manager API,
even when `HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` are set. Packer skips
this call. The dufflebag server serves the provider's request without additional
configuration.
:::

## Provider support

What dufflebag serves from `terraform-provider-hcp` (0.84.0 or newer; the
end-to-end suite runs 0.112.0):

| Name | Kind | Supported | Notes |
|---|---|---|---|
| `hcp_packer_bucket` | Resource | Yes | Destroy tolerates an already-deleted bucket |
| `hcp_packer_channel` | Resource | Yes | Can adopt the managed `latest`; omit `restricted` on a block managing it |
| `hcp_packer_channel_assignment` | Resource | Yes | Destroy clears the assignment |
| `hcp_packer_version` | Data source | Yes | Refuses a revoked version |
| `hcp_packer_artifact` | Data source | Yes | Resolves by platform and region |
| `hcp_packer_bucket_names` | Data source | Yes | Lists the project's buckets |
| `hcp_packer_run_task` | Resource / data source | No | An inbound-webhook feature of HCP Terraform, outside dufflebag's surface |
| `hcp_packer_bucket_iam_binding` / `_iam_policy` | Resource | No | Bucket IAM is not served |
| `hcp_packer_image` / `hcp_packer_iteration` | Data source | No | Deprecated upstream; removed from the provider before the supported version floor |

The [compatibility reference](../reference/compatibility.md) records the
operations behind this table and the client-version floors.

## Look up an image

Resolve the managed `latest` channel to the current version, then its
artifact by platform and region:

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

data "hcp_packer_artifact" "example" {
  bucket_name         = "dufflebag-example"
  version_fingerprint = data.hcp_packer_version.example.fingerprint
  platform            = "docker"
  region              = "docker"
}
```

- `hcp_packer_version` resolves a channel to its current version - the
  consumption pattern, most often against `latest`. It refuses a revoked
  version, so consumers cannot build from a version that has been pulled.
- `hcp_packer_artifact` returns the `external_identifier` consumed by a
  downstream build.
- `hcp_packer_bucket_names` lists the project's buckets.

The console's consumption card on a version screen renders this block
ready-to-paste for any resolvable version - see
[Versions](../operations/versions.md).

## Manage the registry declaratively

The three resources manage buckets, channels and promotion. One behaviour is
worth knowing before writing a channel block. `CreateBucket` creates the
managed `latest` channel, so an `hcp_packer_channel` block naming `latest`
receives an already-exists response and adopts the existing channel - omit
the `restricted` argument from that block, since setting it makes the
provider try to update a managed channel, and dufflebag refuses the update.

### A working example

The end-to-end suite applies this shape against a real dufflebag instance:

```hcl
resource "hcp_packer_bucket" "images" {
  name = "app-image"
}

resource "hcp_packer_channel" "production" {
  bucket_name = hcp_packer_bucket.images.name
  name        = "production"
}

# CreateBucket already made the managed latest channel; this block adopts it.
resource "hcp_packer_channel" "latest" {
  bucket_name = hcp_packer_bucket.images.name
  name        = "latest"
}

resource "hcp_packer_channel_assignment" "production" {
  bucket_name         = hcp_packer_bucket.images.name
  channel_name        = hcp_packer_channel.production.name
  version_fingerprint = var.release_fingerprint
}
```

### Promotion as code

Prerequisites: An `hcp_packer_channel_assignment` resource and the
fingerprint of a version that passed testing.

1. Change `release_fingerprint` to the selected fingerprint.
2. Apply the change.

The server accumulates the channel's assignment history. Revocation rollback
walks that history when a version is pulled. After a revocation rolls a
channel back, the assignment in Terraform state appears as drift on the next
plan. See [Versions - revocation](../operations/versions.md) and
[Channels](../operations/channels.md).

The console provides the same operations to publishers. Use Terraform to
keep pipeline operations declarative.

## Next

[Upgrading](./upgrading.md) - replacing the image and verifying readiness.
