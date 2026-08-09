packer {
  required_plugins {
    docker = {
      version = "= 1.1.4"
      source  = "github.com/hashicorp/docker"
    }
  }
}

variable "run_label" {
  type = string
}

data "hcp-packer-version" "parent" {
  bucket_name  = "dufflebag-sbom-e2e"
  channel_name = "latest"
}

data "hcp-packer-artifact" "parent" {
  bucket_name         = "dufflebag-sbom-e2e"
  version_fingerprint = data.hcp-packer-version.parent.fingerprint
  platform            = "docker"
  region              = "docker"
}

hcp_packer_registry {
  bucket_name = "dufflebag-derived-e2e"
}

source "docker" "child" {
  image  = data.hcp-packer-artifact.parent.external_identifier
  pull   = false
  commit = true
  changes = [
    "LABEL dev.dufflebag.e2e=${var.run_label}",
  ]
}

build {
  sources = ["source.docker.child"]
}
