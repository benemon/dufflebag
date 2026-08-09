terraform {
  required_version = "= 1.14.7"

  required_providers {
    hcp = {
      # Current stable when this lane was added; the exact pin freezes the real
      # provider behaviours ADR-0013 makes this gate responsible for.
      source  = "hashicorp/hcp"
      version = "= 0.112.0"
    }
  }
}

provider "hcp" {
  # The status page is outside dufflebag's compatibility surface. Keeping this
  # off also proves the lane makes no call to the real HCP service (ADR-0013).
  skip_status_check = true
}

variable "bucket_name" {
  type = string
}

variable "seeded_fingerprint" {
  type = string
}

resource "hcp_packer_bucket" "images" {
  name = var.bucket_name
}

resource "hcp_packer_channel" "production" {
  bucket_name = hcp_packer_bucket.images.name
  name        = "production"
  restricted  = false
}

# CreateBucket already made managed latest. The provider's CreateChannel call
# receives AlreadyExists/code 6 and adopts it through its managed-channel
# branch (live probe 38; duf-why).
resource "hcp_packer_channel" "latest" {
  bucket_name = hcp_packer_bucket.images.name
  name        = "latest"
}

resource "hcp_packer_channel_assignment" "production" {
  bucket_name         = hcp_packer_bucket.images.name
  channel_name        = hcp_packer_channel.production.name
  version_fingerprint = var.seeded_fingerprint
}

data "hcp_packer_version" "latest" {
  bucket_name  = hcp_packer_bucket.images.name
  channel_name = hcp_packer_channel.latest.name
}

data "hcp_packer_artifact" "seeded" {
  bucket_name         = hcp_packer_bucket.images.name
  version_fingerprint = data.hcp_packer_version.latest.fingerprint
  platform            = "aws"
  region              = "eu-west-1"
}

data "hcp_packer_bucket_names" "all" {
  depends_on = [hcp_packer_bucket.images]
}

output "version_fingerprint" {
  value = data.hcp_packer_version.latest.fingerprint
}

output "artifact_fingerprint" {
  value = data.hcp_packer_artifact.seeded.version_fingerprint
}

output "artifact_external_identifier" {
  value = data.hcp_packer_artifact.seeded.external_identifier
}

output "bucket_names" {
  value = data.hcp_packer_bucket_names.all.names
}

# run_task remains deliberately unsupported; bucket IAM binding/policy are
# supported in principle but not implemented. Managed latest is both adopted
# above and read through the version data source.
