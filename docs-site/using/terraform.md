# Terraform provider

Use `terraform-provider-hcp` as the automation interface for a dufflebag
registry. It includes resources, not only data sources, so it can manage
buckets, channels, and channel assignments declaratively. Dufflebag supports
version 0.84.0 or newer. The end-to-end suite runs version 0.112.0.

## Configure the provider

Prerequisites: The `HCP_*` environment variables from
[Getting started](../getting-started/first-use.md) and
`terraform-provider-hcp` 0.84.0 or newer.

1. Set `skip_status_check = true` in the provider configuration. The HCP status
   page is outside dufflebag's surface. Skipping the check prevents the run
   from calling the HCP service.

2. Run Terraform with the `HCP_*` environment variables set.

::: info
The provider fetches the selected project through the resource-manager API,
even when `HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` are set. Packer skips this
call. Dufflebag serves the provider's request without additional
configuration.
:::

## Resources

- **`hcp_packer_bucket`** creates and manages a bucket. Destroying the resource
  tolerates an already-deleted bucket and fails on any other error.
- **`hcp_packer_channel`** manages user channels. It can also manage the
  managed `latest` channel. `CreateBucket` creates `latest`, so the provider
  receives an already-exists response and adopts the existing channel. Omit
  the `restricted` argument from a channel block that manages `latest`.
  Setting it makes the provider try to update a managed channel, and dufflebag
  refuses the update.
- **`hcp_packer_channel_assignment`** records a promotion by assigning a
  version fingerprint to a channel. Destroying the resource clears the
  assignment.

::: warning
`hcp_packer_run_task`, an inbound-webhook feature of HCP Terraform, is not
supported. Bucket IAM bindings are also not supported.
:::

## Data sources

- `hcp_packer_version` resolves a channel to its current version. This is the
  consumption pattern, most often against `latest`. It refuses a revoked
  version, so consumers cannot build from a version that has been pulled.
- `hcp_packer_artifact` resolves a version's artifact by platform and region.
  It returns the `external_identifier` consumed by a downstream build.
- `hcp_packer_bucket_names` lists the project's buckets.

## A working example

Prerequisites: A dufflebag instance and the environment from
[Getting started](../getting-started/first-use.md).

1. Apply the following configuration. The end-to-end suite applies this shape
   against a real dufflebag instance:

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

data "hcp_packer_version" "latest" {
  bucket_name  = hcp_packer_bucket.images.name
  channel_name = "latest"
}

data "hcp_packer_artifact" "release" {
  bucket_name         = hcp_packer_bucket.images.name
  version_fingerprint = data.hcp_packer_version.latest.fingerprint
  platform            = "docker"
  region              = "docker"
}
```

## Promotion as code

Prerequisites: An `hcp_packer_channel_assignment` resource and the fingerprint
of a version that passed testing.

1. Change `release_fingerprint` to the selected fingerprint.

2. Apply the change.

The server accumulates the channel's assignment history. Revocation rollback
walks that history when a version is pulled. After a revocation rolls a channel
back, the assignment in Terraform state appears as drift on the next plan.
See [Revocation, restore and channels](./revocation-channels.md).

The console provides the same operations to publishers. Use Terraform to keep
pipeline operations declarative.

## Where to go next

- [Revocation, restore and channels](./revocation-channels.md): what happens to
  assignments when versions are revoked.
- [Compatibility reference](../reference/compatibility.md): the twelve
  provider operations and the client-version floors.
