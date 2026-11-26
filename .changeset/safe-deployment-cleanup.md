---
"@cookist/web": minor
---

Add confirmed resource deletion for ready and failed deployments, retryable cleanup failures, and serialized Terraform destruction using retained state. Successful cleanup frees a deployment slot; worker IAM permissions must be updated before use.
