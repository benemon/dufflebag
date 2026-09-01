packer {
  required_plugins {
    docker = {
      version = "= 1.1.4"
      source  = "github.com/hashicorp/docker"
    }
  }
}

hcp_packer_registry {
  bucket_name = "verify-docker"
  description = "Container images built by Dufflebag's cloud verification lane."
}

source "docker" "ubuntu" {
  image  = "ubuntu:22.04"
  pull   = true
  commit = true
}

build {
  sources = ["source.docker.ubuntu"]

  provisioner "shell" {
    inline = ["echo dufflebag cloud verification"]
  }

  provisioner "hcp-sbom" {
    # Match the local Packer lane: inventory the container itself, excluding
    # the temporary runner that Packer uploads into the scan root.
    auto_generate   = true
    scanner_args    = ["-o", "spdx-json", "--exclude", "./tmp/packer-sbom-runner"]
    scan_path       = "/"
    execute_command = "chmod +x {{.Path}} && {{.Path}} sbom-generate {{.Args}} {{.ScanPath}} > {{.Output}}"
  }
}
