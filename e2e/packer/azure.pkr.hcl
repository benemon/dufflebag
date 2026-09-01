packer {
  required_plugins {
    azure = {
      version = ">= 2.0.0"
      source  = "github.com/hashicorp/azure"
    }
  }
}

variable "arm_client_id" {
  type    = string
  default = env("ARM_CLIENT_ID")
}

variable "arm_client_secret" {
  type      = string
  sensitive = true
  default   = env("ARM_CLIENT_SECRET")
}

variable "arm_tenant_id" {
  type    = string
  default = env("ARM_TENANT_ID")
}

variable "arm_subscription_id" {
  type    = string
  default = env("ARM_SUBSCRIPTION_ID")
}

variable "resource_group" {
  type    = string
  default = env("AZURE_CI_RESOURCE_GROUP") != "" ? env("AZURE_CI_RESOURCE_GROUP") : "rg-dufflebag-ci"
}

variable "image_name_suffix" {
  type    = string
  default = env("GITHUB_RUN_ID") != "" ? env("GITHUB_RUN_ID") : "local"
}

variable "work_dir" {
  type    = string
  default = env("E2E_PACKER_WORK")
}

hcp_packer_registry {
  bucket_name = "verify-azure"
  description = "Azure managed images built by Dufflebag's cloud verification lane."
}

source "azure-arm" "ubuntu" {
  client_id       = var.arm_client_id
  client_secret   = var.arm_client_secret
  tenant_id       = var.arm_tenant_id
  subscription_id = var.arm_subscription_id

  build_resource_group_name         = var.resource_group
  managed_image_resource_group_name = var.resource_group
  managed_image_name                = "dufflebag-ci-${var.image_name_suffix}-azure-solo"
  vm_size                           = "Standard_B1ms"

  os_type         = "Linux"
  image_publisher = "Canonical"
  image_offer     = "0001-com-ubuntu-server-jammy"
  image_sku       = "22_04-lts"

  communicator = "ssh"
}

build {
  sources = ["source.azure-arm.ubuntu"]

  provisioner "shell" {
    inline = [
      "echo dufflebag cloud verification",
      "sudo apt-get update",
      "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y curl ca-certificates",
      "curl -fsSL -o /tmp/syft.tar.gz https://github.com/anchore/syft/releases/download/v1.51.0/syft_1.51.0_linux_amd64.tar.gz",
      "echo '2a2e837a2c8d59ec9af5472ee22d3b04ee463c4e44476ecf993fd1e5ab6ebc7f  /tmp/syft.tar.gz' | sha256sum -c -",
      "sudo tar -xzf /tmp/syft.tar.gz -C /usr/local/bin syft",
      "sudo /usr/local/bin/syft scan dir:/ --exclude ./proc --exclude ./sys --exclude ./dev --exclude ./run -o spdx-json=/tmp/dufflebag.spdx.json",
      "sudo chown $(id -u):$(id -g) /tmp/dufflebag.spdx.json",
    ]
  }

  provisioner "file" {
    direction   = "download"
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-azure-solo.spdx.json"
  }

  provisioner "hcp-sbom" {
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-azure-solo.spdx.json"
  }
}
