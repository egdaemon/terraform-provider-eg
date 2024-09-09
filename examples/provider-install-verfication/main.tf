terraform {
  required_providers {
    eg = {
      source  = "egdaemon/eg"
      version = "0.0.5"
    }
  }
}

# provider "eg" {}

locals {
  exampledir = "${path.module}/rootfs"
}

resource "eg_tar" "default" {
  dynamic "source" {
    for_each = sort(toset(fileset(local.exampledir, "*")))
    content {
      base64   = filebase64("${local.exampledir}/${source.value}")
      location = source.value
    }
  }

  source {
    base64   = base64encode("waaaaaaaaaaaaaaaaaaaaaaaaat")
    location = "example.3.txt"
  }
}

resource "local_file" "default" {
  content_base64       = eg_tar.default.archiveb64
  filename             = "example.tar.gz"
  file_permission      = "0600"
  directory_permission = "0750"
}
