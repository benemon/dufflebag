packer {
  required_plugins {
    qemu = {
      version = ">= 1.1.3"
      source  = "github.com/hashicorp/qemu"
    }
  }
}

variable "image_name_suffix" {
  type    = string
  default = env("GITHUB_RUN_ID") != "" ? env("GITHUB_RUN_ID") : "local"
}

variable "qemu_accelerator" {
  type    = string
  default = "kvm"
}

variable "work_dir" {
  type    = string
  default = env("E2E_PACKER_WORK")
}

hcp_packer_registry {
  bucket_name = "verify-qemu"
  description = "Local VM images built by Dufflebag's cloud verification lane."
}

source "qemu" "ubuntu" {
  iso_url      = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
  iso_checksum = "file:https://cloud-images.ubuntu.com/jammy/current/SHA256SUMS"
  disk_image   = true

  accelerator      = var.qemu_accelerator
  cd_files         = ["${path.root}/cloud-init/meta-data", "${path.root}/cloud-init/user-data"]
  cd_label         = "cidata"
  disk_interface   = "virtio"
  format           = "qcow2"
  headless         = true
  memory           = 2048
  net_device       = "virtio-net"
  output_directory = "${var.work_dir}/qemu-solo"
  vm_name          = "dufflebag-ci-${var.image_name_suffix}-qemu-solo.qcow2"

  communicator     = "ssh"
  ssh_password     = "dufflebag"
  ssh_timeout      = "20m"
  ssh_username     = "ubuntu"
  shutdown_command = "echo 'dufflebag' | sudo -S shutdown -P now"
}

build {
  sources = ["source.qemu.ubuntu"]

  provisioner "shell" {
    inline = [
      "echo dufflebag cloud verification",
      "sudo apt-get update",
      "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y curl ca-certificates",
      "curl -fsSL -o /tmp/syft.tar.gz https://github.com/anchore/syft/releases/download/v1.51.0/syft_1.51.0_linux_amd64.tar.gz",
      "echo '2a2e837a2c8d59ec9af5472ee22d3b04ee463c4e44476ecf993fd1e5ab6ebc7f  /tmp/syft.tar.gz' | sha256sum -c -",
      "sudo tar -xzf /tmp/syft.tar.gz -C /usr/local/bin syft",
      "sudo /usr/local/bin/syft scan dir:/ -o spdx-json=/tmp/dufflebag.spdx.json",
      "sudo chown $(id -u):$(id -g) /tmp/dufflebag.spdx.json",
    ]
  }

  provisioner "file" {
    direction   = "download"
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-qemu-solo.spdx.json"
  }

  provisioner "hcp-sbom" {
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-qemu-solo.spdx.json"
  }
}
