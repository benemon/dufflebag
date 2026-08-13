# Terraform provider

`terraform-provider-hcp` is the recommended automation interface for a
dufflebag registry: it ships *resources*, not just data sources, so buckets,
channels and channel assignments can be managed declaratively. Version
**0.84.0 or newer** is supported; 0.112.0 is the pin the end-to-end suite
actually drives.

The provider reads the same `HCP_*` environment variables as Packer —
[Getting started](./getting-started.md) covers the block. Two
provider-specific notes:

- Set `skip_status_check = true`: the HCP status page is outside dufflebag's
  surface, and skipping it keeps the run from calling the real service.
- Even with `HCP_ORGANIZATION_ID` and `HCP_PROJECT_ID` set, the provider
  fetches the pinned project through the resource-manager API (Packer skips
  that call). dufflebag serves it; nothing to configure.

## Resources

**`hcp_packer_bucket`** creates and manages a bucket. Its destroy tolerates
an already-deleted bucket and fails on anything else.

**`hcp_packer_channel`** manages user channels. It can also manage the
managed `latest` channel: `CreateBucket` mints `latest` automatically, so the
provider's create receives an already-exists answer and *adopts* the existing
channel — this is expected, not an error. Omit the `restricted` argument on a
channel block managing `latest`; setting it makes the provider try to update
a managed channel, which is refused.

**`hcp_packer_channel_assignment`** is the promotion record: it assigns a
version fingerprint to a channel. Destroying it clears the assignment.

Not supported: `hcp_packer_run_task` (an inbound-webhook feature of HCP
Terraform), and bucket IAM bindings.

## Data sources

- `hcp_packer_version` resolves a channel to its current version — the
  standard consumption pattern, most often against `latest`. A revoked
  version is refused, which is the point: consumers cannot build from what
  has been pulled.
- `hcp_packer_artifact` resolves a version's artifact by platform and
  region, yielding the `external_identifier` a downstream build consumes.
- `hcp_packer_bucket_names` lists the project's buckets.

## A working example

This is the shape the end-to-end suite applies against a real dufflebag
instance (with the environment from
[Getting started](./getting-started.md)):

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

With the assignment resource holding the pointer, promotion is a plan/apply:
change `release_fingerprint` to the fingerprint that passed testing and
apply. The channel's assignment history accumulates on the server, which is
what [revocation rollback](./revocation-channels.md) walks when a version is
pulled — after a revocation rolls a channel back, the Terraform state's
assignment will show as drift on the next plan, which is exactly what you
want to see.

The console offers the same operations interactively to publishers, but for
pipelines, Terraform remains the recommended path.

## Where to go next

- [Revocation, restore and channels](./revocation-channels.md) — what
  happens to assignments when versions are revoked.
- [Compatibility reference](../reference/compatibility.md)
  — the twelve provider operations and the client-version floors.
