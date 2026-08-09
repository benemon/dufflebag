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

variable "bucket_name" {
  type = string
}

variable "run_label" {
  type = string
}

# One bucket per distro, so the demo shows findings side by side rather than
# mixed into a single history where the reader cannot tell which image a
# finding came from.
hcp_packer_registry {
  bucket_name = var.bucket_name
}

source "docker" "distro" {
  image  = var.base_image
  pull   = true
  commit = true
  changes = [
    "LABEL dev.dufflebag.demo=${var.run_label}",
  ]
}

build {
  sources = ["source.docker.distro"]

  provisioner "hcp-sbom" {
    # Scans the image's own filesystem, so the inventory is the distro's real
    # package set rather than anything this repository supplies.
    auto_generate   = true
    scanner_args    = ["-o", "spdx-json", "--exclude", "./tmp/packer-sbom-runner"]
    scan_path       = "/"
    execute_command = "chmod +x {{.Path}} && {{.Path}} sbom-generate {{.Args}} {{.ScanPath}} > {{.Output}}"
  }
}
