mock_provider "aws" {}

variables {
  account_id    = "123456789012"
  deployment_id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  region        = "us-west-2"
  page_html     = "<!doctype html><html><body><main></main></body></html>"
}

run "blank_site" {
  command = plan

  assert {
    condition     = aws_s3_bucket.site.bucket == "cookist-123456789012-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    error_message = "The site bucket must be scoped to the authorized account and deployment."
  }

  assert {
    condition     = aws_s3_object.index.content == var.page_html
    error_message = "The blank site must upload the supplied HTML unchanged."
  }

  assert {
    condition = (
      aws_s3_bucket_public_access_block.site.block_public_acls &&
      aws_s3_bucket_public_access_block.site.block_public_policy &&
      aws_s3_bucket_public_access_block.site.ignore_public_acls &&
      aws_s3_bucket_public_access_block.site.restrict_public_buckets
    )
    error_message = "The S3 origin must remain private."
  }
}

run "business_site_literal_content" {
  command = plan

  variables {
    page_html = "<html><body><main><h1>Shop</h1><h2>Products</h2><ul><li>$${file(\"secret\")}</li><li>%%{ if true }</li></ul></main></body></html>"
  }

  assert {
    condition     = aws_s3_object.index.content == var.page_html
    error_message = "Menu, product and service content must remain literal data."
  }
}

run "invalid_inputs" {
  command = plan

  variables {
    account_id    = "invalid"
    deployment_id = "../site"
    region        = "not a region"
    page_html     = " "
  }

  expect_failures = [
    var.account_id,
    var.deployment_id,
    var.region,
    var.page_html,
  ]
}
