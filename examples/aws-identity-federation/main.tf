terraform {
  required_providers {
    eg = {
      source = "egdaemon/eg"
    }
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "eg" {
  # account_id = "00000000-0000-0000-0000-000000000000"
}

provider "aws" {
  region = "us-east-2"
}

# the issuer and subject eg presents to your aws account.
data "eg_aws_oidc" "default" {}

# trust eg's identity. the provider and role stay with the aws provider.
resource "aws_iam_openid_connect_provider" "egdaemon" {
  url            = data.eg_aws_oidc.default.issuer_url
  client_id_list = ["sts.amazonaws.com"]
}

locals {
  issuer = trimprefix(data.eg_aws_oidc.default.issuer_url, "https://")
}

resource "aws_iam_role" "egdaemon" {
  name = "egdaemon-workloads"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = { Federated = aws_iam_openid_connect_provider.egdaemon.arn }
      Condition = {
        StringEquals = {
          "${local.issuer}:aud" = "sts.amazonaws.com"
          "${local.issuer}:sub" = data.eg_aws_oidc.default.subject
        }
      }
    }]
  })
}

# register the role with eg.
resource "eg_aws_identity_federation" "default" {
  role_arn    = aws_iam_role.egdaemon.arn
  region      = "us-east-2"
  description = "my account"
}
