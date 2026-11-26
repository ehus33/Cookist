output "website_url" {
  description = "Public HTTPS URL of the deployed static site."
  value       = format("https://%s", aws_cloudfront_distribution.site.domain_name)
}
