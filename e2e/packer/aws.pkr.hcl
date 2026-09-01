packer {
  required_plugins {
    amazon = {
      version = ">= 1.8.2"
      source  = "github.com/hashicorp/amazon"
    }
  }
}

variable "region" {
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
  bucket_name = "verify-aws"
  description = "AWS images built by Dufflebag's cloud verification lane."
}

source "amazon-ebs" "ubuntu" {
  ami_name      = "dufflebag-ci-${var.image_name_suffix}-aws-solo"
  instance_type = "t3.small"
  region        = var.region
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
    Name          = "dufflebag-ci-${var.image_name_suffix}-aws-solo"
    dufflebag-ci  = "packer"
  }
}

build {
  sources = ["source.amazon-ebs.ubuntu"]

  provisioner "shell" {
    inline = [
      "echo dufflebag cloud verification",
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
    direction   = "download"
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-aws-solo.spdx.json"
  }

  provisioner "hcp-sbom" {
    source      = "/tmp/dufflebag.spdx.json"
    destination = "${var.work_dir}/sbom-aws-solo.spdx.json"
  }
}
