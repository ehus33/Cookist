---
"@cookist/web": minor
---

Add an operator-restricted Terraform deployment workflow for blank sites and business listings.

Embed reusable S3 and CloudFront Terraform files and a provider lock file in the
gateway, with isolated per-site variables and credential-free mock plan tests.

Limit test-phase site creation to four deployments across all users sharing the
worker state directory, including running, failed, and pending deployments.

Add a separately bootstrapped, scoped local AWS worker role and require Cookist
tags on new CloudFront distributions.
