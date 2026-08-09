packer {
  required_plugins {
    docker = {
      version = "= 1.1.4"
      source  = "github.com/hashicorp/docker"
    }
  }
}

variable "base_image" {
  type = string
}

variable "run_label" {
  type = string
}

variable "sbom_source" {
  type = string
}

hcp_packer_registry {
  bucket_name = "dufflebag-sbom-e2e"
}

source "docker" "sbom" {
  image  = var.base_image
  pull   = false
  commit = true
  changes = [
    "LABEL dev.dufflebag.e2e=${var.run_label}",
  ]
}

build {
  sources = ["source.docker.sbom"]

  provisioner "file" {
    source      = var.sbom_source
    destination = "/tmp/dufflebag.spdx.json"
  }

  provisioner "hcp-sbom" {
    source = "/tmp/dufflebag.spdx.json"
  }
}
