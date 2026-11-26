terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "= 6.73.0"
    }
  }
}

variable "account_id" {
  type     = string
  nullable = false

  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "account_id must contain exactly 12 digits."
  }
}

variable "bootstrap_profile" {
  description = "Administrative SSO profile used only to provision this role."
  type        = string
  nullable    = false
}

variable "trusted_role_arn" {
  description = "Exact existing IAM role allowed to assume the local worker role."
  type        = string
  nullable    = false
}

provider "aws" {
  region              = "us-west-2"
  profile             = var.bootstrap_profile
  allowed_account_ids = [var.account_id]
}

data "aws_iam_policy_document" "trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = [var.trusted_role_arn]
    }
  }
}

resource "aws_iam_role" "worker" {
  name                 = "CookistLocalDeploymentWorker"
  description          = "Local Cookist test-site worker; no IAM administration or role passing."
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = 3600
}

resource "aws_iam_role_policy" "worker" {
  name   = "CookistTestSites"
  role   = aws_iam_role.worker.id
  policy = templatefile("${path.module}/permissions.json.tftpl", { account_id = var.account_id })
}

output "worker_role_arn" {
  value = aws_iam_role.worker.arn
}
