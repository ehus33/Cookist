variable "account_id" {
  description = "The AWS account authorized to own this site."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "account_id must contain exactly 12 digits."
  }
}

variable "deployment_id" {
  description = "The portal-generated 32-character hexadecimal deployment ID."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[a-f0-9]{32}$", var.deployment_id))
    error_message = "deployment_id must contain exactly 32 lowercase hexadecimal characters."
  }
}

variable "region" {
  description = "The enabled commercial AWS Region for the S3 bucket."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[a-z]{2}(-[a-z]+)+-[0-9]+$", var.region))
    error_message = "region must be an AWS Region code."
  }
}

variable "page_html" {
  description = "The complete HTML document rendered by the portal's blank or business template."
  type        = string
  nullable    = false

  validation {
    condition     = length(trimspace(var.page_html)) > 0
    error_message = "page_html must not be empty."
  }
}
