terraform {
  required_providers {
    eg = {
      source = "hashicorp.com/edu/eg"
    }
  }
}

provider "eg" {}

locals {
  exampledir = "${path.module}/rootfs"
}

resource "eg_tar" "archive" {
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
