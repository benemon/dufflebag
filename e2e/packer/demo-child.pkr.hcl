// The demo's derived image. Unlike local-child.pkr.hcl, which the Packer gate
// uses and which deliberately adds nothing, this one installs packages and
// reports its own SBOM — so the console has a child whose contents differ from
// its parent's, which is the case worth looking at.
//
// Two sources, deliberately. A version holds one build per source, so this
// publishes a single fingerprint carrying two builds with different component
// types, different artifacts and different package lists. It is the only place
// in the demo where the Builds tab has more than one row, where a version's
// "every build must succeed" completion rule has anything to hold, and where
// two SBOMs can be compared within one version rather than across two.
//
// Both sources are docker, because the demo has no cloud credentials. The
// multi-platform case — the same version built for docker and for AWS — has
// exactly this shape, with the builds differing in platform rather than in
// what they install.

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

source "docker" "web" {
  image  = data.hcp-packer-artifact.parent.external_identifier
  pull   = false
  commit = true
  changes = [
    "LABEL dev.dufflebag.e2e=${var.run_label}",
    "LABEL dev.dufflebag.component=web",
  ]
}

source "docker" "proxy" {
  image  = data.hcp-packer-artifact.parent.external_identifier
  pull   = false
  commit = true
  changes = [
    "LABEL dev.dufflebag.e2e=${var.run_label}",
    "LABEL dev.dufflebag.component=proxy",
  ]
}

build {
  sources = [
    "source.docker.web",
    "source.docker.proxy",
  ]

  // Each source installs its own package, so the two builds' SBOMs diverge
  // from each other as well as from the parent's.
  provisioner "shell" {
    only   = ["docker.web"]
    inline = ["apk add --no-cache nginx"]
  }

  provisioner "shell" {
    only   = ["docker.proxy"]
    inline = ["apk add --no-cache haproxy"]
  }

  provisioner "hcp-sbom" {
    # Generated in the container, as the parent does, so the document describes
    # this image rather than whatever machine ran Packer.
    auto_generate   = true
    scanner_args    = ["-o", "spdx-json", "--exclude", "./tmp/packer-sbom-runner"]
    scan_path       = "/"
    execute_command = "chmod +x {{.Path}} && {{.Path}} sbom-generate {{.Args}} {{.ScanPath}} > {{.Output}}"
  }
}
