packer {
  required_plugins {
    amazon = {
      version = ">= 1.8.2"
      source  = "github.com/hashicorp/amazon"
    }
    azure = {
      version = ">= 2.0.0"
      source  = "github.com/hashicorp/azure"
    }
    docker = {
      version = "= 1.1.4"
      source  = "github.com/hashicorp/docker"
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

variable "azure_resource_group" {
  type    = string
  default = env("AZURE_CI_RESOURCE_GROUP") != "" ? env("AZURE_CI_RESOURCE_GROUP") : "rg-dufflebag-ci"
}

variable "aws_region" {
  type    = string
  default = env("AWS_DEFAULT_REGION") != "" ? env("AWS_DEFAULT_REGION") : "eu-west-2"
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
  bucket_name = "verify-multi"
  description = "One version spanning cloud, container, and local VM image builders."
}

# These sources deliberately repeat the solo templates. The one build block is
# the compatibility claim: Packer completes one version only after all four do.
source "amazon-ebs" "ubuntu" {
  ami_name      = "dufflebag-ci-${var.image_name_suffix}-aws-multi"
  instance_type = "t3.small"
  region        = var.aws_region
  ssh_username  = "ubuntu"

  source_ami_filter {
    filters = {
      name                = "ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"
      root-device-type    = "ebs"
      virtualization-type = "hvm"
    }
    most_recent = true
    owners      = ["099720109477"]
  }

  run_tags = {
    Name          = "dufflebag-ci-${var.image_name_suffix}-aws-multi"
    dufflebag-ci  = "packer"
  }
}

source "azure-arm" "ubuntu" {
  client_id       = var.arm_client_id
  client_secret   = var.arm_client_secret
  tenant_id       = var.arm_tenant_id
  subscription_id = var.arm_subscription_id

  build_resource_group_name         = var.azure_resource_group
  managed_image_resource_group_name = var.azure_resource_group
  managed_image_name                = "dufflebag-ci-${var.image_name_suffix}-azure-multi"
  vm_size                           = "Standard_B1ms"

  os_type         = "Linux"
  image_publisher = "Canonical"
  image_offer     = "0001-com-ubuntu-server-jammy"
  image_sku       = "22_04-lts"

  communicator = "ssh"
}

source "docker" "ubuntu" {
  image  = "ubuntu:22.04"
  pull   = true
  commit = true
}

build {
  sources = [
    "source.amazon-ebs.ubuntu",
    "source.azure-arm.ubuntu",
    "source.docker.ubuntu",
  ]

  provisioner "shell" {
    inline = ["echo dufflebag cloud verification"]
  }

  provisioner "shell" {
    only = ["amazon-ebs.ubuntu", "azure-arm.ubuntu"]
    inline = [
      "sudo cloud-init status --wait || true",
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
    only        = ["amazon-ebs.ubuntu"]
    direction   = "download"
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-aws-multi.spdx.json"
  }

  provisioner "hcp-sbom" {
    only        = ["amazon-ebs.ubuntu"]
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-aws-multi.spdx.json"
  }

  provisioner "file" {
    only        = ["azure-arm.ubuntu"]
    direction   = "download"
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-azure-multi.spdx.json"
  }

  provisioner "hcp-sbom" {
    only        = ["azure-arm.ubuntu"]
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-azure-multi.spdx.json"
  }

  provisioner "hcp-sbom" {
    only            = ["docker.ubuntu"]
    auto_generate   = true
    scanner_args    = ["-o", "spdx-json", "--exclude", "./tmp/packer-sbom-runner"]
    scan_path       = "/"
    execute_command = "chmod +x {{.Path}} && {{.Path}} sbom-generate {{.Args}} {{.ScanPath}} > {{.Output}}"
  }
}
