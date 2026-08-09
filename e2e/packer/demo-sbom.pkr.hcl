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

  provisioner "hcp-sbom" {
    # Packer uploads this runner inside the scan root; exclude it so its
    # embedded Go modules are not reported as packages in the built image.
    auto_generate   = true
    scanner_args    = ["-o", "spdx-json", "--exclude", "./tmp/packer-sbom-runner"]
    scan_path       = "/"
    execute_command = "chmod +x {{.Path}} && {{.Path}} sbom-generate {{.Args}} {{.ScanPath}} > {{.Output}}"
  }
}
