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
  description = "Administrative profile for provisioning; never used by the gateway."
  type        = string
  nullable    = false
}

variable "region" {
  type    = string
  default = "us-west-2"
}

variable "billing_mode" {
  type    = string
  default = "test"
  validation {
    condition     = contains(["test", "live"], var.billing_mode)
    error_message = "billing_mode must match the gateway Stripe test/live mode."
  }
}

variable "name" {
  type    = string
  default = "cookist-dynamic-test"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,47}$", var.name))
    error_message = "name must contain 3-48 lowercase letters, digits or hyphens."
  }
}

provider "aws" {
  region              = var.region
  profile             = var.bootstrap_profile
  allowed_account_ids = [var.account_id]
}

resource "aws_dynamodb_table" "records" {
  name                        = "${var.name}-records"
  billing_mode                = "PAY_PER_REQUEST"
  hash_key                    = "PK"
  range_key                   = "SK"
  deletion_protection_enabled = true
  attribute {
    name = "PK"
    type = "S"
  }
  attribute {
    name = "SK"
    type = "S"
  }
  server_side_encryption {
    enabled = true
  }
  point_in_time_recovery {
    enabled = true
  }
  on_demand_throughput {
    max_read_request_units  = 100
    max_write_request_units = 25
  }
  tags = {
    CookistManaged  = "dynamic-${var.billing_mode}"
    CookistTenant   = "owner"
    CookistDatabase = "${var.name}-records-${var.billing_mode}"
  }
}

# This first-release test pool has no MFA challenge UI. Do not use it as a
# production authentication configuration; configure MFA/managed login first.
resource "aws_cognito_user_pool" "visitors" {
  name                     = "${var.name}-visitors"
  username_attributes      = ["email"]
  auto_verified_attributes = ["email"]
  deletion_protection      = "ACTIVE"
  mfa_configuration        = "OFF"
  username_configuration {
    case_sensitive = false
  }
  password_policy {
    minimum_length                   = 12
    require_lowercase                = true
    require_uppercase                = true
    require_numbers                  = true
    require_symbols                  = true
    temporary_password_validity_days = 1
  }
  schema {
    name                = "email"
    attribute_data_type = "String"
    required            = true
    mutable             = true
    string_attribute_constraints {
      min_length = "1"
      max_length = "254"
    }
  }
  account_recovery_setting {
    recovery_mechanism {
      name     = "verified_email"
      priority = 1
    }
  }
  tags = { CookistManaged = "dynamic-test" }
}

resource "aws_cognito_user_pool_client" "visitors" {
  name                          = "${var.name}-gateway"
  user_pool_id                  = aws_cognito_user_pool.visitors.id
  generate_secret               = false
  explicit_auth_flows           = ["ALLOW_USER_PASSWORD_AUTH"]
  prevent_user_existence_errors = "ENABLED"
  enable_token_revocation       = true
  access_token_validity         = 15
  id_token_validity             = 15
  refresh_token_validity        = 1
  token_validity_units {
    access_token  = "minutes"
    id_token      = "minutes"
    refresh_token = "days"
  }
  read_attributes  = ["email", "email_verified"]
  write_attributes = ["email"]
}

output "gateway_configuration" {
  value = {
    COOKIST_DYNAMIC_REGION    = var.region
    COOKIST_DYNAMIC_TABLE     = aws_dynamodb_table.records.name
    COOKIST_VISITOR_POOL_ID   = aws_cognito_user_pool.visitors.id
    COOKIST_VISITOR_CLIENT_ID = aws_cognito_user_pool_client.visitors.id
  }
}

output "records_table_arn" {
  description = "Operator-only table; customer data uses separate tagged tables."
  value       = aws_dynamodb_table.records.arn
}

locals {
  database_scope = "${aws_dynamodb_table.records.name}-${var.billing_mode}"
  tenant_tables  = "arn:aws:dynamodb:${var.region}:${var.account_id}:table/${local.database_scope}-tenant-*"
}

data "aws_iam_policy_document" "gateway_database" {
  statement {
    sid       = "OperatorRecords"
    actions   = ["dynamodb:GetItem", "dynamodb:Query", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:ConditionCheckItem", "dynamodb:BatchWriteItem", "dynamodb:DescribeTable"]
    resources = [aws_dynamodb_table.records.arn]
  }
  statement {
    sid = "ScopedOrganizationRecords"
    actions = [
      "dynamodb:GetItem", "dynamodb:Query", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:ConditionCheckItem",
      "dynamodb:BatchWriteItem", "dynamodb:DescribeTable", "dynamodb:ListTagsOfResource",
      "dynamodb:UpdateTable", "dynamodb:UpdateContinuousBackups", "dynamodb:DeleteTable"
    ]
    resources = [local.tenant_tables]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/CookistDatabase"
      values   = [local.database_scope]
    }
  }
  statement {
    sid       = "CreateTaggedOrganizationTables"
    actions   = ["dynamodb:CreateTable", "dynamodb:TagResource"]
    resources = [local.tenant_tables]
    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/CookistDatabase"
      values   = [local.database_scope]
    }
    condition {
      test     = "Null"
      variable = "aws:RequestTag/CookistTenant"
      values   = ["false"]
    }
    condition {
      test     = "ForAllValues:StringEquals"
      variable = "aws:TagKeys"
      values   = ["CookistTenant", "CookistDatabase"]
    }
    condition {
      test     = "Null"
      variable = "aws:TagKeys"
      values   = ["false"]
    }
  }
  statement {
    sid       = "ReportedDatabaseCosts"
    actions   = ["ce:ListCostAllocationTags", "aws-portal:ViewBilling"]
    resources = ["*"]
  }
  statement {
    sid       = "DatabaseBillingView"
    actions   = ["ce:GetCostAndUsage", "ce:GetDimensionValues"]
    resources = ["arn:aws:billing::${var.account_id}:billingview/*"]
  }
}

output "database_cost_scope" {
  value = local.database_scope
}

output "gateway_database_policy" {
  description = "Review and attach to the gateway role; does not grant cost-tag activation."
  value       = data.aws_iam_policy_document.gateway_database.json
}
